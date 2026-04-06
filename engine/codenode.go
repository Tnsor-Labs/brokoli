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

# Read input — NDJSON mode is fastest for large datasets
if _input_ndjson:
    with open(_input_ndjson, 'r') as _f:
        rows = [json.loads(line) for line in _f if line.strip()]
    columns = list(rows[0].keys()) if rows else []
    print(f"#PROGRESS:5 Loaded {len(rows)} rows via NDJSON (fast mode)", file=sys.stderr)
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

# Output defaults to passthrough
output_data = {"columns": columns, "rows": rows}

# --- USER SCRIPT START ---
%s
# --- USER SCRIPT END ---

# Write output
_out_cols = output_data.get("columns", columns)
_out_rows = output_data.get("rows", [])

if _output_ndjson and _out_rows:
    with open(_output_ndjson, 'w') as _f:
        for row in _out_rows:
            _f.write(json.dumps(row) + '\n')
    print(f"#PROGRESS:95 Wrote {len(_out_rows)} rows via NDJSON", file=sys.stderr)
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
