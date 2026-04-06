package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"
)

// CompiledPipeline is the result of compiling a Python pipeline file.
type CompiledPipeline struct {
	Name        string                   `json:"name"`
	PipelineID  string                   `json:"pipeline_id"`
	Description string                   `json:"description"`
	Schedule    string                   `json:"schedule"`
	Enabled     bool                     `json:"enabled"`
	Nodes       []map[string]interface{} `json:"nodes"`
	Edges       []map[string]interface{} `json:"edges"`
	Tags        []string                 `json:"tags"`
	SLADeadline string                   `json:"sla_deadline,omitempty"`
	SLATimezone string                   `json:"sla_timezone,omitempty"`
	DependsOn   []string                 `json:"depends_on,omitempty"`
}

// CompilePythonPipeline runs a Python file and extracts pipeline definitions.
// It uses the brokoli SDK to capture Pipeline objects without executing data processing.
// Timeout: 10 seconds. Returns one or more pipelines defined in the file.
func CompilePythonPipeline(filePath string) ([]CompiledPipeline, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Python script that imports the file and captures pipeline definitions
	script := fmt.Sprintf(`
import json, sys, importlib.util

# Monkey-patch Pipeline to capture definitions
pipelines = []
try:
    from brokoli.pipeline import Pipeline
    _orig_exit = Pipeline.__exit__
    def _capture_exit(self, *args):
        d = self.to_json()
        # Use the pipeline name as pipeline_id (slug)
        name = self.name
        pid = name.lower().replace(' ', '-')
        import re
        pid = re.sub(r'[^a-z0-9-]', '', pid)
        pid = re.sub(r'-+', '-', pid).strip('-')
        d['pipeline_id'] = pid
        pipelines.append(d)
        return _orig_exit(self, *args)
    Pipeline.__exit__ = _capture_exit
except ImportError:
    print(json.dumps({"error": "brokoli SDK not installed"}))
    sys.exit(1)

try:
    spec = importlib.util.spec_from_file_location("user_pipeline", %q)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
except Exception as e:
    print(json.dumps({"error": str(e)}))
    sys.exit(1)

print(json.dumps(pipelines))
`, filePath)

	cmd := exec.CommandContext(ctx, "python3", "-c", script)
	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("compile timeout (10s)")
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			// Check if stdout has the JSON error
			var errResp struct {
				Error string `json:"error"`
			}
			if json.Unmarshal(output, &errResp) == nil && errResp.Error != "" {
				return nil, fmt.Errorf("compile error: %s", errResp.Error)
			}
			return nil, fmt.Errorf("compile failed: %s", string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("compile failed: %w", err)
	}

	var result []CompiledPipeline
	if err := json.Unmarshal(output, &result); err != nil {
		// Try error response
		var errResp struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(output, &errResp) == nil && errResp.Error != "" {
			return nil, fmt.Errorf("compile error: %s", errResp.Error)
		}
		return nil, fmt.Errorf("parse compile output: %w", err)
	}

	return result, nil
}
