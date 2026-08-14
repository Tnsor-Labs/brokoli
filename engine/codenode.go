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

class _LazyRows:
    """ADR-019 Milestone 1.5: a disk-backed, RE-ITERABLE view of the input.

    Iterating streams the NDJSON file line by line in constant memory, and
    every fresh iteration is a fresh pass over the file — so a script that
    loops over rows twice still works. len(rows), rows[i], and slicing
    transparently materialize the whole list first: full backward
    compatibility at the old memory cost, paid only by scripts that
    actually need whole-dataset access."""
    def __init__(self, path):
        self._path = path
        self._m = None
    def __iter__(self):
        if self._m is not None:
            return iter(self._m)
        def _gen():
            with open(self._path, 'r') as f:
                for line in f:
                    if line.strip():
                        yield json.loads(line)
        return _gen()
    def _force(self):
        if self._m is None:
            self._m = [r for r in self]
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
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
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

	// JSON fallback
	var output CodeNodeOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		return nil, stderrStr, fmt.Errorf("script output is not valid JSON: %w\nstdout: %s", err, stdout.String())
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
