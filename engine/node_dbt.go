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
	"github.com/Tnsor-Labs/brokoli/pkg/dbtmanifest"
)

// dbtCommands are the subcommands this node will invoke, and the ones the UI
// offers. Anything else is refused by name rather than passed through: the
// argument list below is assembled for these, and a command with different
// flags would be handed options it does not accept -- which is exactly the
// defect this file was rewritten to fix (#348).
var dbtCommands = map[string]bool{
	"run": true, "test": true, "build": true, "seed": true,
	"snapshot": true, "compile": true, "ls": true,
}

// runDBT executes a dbt command against an existing dbt project and returns
// its per-model results.
//
// This shells out to dbt-core, which must be installed on the worker. ADR-025
// proposes replacing this with a model-level integration that reads dbt's
// manifest; until that exists, the contract here is narrow: run the command,
// report what dbt reported, and fail loudly when dbt fails.
func (r *Runner) runDBT(node models.Node) (nodeExecutionResult, error) {
	command, _ := node.Config["command"].(string)
	projectDir, _ := node.Config["project_dir"].(string)
	profiles, _ := node.Config["profiles_dir"].(string)
	target, _ := node.Config["target"].(string)
	selectModels, _ := node.Config["select"].(string)
	varsJSON, _ := node.Config["vars"].(string)

	if command == "" {
		command = "run"
	}
	if !dbtCommands[command] {
		return nodeExecutionResult{}, fmt.Errorf("dbt: unsupported command %q (allowed: run, test, build, seed, snapshot, compile, ls)", command)
	}
	if projectDir == "" {
		projectDir = "."
	}

	// #353 Phase 2: a conn_id generates the profile from a Brokoli
	// connection, so a dbt project needs no second copy of the warehouse
	// password. An explicit profiles_dir stays authoritative -- a project
	// with a profile it already maintains should keep using it, and
	// silently overriding that would be the wrong kind of helpful.
	if connID, _ := node.Config["conn_id"].(string); connID != "" && profiles == "" {
		generated, err := r.generateDBTProfileForNode(node, connID)
		if err != nil {
			return nodeExecutionResult{}, fmt.Errorf("dbt: %w", err)
		}
		// Removed when this node finishes, including on failure: the file
		// holds a resolved credential.
		defer generated.Cleanup()
		profiles = generated.Dir
		r.log(node.ID, models.LogLevelInfo,
			"using a generated dbt profile for connection %q; the project's own profiles.yml is not consulted", connID)
	}

	args := []string{command, "--project-dir", projectDir}
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
	// --output json is a flag on `dbt ls` alone. Appending it to every
	// command -- which this node did until #348 -- makes dbt exit at its
	// option parser before reading the project, so run/test/build/seed
	// could never execute. The machine-readable flag those commands do
	// have is --log-format json, and the authoritative per-model result is
	// run_results.json in the project's target directory.
	if command == "ls" {
		args = append(args, "--output", "json")
	}
	args = append(args, "--log-format", "json", "--no-use-colors")

	r.log(node.ID, models.LogLevelInfo, "Running: dbt %s", strings.Join(args, " "))

	// The attempt's context, so the node's configured timeout and a
	// cancelled run both stop dbt where it is. Without it a hung invocation
	// outlived both, which no other external call in the engine allows.
	cmd := exec.CommandContext(r.ctx, "dbt", args...)
	cmd.Dir = projectDir
	// Only override DBT_PROFILES_DIR when one was configured. Setting it
	// unconditionally exported an empty value and overrode dbt's own
	// ~/.dbt default, so a project relying on that default could not
	// resolve its profile.
	if profiles != "" {
		cmd.Env = append(os.Environ(), "DBT_PROFILES_DIR="+profiles)
	}

	start := time.Now()
	output, err := cmd.CombinedOutput()
	duration := time.Since(start)
	outputStr := string(output)

	for _, line := range strings.Split(outputStr, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			r.log(node.ID, models.LogLevelInfo, "[dbt] %s", dbtLogLine(line))
		}
	}

	// run_results.json is written even when the command fails, and it names
	// which models failed -- strictly better than an exit code, so it is
	// read on both paths.
	results, resultsErr := readDbtRunResults(projectDir)

	// #353: one invocation, an outcome per model. The manifest supplies the
	// dependency graph, which is what lets a skip say WHICH failure caused
	// it -- dbt records the status and nothing linking it to the cause.
	// Best-effort: a project whose artifacts cannot be read still reports
	// exactly what it reported before.
	if summary, ok := r.recordDBTModelOutcomesFromProject(node, projectDir); ok {
		r.log(node.ID, models.LogLevelInfo,
			"dbt %s: %d succeeded, %d failed, %d skipped, %d warned",
			command, summary.Succeeded, summary.Failed, summary.Skipped, summary.Warned)
	}

	if err != nil {
		// #353 Phase 2: "dbt failed" is not one thing. A failing test means
		// the models built and the data is wrong; a compile error means
		// nothing ran at all. Those call for different actions, and the
		// artifacts already distinguish them.
		var parsed *dbtmanifest.RunResults
		if p, perr := dbtmanifest.ParseRunResultsForProject(projectDir); perr == nil {
			parsed = p
		}
		failure := classifyDBTFailure(command, err, contextErrOf(r.ctx), parsed)
		r.log(node.ID, models.LogLevelError, "dbt %s failed after %.1fs (%s)",
			command, duration.Seconds(), failure.Kind)
		return nodeExecutionResult{}, failure
	}

	r.log(node.ID, models.LogLevelInfo, "dbt %s completed in %.1fs", command, duration.Seconds())

	// #353 Phase 3: a model dbt just built is a named relation on a known
	// server, which is what an ADR-023 reference is. Handing one downstream
	// lets a sink on the same connection compose through the pushdown
	// compiler that already exists, without the engine learning what dbt
	// is. Only when the node asks for it by naming an output_model.
	if project, presults, ok := r.dbtArtifacts(projectDir); ok {
		ref, refErr := r.dbtOutputTableRef(node, projectDir, project, presults)
		if refErr != nil {
			return nodeExecutionResult{}, fmt.Errorf("dbt: %w", refErr)
		}
		if ref != nil {
			r.log(node.ID, models.LogLevelInfo,
				"handing %s downstream as a reference; its rows stay in the database",
				strings.TrimPrefix(ref.Query, "SELECT * FROM "))
			return nodeExecutionResult{outputTable: ref}, nil
		}
	} else if wanted, _ := node.Config["output_model"].(string); wanted != "" {
		return nodeExecutionResult{}, fmt.Errorf(
			"dbt: output_model %q was requested but dbt's artifacts could not be read", wanted)
	}

	if resultsErr == nil && len(results.Results) > 0 {
		return nodeExecutionResult{output: dbtResultsDataSet(results)}, nil
	}
	// ls and compile write no run_results.json; neither does a command that
	// selected nothing. The output stays available rather than being
	// discarded.
	return nodeExecutionResult{output: &common.DataSet{
		Columns: []string{"command", "output"},
		Rows:    []common.DataRow{{"command": command, "output": strings.TrimSpace(outputStr)}},
	}}, nil
}

