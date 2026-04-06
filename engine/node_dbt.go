package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// runDBT executes a dbt command (run, test, build, seed, snapshot) and returns
// structured results. Requires dbt-core installed and a dbt project directory.
func (r *Runner) runDBT(node models.Node) (*common.DataSet, error) {
	command, _ := node.Config["command"].(string)
	projectDir, _ := node.Config["project_dir"].(string)
	profiles, _ := node.Config["profiles_dir"].(string)
	target, _ := node.Config["target"].(string)
	selectModels, _ := node.Config["select"].(string)
	varsJSON, _ := node.Config["vars"].(string)

	if command == "" {
		command = "run"
	}

	validCommands := map[string]bool{
		"run": true, "test": true, "build": true, "seed": true,
		"snapshot": true, "compile": true, "ls": true,
	}
	if !validCommands[command] {
		return nil, fmt.Errorf("dbt: unsupported command %q (allowed: run, test, build, seed, snapshot, compile, ls)", command)
	}

	if projectDir == "" {
		projectDir = "."
	}

	// Build dbt command args
	args := []string{command}
	args = append(args, "--project-dir", projectDir)

	if profiles != "" {
		args = append(args, "--profiles-dir", profiles)
	}
	if target != "" {
		args = append(args, "--target", target)
	}
	if selectModels != "" {
		args = append(args, "--select", selectModels)
	}
	if varsJSON != "" {
		args = append(args, "--vars", varsJSON)
	}

	// Output format: JSON for machine-readable results
	args = append(args, "--output", "json", "--no-use-colors")

	r.log(node.ID, models.LogLevelInfo, "Running: dbt %s", strings.Join(args, " "))

	cmd := exec.Command("dbt", args...)
	cmd.Dir = projectDir
	cmd.Env = append(os.Environ(), "DBT_PROFILES_DIR="+profiles)

	start := time.Now()
	output, err := cmd.CombinedOutput()
	duration := time.Since(start)

	outputStr := string(output)

	// Log dbt output line by line
	for _, line := range strings.Split(outputStr, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			r.log(node.ID, models.LogLevelInfo, "[dbt] %s", line)
		}
	}

	if err != nil {
		return nil, fmt.Errorf("dbt %s failed (%.1fs): %s\n%s", command, duration.Seconds(), err, outputStr)
	}

	r.log(node.ID, models.LogLevelInfo, "dbt %s completed in %.1fs", command, duration.Seconds())

	// Parse results into a dataset
	ds := parseDbtResults(command, outputStr)
	return ds, nil
}

// parseDbtResults converts dbt JSON output into a DataSet for downstream nodes.
func parseDbtResults(command, output string) *common.DataSet {
	// Try to parse dbt JSON output
	var results []map[string]interface{}
	if err := json.Unmarshal([]byte(output), &results); err == nil && len(results) > 0 {
		return common.ConvertToDataSet(results)
	}

	// Fallback: parse dbt run_results.json if available
	resultsPath := filepath.Join("target", "run_results.json")
	if data, err := os.ReadFile(resultsPath); err == nil {
		var runResults struct {
			Results []struct {
				UniqueID      string  `json:"unique_id"`
				Status        string  `json:"status"`
				ExecutionTime float64 `json:"execution_time"`
				Message       string  `json:"message"`
			} `json:"results"`
		}
		if json.Unmarshal(data, &runResults) == nil {
			rows := make([]map[string]interface{}, 0, len(runResults.Results))
			for _, res := range runResults.Results {
				rows = append(rows, map[string]interface{}{
					"model":          res.UniqueID,
					"status":         res.Status,
					"execution_time": res.ExecutionTime,
					"message":        res.Message,
				})
			}
			return common.ConvertToDataSet(rows)
		}
	}

	// Final fallback: single row with command output
	return &common.DataSet{
		Columns: []string{"command", "output"},
		Rows:    []common.DataRow{{"command": command, "output": strings.TrimSpace(output)}},
	}
}
