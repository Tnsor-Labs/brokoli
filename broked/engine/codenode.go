package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/hc12r/brokolisql-go/pkg/common"
)

// CodeNodeInput is the JSON structure passed to the Python script via stdin.
type CodeNodeInput struct {
	Columns []string                 `json:"columns"`
	Rows    []map[string]interface{} `json:"rows"`
	Config  map[string]interface{}   `json:"config"`  // node config (minus script)
	Params  map[string]string        `json:"params"`  // pipeline run params
}

// CodeNodeOutput is the JSON structure expected from the Python script on stdout.
type CodeNodeOutput struct {
	Columns []string                 `json:"columns"`
	Rows    []map[string]interface{} `json:"rows"`
}

// The Python wrapper that gets prepended to user scripts.
// Provides `input_data` (dict with columns/rows/config/params) and
// expects the script to set `output_data` (dict with columns/rows).
const pythonWrapper = `
import sys, json

# Read input from stdin
_raw = sys.stdin.read()
input_data = json.loads(_raw) if _raw.strip() else {"columns": [], "rows": [], "config": {}, "params": {}}

# Convenience variables
columns = input_data.get("columns", [])
rows = input_data.get("rows", [])
config = input_data.get("config", {})
params = input_data.get("params", {})

# Output defaults to passthrough
output_data = {"columns": columns, "rows": rows}

# --- USER SCRIPT START ---
%s
# --- USER SCRIPT END ---

# Write output to stdout
print(json.dumps(output_data))
`

// ExecuteCodeNode runs a Python script with the given input data.
func ExecuteCodeNode(script string, input *common.DataSet, nodeConfig map[string]interface{}, runParams map[string]string, timeoutSec int) (*common.DataSet, string, error) {
	if script == "" {
		return nil, "", fmt.Errorf("code node requires a 'script' in config")
	}

	if timeoutSec <= 0 {
		timeoutSec = 30
	}

	// Build the full Python script
	fullScript := fmt.Sprintf(pythonWrapper, script)

	// Write to temp file (more reliable than stdin for larger scripts)
	tmpDir := os.TempDir()
	scriptFile := filepath.Join(tmpDir, fmt.Sprintf("broked_code_%d.py", time.Now().UnixNano()))
	if err := os.WriteFile(scriptFile, []byte(fullScript), 0o600); err != nil {
		return nil, "", fmt.Errorf("write script file: %w", err)
	}
	defer os.Remove(scriptFile)

	// Prepare input JSON
	var inputJSON []byte
	if input != nil {
		inp := CodeNodeInput{
			Columns: input.Columns,
			Config:  nodeConfig,
			Params:  runParams,
		}
		// Convert DataRow to plain maps
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

	// Execute Python
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "python3", scriptFile)
	cmd.Stdin = bytes.NewReader(inputJSON)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	stderrStr := stderr.String()

	if ctx.Err() == context.DeadlineExceeded {
		return nil, stderrStr, fmt.Errorf("script timed out after %ds", timeoutSec)
	}
	if err != nil {
		return nil, stderrStr, fmt.Errorf("script failed: %w\nstderr: %s", err, stderrStr)
	}

	// Parse output
	var output CodeNodeOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		return nil, stderrStr, fmt.Errorf("script output is not valid JSON: %w\nstdout: %s", err, stdout.String())
	}

	// Convert back to DataSet
	ds := &common.DataSet{
		Columns: output.Columns,
		Rows:    make([]common.DataRow, len(output.Rows)),
	}
	for i, row := range output.Rows {
		ds.Rows[i] = common.DataRow(row)
	}

	return ds, stderrStr, nil
}
