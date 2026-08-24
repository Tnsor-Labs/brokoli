package engine

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// CodeNodeInput is the JSON structure passed to the Python script via stdin (small datasets).
type CodeNodeInput struct {
	Columns []string                 `json:"columns"`
	Rows    []map[string]interface{} `json:"rows"`
	Config  map[string]interface{}   `json:"config"`
	Params  map[string]string        `json:"params"`
}

// CodeNodeOutput is the JSON structure expected from the Python script on stdout.
type CodeNodeOutput struct {
	Columns []string                 `json:"columns"`
	Rows    []map[string]interface{} `json:"rows"`
}

// Threshold: above this many rows, use file-based transfer instead of stdin/stdout.
const fileModeThreshold = 10000

// Python wrapper with auto-detection of transfer mode and pyarrow.
const pythonWrapper = `
import sys, json, os, csv

# Transfer mode detection
_input_csv = os.environ.get("BROKED_INPUT_CSV", "")
_output_csv = os.environ.get("BROKED_OUTPUT_CSV", "")
_input_ndjson = os.environ.get("BROKED_INPUT_NDJSON", "")
_output_ndjson = os.environ.get("BROKED_OUTPUT_NDJSON", "")
_output_columns = os.environ.get("BROKED_OUTPUT_COLUMNS", "")
_input_columns_env = os.environ.get("BROKED_INPUT_COLUMNS", "")

# Journals outlive the passes that wrote them (they become the file later
# passes read), so the last one is still needed when the script ends. Removing
# them at exit keeps a long-running worker from accumulating one temp file per
# mutating node execution.
_journals = set()

def _drop_journals():
    for _p in list(_journals):
        try:
            os.remove(_p)
        except OSError:
            pass
    _journals.clear()

import atexit
atexit.register(_drop_journals)

class _MutationAfterPass(Exception):
    pass

class _Row(dict):
    """A row that tells its pass when it is written to.

    Reading is a plain dict. Writing marks the pass dirty so the edit is kept
    instead of being discarded the next time the file is read.
    """
    __slots__ = ("_pass",)
    def __init__(self, data, owner):
        dict.__init__(self, data)
        self._pass = owner
    def __setitem__(self, k, v):
        self._pass._touched(self); dict.__setitem__(self, k, v)
    def __delitem__(self, k):
        self._pass._touched(self); dict.__delitem__(self, k)
    def update(self, *a, **kw):
        self._pass._touched(self); return dict.update(self, *a, **kw)
    def setdefault(self, k, d=None):
        self._pass._touched(self); return dict.setdefault(self, k, d)
    def pop(self, *a):
        self._pass._touched(self); return dict.pop(self, *a)
    def popitem(self):
        self._pass._touched(self); return dict.popitem(self)
    def clear(self):
        self._pass._touched(self); return dict.clear(self)

class _Pass:
    """One iteration over the input file.

    State lives here rather than on the view so that nested iteration --
    for a in rows: for b in rows: -- gives each loop its own cursor instead of
    the inner one trampling the outer one's position.
    """
    def __init__(self, view, src):
        self.view = view
        self.src = src
        self.jf = None          # journal handle, opened on the first edit
        self.jpath = None
        self.live = None        # row currently yielded
        self.live_at = None     # byte offset of that row's line
        self.resume_at = 0      # byte offset just past the last settled row

    def _touched(self, row):
        if row is not self.live:
            # The pass has moved past this row, so there is no correct place
            # to put the edit. Refusing is the only honest answer available:
            # writing it anywhere else would be inventing an ordering.
            raise _MutationAfterPass()
        if self.jf is None:
            self._open_journal()

    def _open_journal(self):
        import tempfile
        fd, jpath = tempfile.mkstemp(prefix="brokoli-rows-", suffix=".ndjson")
        os.close(fd)
        # Rows before the first edit are untouched, so copy their bytes rather
        # than decoding and re-encoding them.
        with open(self.src, "rb") as src, open(jpath, "wb") as dst:
            left = self.live_at
            while left > 0:
                chunk = src.read(min(1 << 20, left))
                if not chunk:
                    break
                dst.write(chunk); left -= len(chunk)
        self.jf = open(jpath, "a")
        self.jpath = jpath

    def settle(self):
        if self.jf is not None and self.live is not None:
            self.jf.write(json.dumps(self.live) + "\n")
        self.live = None

    def finish(self):
        if self.jf is None:
            self.live = None
            return
        # A pass abandoned early (break) leaves rows unread. They are unchanged
        # but still part of the dataset, so carry them over verbatim.
        with open(self.src, "rb") as src:
            src.seek(self.resume_at)
            while True:
                chunk = src.read(1 << 20)
                if not chunk:
                    break
                self.jf.write(chunk.decode("utf-8"))
        self.jf.close(); self.jf = None
        self.view._adopt(self.jpath)
        self.jpath = None

class _LazyRows:
    """A disk-backed, re-iterable, MUTABLE view of the input.

    Iterating streams the NDJSON file line by line in constant memory, and
    every fresh iteration is a fresh pass over the file -- so a script that
    loops over rows twice still works. len(rows), rows[i], and slicing
    transparently materialize the whole list first.

    Mutations survive. A row written to during a pass is journaled to disk as
    the pass moves past it, and the journal becomes the file later passes
    read, so the ordinary idiom

        for r in rows:
            r["name"] = r["name"].upper()
        output_data = {"columns": columns, "rows": rows}

    means what it says. It used to silently produce the ORIGINAL rows: the
    mutated dicts were dropped at the end of the pass and the output re-read
    the untouched file, with nothing reporting it.

    The cost is disk, paid only by scripts that actually mutate -- a read-only
    pass never opens a journal. Memory stays constant either way, which is the
    point: the alternative is holding the whole dataset just to keep the edits.
    """
    def __init__(self, path):
        self._path = path
        self._m = None
        self._owned = None      # a journal this view created and may delete

    def _adopt(self, jpath):
        old = self._owned
        self._path = jpath
        self._owned = jpath
        _journals.add(jpath)
        if old and old != jpath:
            _journals.discard(old)
            try:
                os.remove(old)
            except OSError:
                pass

    def __iter__(self):
        if self._m is not None:
            return iter(self._m)
        return self._stream()

    def _stream(self):
        p = _Pass(self, self._path)
        try:
            with open(p.src, "r") as f:
                while True:
                    at = f.tell()
                    line = f.readline()
                    if not line:
                        break
                    if not line.strip():
                        p.resume_at = f.tell()
                        continue
                    p.settle()
                    p.resume_at = at
                    row = _Row(json.loads(line), p)
                    p.live, p.live_at = row, at
                    yield row
                    p.resume_at = f.tell()
                p.settle()
        finally:
            p.settle()
            p.finish()

    def _force(self):
        if self._m is None:
            self._m = [dict(r) for r in self]
        return self._m
    def __len__(self):
        return len(self._force())
    def __getitem__(self, i):
        return self._force()[i]
    def __bool__(self):
        if self._m is not None:
            return bool(self._m)
        for _ in self:
            return True
        return False

def _brokoli_excepthook(etype, value, tb):
    if etype is _MutationAfterPass:
        sys.stderr.write(
            "BROKOLI_ERROR: a row was modified after the loop that read it had already moved on.\n"
            "Rows stream from disk one at a time, so an edit has to be made while the row is the\n"
            "one being handled. Edit it inside the loop body, or collect what you need into a new\n"
            "list and output that instead. This is refused rather than ignored because applying it\n"
            "would mean guessing where the row belongs.\n")
        sys.exit(3)
    sys.__excepthook__(etype, value, tb)
sys.excepthook = _brokoli_excepthook
_use_file = bool(_input_csv) or bool(_input_ndjson)

# Try to use pyarrow/pandas for faster processing
_has_pyarrow = False
try:
    import pyarrow
    import pyarrow.csv as pa_csv
    _has_pyarrow = True
except ImportError:
    pass

_has_pandas = False
try:
    import pandas as pd
    _has_pandas = True
except ImportError:
    pass

# Read input — NDJSON mode attaches lazily (constant memory until the
# script itself demands otherwise via len()/indexing)
if _input_ndjson:
    rows = _LazyRows(_input_ndjson)
    if _input_columns_env:
        columns = json.loads(_input_columns_env)
    else:
        columns = []
        for _first in rows:
            columns = list(_first.keys())
            break
    print("#PROGRESS:5 Input attached lazily via NDJSON (constant memory)", file=sys.stderr)
elif _use_file and _has_pyarrow:
    _table = pa_csv.read_csv(_input_csv)
    columns = _table.column_names
    rows = _table.to_pylist()
    print(f"#PROGRESS:5 Loaded {len(rows)} rows via Arrow (fast mode)", file=sys.stderr)
elif _use_file and _has_pandas:
    _df = pd.read_csv(_input_csv, keep_default_na=False)
    columns = list(_df.columns)
    rows = _df.to_dict('records')
    print(f"#PROGRESS:5 Loaded {len(rows)} rows via pandas", file=sys.stderr)
elif _use_file:
    with open(_input_csv, 'r') as f:
        reader = csv.DictReader(f)
        columns = reader.fieldnames or []
        rows = list(reader)
    print(f"#PROGRESS:5 Loaded {len(rows)} rows via CSV", file=sys.stderr)
else:
    _raw = sys.stdin.read()
    input_data = json.loads(_raw) if _raw.strip() else {"columns": [], "rows": [], "config": {}, "params": {}}
    columns = input_data.get("columns", [])
    rows = input_data.get("rows", [])

# Config and params via env
_config_json = os.environ.get("BROKED_CONFIG", "{}")
_params_json = os.environ.get("BROKED_PARAMS", "{}")
config = json.loads(_config_json)
params = json.loads(_params_json)

# Streaming output (ADR-019 Milestone 1.5): emit(row) writes straight to
# the output file, one row at a time — a script that emits instead of
# building a list holds no output in memory at all. Column order for
# emitted output is the first emitted row's own key order (Python dicts
# preserve insertion order); output_data is ignored once emit() is used.
_emit_state = {"f": None, "count": 0, "cols": None, "mode": False}
def begin_emit(columns=None):
    """Declare emit-mode output explicitly. Optional before emit() when at
    least one row will be emitted — but a filter that might match nothing
    MUST call this, or its zero-emit case falls back to the passthrough
    default and outputs the entire input. Also fixes the column order
    instead of inferring it from the first emitted row."""
    _emit_state["mode"] = True
    if columns is not None:
        _emit_state["cols"] = list(columns)
def emit(row):
    if not _emit_state["mode"]:
        begin_emit()
    if _emit_state["f"] is None:
        if not _output_ndjson:
            raise RuntimeError("emit() requires file-mode output; build output_data instead")
        _emit_state["f"] = open(_output_ndjson, 'w')
    _emit_state["f"].write(json.dumps(row) + '\n')
    if _emit_state["cols"] is None:
        try:
            _emit_state["cols"] = list(row.keys())
        except AttributeError:
            _emit_state["cols"] = []
    _emit_state["count"] += 1

# Output defaults to passthrough
output_data = {"columns": columns, "rows": rows}

# --- USER SCRIPT START ---
%s
# --- USER SCRIPT END ---

# Write output
if _emit_state["mode"]:
    if _emit_state["f"] is not None:
        _emit_state["f"].close()
    if _emit_state["count"] == 0:
        # Zero emitted rows is a real, intentional result (a filter that
        # matched nothing) — the empty output travels as stdout JSON,
        # preserving the no-rows/no-file contract.
        print(json.dumps({"columns": _emit_state["cols"] or [], "rows": []}))
    else:
        if _output_columns:
            with open(_output_columns, 'w') as _f:
                json.dump(list(_emit_state["cols"] or []), _f)
        print(f"#PROGRESS:95 Wrote {_emit_state['count']} rows via emit", file=sys.stderr)
    _out_rows = None  # emitted output wins; skip every branch below
    _out_cols = []
else:
    _out_cols = output_data.get("columns", columns)
    _out_rows = output_data.get("rows", [])

if _emit_state["mode"]:
    pass
elif _output_ndjson and _out_rows is not None:
    # Iterated, not len()'d: _out_rows may be a list, a generator (a
    # fully-streaming script), or the untouched lazy passthrough — all
    # write in constant memory here.
    _written = 0
    with open(_output_ndjson, 'w') as _f:
        for row in _out_rows:
            _f.write(json.dumps(row) + '\n')
            _written += 1
    if _written == 0:
        # Preserve the original contract exactly: no rows means no NDJSON
        # file, and the result travels as stdout JSON instead.
        os.remove(_output_ndjson)
        print(json.dumps({"columns": list(_out_cols) if _out_cols is not None else [], "rows": []}))
    else:
        if _output_columns:
            # Column-order sidecar: NDJSON rows are JSON objects, whose key
            # order Go recovers from a map (arbitrary). The script's own
            # declared column order in output_data["columns"] survives via
            # this file instead of being lost the way plain file mode always
            # lost it.
            with open(_output_columns, 'w') as _f:
                json.dump(list(_out_cols), _f)
        print(f"#PROGRESS:95 Wrote {_written} rows via NDJSON", file=sys.stderr)
elif _output_csv and _out_rows:
    if _has_pandas:
        _df_out = pd.DataFrame(_out_rows, columns=_out_cols)
        _df_out.to_csv(_output_csv, index=False)
        print(f"#PROGRESS:95 Wrote {len(_out_rows)} rows via pandas", file=sys.stderr)
    else:
        with open(_output_csv, 'w', newline='') as f:
            writer = csv.DictWriter(f, fieldnames=_out_cols, extrasaction='ignore')
            writer.writeheader()
            writer.writerows(_out_rows)
        print(f"#PROGRESS:95 Wrote {len(_out_rows)} rows via CSV", file=sys.stderr)
else:
    _rows_val = output_data.get("rows", [])
    if not isinstance(_rows_val, list):
        output_data["rows"] = [r for r in _rows_val]
    print(json.dumps(output_data))
`

