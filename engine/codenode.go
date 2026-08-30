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

	"github.com/Tnsor-Labs/brokoli/pkg/codeexec"
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
	return ExecuteCodeNodeProgress(parent, script, input, nodeConfig, runParams, timeoutSec, nil)
}

// ExecuteCodeNodeProgress additionally consumes the script's typed
// progress reports (ADR-029; pool path only — the legacy transport
// emitted #PROGRESS: markers on stderr and always discarded them).
func ExecuteCodeNodeProgress(parent context.Context, script string, input *common.DataSet, nodeConfig map[string]interface{}, runParams map[string]string, timeoutSec int, progress func(percent int, message string)) (*common.DataSet, string, error) {
	if script == "" {
		return nil, "", fmt.Errorf("code node requires a 'script' in config")
	}
	if timeoutSec <= 0 {
		timeoutSec = 30
	}
	if codeexec.PoolEnabled() {
		return executeCodeNodePooled(parent, script, input, nodeConfig, runParams, timeoutSec, progress)
	}

	// Contract v2 (ADR-029): the wrapper is the embedded, versioned
	// pywrapper library materialized to disk; the user script travels as
	// its own file (BROKED_SCRIPT) and is exec'd in the wrapper's
	// globals — no more %-interpolation, and tracebacks carry the user
	// script's own line numbers.
	wrapperFile, err := codeexec.WrapperPath()
	if err != nil {
		return nil, "", fmt.Errorf("materialize code wrapper: %w", err)
	}
	tmpDir := os.TempDir()
	scriptFile := filepath.Join(tmpDir, fmt.Sprintf("brokoli_code_%d.py", time.Now().UnixNano()))
	if err := os.WriteFile(scriptFile, []byte(script), 0o600); err != nil {
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

	limits := codeexec.Resolve(nodeConfig)

	cmd := exec.CommandContext(ctx, pythonPath, wrapperFile)
	cmd.Env = append(os.Environ(),
		"BROKED_SCRIPT="+scriptFile,
		"BROKED_CONFIG="+string(configJSON),
		"BROKED_PARAMS="+string(paramsJSON),
	)
	cmd.Env = append(cmd.Env, limits.Env()...)
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

	err = cmd.Run()
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
		if ctx.Err() == nil {
			if lerr := limitBreachError(err, stderrStr, limits); lerr != nil {
				return nil, stderrStr, lerr
			}
		}
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