// dbtArtifacts reads a project's manifest and results together, since every
// caller needs both or neither.
func (r *Runner) dbtArtifacts(projectDir string) (*dbtmanifest.Project, *dbtmanifest.RunResults, bool) {
	project, err := dbtmanifest.ParseProject(projectDir)
	if err != nil {
		return nil, nil, false
	}
	results, err := dbtmanifest.ParseRunResultsForProject(projectDir)
	if err != nil {
		return nil, nil, false
	}
	return project, results, true
}

// dbtRunResults is the subset of dbt's run_results.json this node reads. The
// file is a documented dbt artifact with its own schema version; only fields
// stable across the 1.x line are taken, and an unknown shape degrades to the
// raw output rather than failing the node.
type dbtRunResults struct {
	Results []struct {
		UniqueID      string  `json:"unique_id"`
		Status        string  `json:"status"`
		ExecutionTime float64 `json:"execution_time"`
		Message       string  `json:"message"`
		FailuresRaw   *int    `json:"failures"`
	} `json:"results"`
}

// readDbtRunResults reads the project's run_results.json.
//
// The path is resolved against the project directory. Reading it relative to
// the engine's working directory -- as this did until #348 -- resolved to the
// wrong path for every project that is not the process cwd, which is every
// real deployment.
func readDbtRunResults(projectDir string) (dbtRunResults, error) {
	var out dbtRunResults
	data, err := os.ReadFile(filepath.Join(projectDir, "target", "run_results.json"))
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, err
	}
	return out, nil
}

func dbtResultsDataSet(res dbtRunResults) *common.DataSet {
	ds := &common.DataSet{Columns: []string{"model", "status", "execution_time", "message", "failures"}}
	for _, r := range res.Results {
		failures := interface{}(nil)
		if r.FailuresRaw != nil {
			failures = *r.FailuresRaw
		}
		ds.Rows = append(ds.Rows, common.DataRow{
			"model":          r.UniqueID,
			"status":         r.Status,
			"execution_time": r.ExecutionTime,
			"message":        r.Message,
			"failures":       failures,
		})
	}
	return ds
}

// dbtLogLine renders one line of dbt's JSON log stream for the run log. With
// --log-format json each line is an object whose "info" carries the human
// message; anything that does not parse is passed through unchanged, so a
// plain-text line or a stack trace still reaches the author.
func dbtLogLine(line string) string {
	if !strings.HasPrefix(line, "{") {
		return line
	}
	var entry struct {
		Info struct {
			Msg   string `json:"msg"`
			Level string `json:"level"`
		} `json:"info"`
	}
	if err := json.Unmarshal([]byte(line), &entry); err != nil || entry.Info.Msg == "" {
		return line
	}
	return entry.Info.Msg
}