// ExecuteCodeNode runs a Python script with the given input data.
// For datasets > 10K rows, uses CSV temp files instead of JSON stdin/stdout for 5-10x speed.
// Auto-detects pyarrow/pandas for even faster transfers.
func ExecuteCodeNode(script string, input *common.DataSet, nodeConfig map[string]interface{}, runParams map[string]string, timeoutSec int) (*common.DataSet, string, error) {
	return ExecuteCodeNodeContext(context.Background(), script, input, nodeConfig, runParams, timeoutSec)
}

// ExecuteCodeNodeContext is ExecuteCodeNode with caller-controlled cancellation.
// The context is combined with the node's own timeout so remote workers can
// terminate an in-flight code WorkOrder when its run is cancelled.
func ExecuteCodeNodeContext(parent context.Context, script string, input *common.DataSet, nodeConfig map[string]interface{}, runParams map[string]string, timeoutSec int) (*common.DataSet, string, error) {
	if script == "" {
		return nil, "", fmt.Errorf("code node requires a 'script' in config")
	}
	if timeoutSec <= 0 {
		timeoutSec = 30
	}

	fullScript := fmt.Sprintf(pythonWrapper, script)

	tmpDir := os.TempDir()
	scriptFile := filepath.Join(tmpDir, fmt.Sprintf("brokoli_code_%d.py", time.Now().UnixNano()))
	if err := os.WriteFile(scriptFile, []byte(fullScript), 0o600); err != nil {
		return nil, "", fmt.Errorf("write script file: %w", err)
	}
	defer os.Remove(scriptFile)

	// Decide transfer mode: file-based for large datasets, stdin/stdout for small
	// NDJSON is preferred over CSV — faster for pyarrow and preserves JSON types (no string coercion).
	useFileMode := input != nil && len(input.Rows) >= fileModeThreshold
	transferMode := TransferJSON // default: small datasets via stdin
	var inputCSV, outputCSV string
	var inputNDJSON, outputNDJSON string

	if useFileMode {
		// Try NDJSON first (faster, type-preserving)
		inputNDJSON = filepath.Join(tmpDir, fmt.Sprintf("brokoli_in_%d.ndjson", time.Now().UnixNano()))
		outputNDJSON = filepath.Join(tmpDir, fmt.Sprintf("brokoli_out_%d.ndjson", time.Now().UnixNano()))
		defer os.Remove(inputNDJSON)
		defer os.Remove(outputNDJSON)

		if err := WriteArrowJSON(inputNDJSON, input); err != nil {
			// Fall back to CSV
			os.Remove(inputNDJSON)
			inputNDJSON = ""
			outputNDJSON = ""

			inputCSV = filepath.Join(tmpDir, fmt.Sprintf("brokoli_in_%d.csv", time.Now().UnixNano()))
			outputCSV = filepath.Join(tmpDir, fmt.Sprintf("brokoli_out_%d.csv", time.Now().UnixNano()))
			defer os.Remove(inputCSV)
			defer os.Remove(outputCSV)

			if err := writeCSVFile(input, inputCSV); err != nil {
				// Fall back to JSON stdin mode
				useFileMode = false
			} else {
				transferMode = TransferCSV
			}
		} else {
			transferMode = TransferArrow
		}
	}

	// Prepare JSON input for small datasets
	var inputJSON []byte
	if !useFileMode {
		if input != nil {
			inp := CodeNodeInput{
				Columns: input.Columns,
				Config:  nodeConfig,
				Params:  runParams,
			}
			inp.Rows = make([]map[string]interface{}, len(input.Rows))
			for i, row := range input.Rows {
				inp.Rows[i] = map[string]interface{}(row)
			}
			var err error
			inputJSON, err = json.Marshal(inp)
			if err != nil {
				return nil, "", fmt.Errorf("marshal input: %w", err)
			}
		} else {
			inputJSON = []byte("{}")
		}
	}

	// Config and params via env (always small, fast)
	configJSON, _ := json.Marshal(nodeConfig)
	paramsJSON, _ := json.Marshal(runParams)

	// Python path: custom or default
	pythonPath := "python3"
	if pp, ok := nodeConfig["python_path"].(string); ok && pp != "" {
		pythonPath = pp
	}

	// Execute
	ctx, cancel := context.WithTimeout(parent, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, pythonPath, scriptFile)
	cmd.Env = append(os.Environ(),
		"BROKED_CONFIG="+string(configJSON),
		"BROKED_PARAMS="+string(paramsJSON),
	)
	if useFileMode && transferMode == TransferArrow {
		cmd.Env = append(cmd.Env,
			"BROKED_INPUT_NDJSON="+inputNDJSON,
			"BROKED_OUTPUT_NDJSON="+outputNDJSON,
		)
		cmd.Stdin = bytes.NewReader([]byte(""))
	} else if useFileMode {
		cmd.Env = append(cmd.Env,
			"BROKED_INPUT_CSV="+inputCSV,
			"BROKED_OUTPUT_CSV="+outputCSV,
		)
		cmd.Stdin = bytes.NewReader([]byte(""))
	} else {
		cmd.Stdin = bytes.NewReader(inputJSON)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	stderrStr := stderr.String()

	// Parse progress messages from stderr
	var cleanStderr []string
	for _, line := range strings.Split(stderrStr, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#PROGRESS:") {
			continue
		}
		cleanStderr = append(cleanStderr, line)
	}
	stderrStr = strings.Join(cleanStderr, "\n")

	if ctx.Err() == context.DeadlineExceeded {
		return nil, stderrStr, fmt.Errorf("script timed out after %ds", timeoutSec)
	}
	if err != nil {
		return nil, stderrStr, fmt.Errorf("script failed: %w\nstderr: %s", err, stderrStr)
	}

	// Read output: try NDJSON first (fastest), then CSV, then JSON stdout
	if useFileMode && transferMode == TransferArrow {
		if _, statErr := os.Stat(outputNDJSON); statErr == nil {
			ds, readErr := ReadArrowJSON(outputNDJSON)
			if readErr == nil {
				return ds, stderrStr, nil
			}
		}
	}
	if useFileMode && (transferMode == TransferCSV || transferMode == TransferArrow) {
		if outputCSV != "" {
			if _, statErr := os.Stat(outputCSV); statErr == nil {
				ds, readErr := readCSVFile(outputCSV)
				if readErr == nil {
					return ds, stderrStr, nil
				}
			}
		}
	}

	// JSON fallback. UseNumber for the same reason the NDJSON reader uses it
	// (#334): a plain decode turns every number into a float64, and a script
	// returning an id of 1000000 would have it rendered downstream as
	// "1e+06". Normalising here keeps a script's numbers the same shape as a
	// database's.
	var output CodeNodeOutput
	dec := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	dec.UseNumber()
	if err := dec.Decode(&output); err != nil {
		return nil, stderrStr, fmt.Errorf("script output is not valid JSON: %w\nstdout: %s", err, stdout.String())
	}
	for _, row := range output.Rows {
		for k, v := range row {
			row[k] = normalizeJSONNumbers(v)
		}
	}

	ds := &common.DataSet{
		Columns: output.Columns,
		Rows:    make([]common.DataRow, len(output.Rows)),
	}
	for i, row := range output.Rows {
		ds.Rows[i] = common.DataRow(row)
	}

	return ds, stderrStr, nil
}

// writeCSVFile writes a DataSet to a CSV temp file.
func writeCSVFile(ds *common.DataSet, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w := csv.NewWriter(f)
	defer w.Flush()

	// Header
	if err := w.Write(ds.Columns); err != nil {
		return err
	}

	// Rows
	for _, row := range ds.Rows {
		record := make([]string, len(ds.Columns))
		for i, col := range ds.Columns {
			if v, ok := row[col]; ok && v != nil {
				record[i] = fmt.Sprintf("%v", v)
			}
		}
		if err := w.Write(record); err != nil {
			return err
		}
	}

	return w.Error()
}

// readCSVFile reads a CSV file back into a DataSet.
func readCSVFile(path string) (*common.DataSet, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)

	// Read header
	columns, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}

	// Read all rows
	var rows []common.DataRow
	for {
		record, err := r.Read()
		if err != nil {
			break
		}
		row := make(common.DataRow, len(columns))
		for i, col := range columns {
			if i < len(record) {
				row[col] = record[i]
			}
		}
		rows = append(rows, row)
	}

	return &common.DataSet{
		Columns: columns,
		Rows:    rows,
	}, nil
}
