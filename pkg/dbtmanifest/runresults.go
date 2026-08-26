package dbtmanifest

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Reading dbt's run_results.json, which is what makes model-level
// observability possible without model-level dispatch.
//
// Measured on a 100-model project (#353): one `dbt build` takes 21.8s,
// while invoking dbt once per model takes 6.64s each -- 30x slower, because
// each invocation pays interpreter and project-parse startup again. Worse,
// the sum of per-model execution times in the single build is 30.2s against
// 21.8s of wall clock, so dbt is already overlapping work that per-model
// dispatch would serialise.
//
// The reason that does not force a choice between speed and granularity:
// one invocation reports every node individually. A single run_results.json
// carries a result per model, each with its own status, duration, message
// and failure count. So Brokoli invokes dbt per selected subset and derives
// per-model node runs from this file, rather than from process boundaries.
//
// Like the manifest, this is a format dbt owns and versions separately --
// run-results v6 alongside manifest v12 -- and it moves underneath its
// version the same way: dbt 1.10.11 adds `batch_results` to each result
// where 1.8.9 has none. The same rules apply: decode only what is needed,
// refuse an unrecognised schema version by name, treat optional fields as
// optional.

// SupportedRunResultsVersions are the run-results schema versions this
// reader is tested against, by the same rule as SupportedSchemaVersions:
// widening it means adding a fixture from that version.
var SupportedRunResultsVersions = []int{6}

// NodeStatus is the outcome dbt reported for one node.
//
// dbt uses a different vocabulary per resource kind -- a model is "success"
// or "error", a test is "pass" or "fail" -- and both can be "skipped". They
// are kept as dbt wrote them rather than collapsed, because a caller
// deciding what to show a user wants to know a test failed rather than that
// something generically did not succeed. Succeeded and Failed below do the
// collapsing where it is wanted.
type NodeStatus string

const (
	StatusSuccess NodeStatus = "success"
	StatusError   NodeStatus = "error"
	StatusPass    NodeStatus = "pass"
	StatusFail    NodeStatus = "fail"
	StatusWarn    NodeStatus = "warn"
	StatusSkipped NodeStatus = "skipped"
	StatusRuntime NodeStatus = "runtime error"
)

// Succeeded reports whether this outcome means the node did its job.
func (s NodeStatus) Succeeded() bool {
	return s == StatusSuccess || s == StatusPass
}

// Failed reports whether this outcome should fail the node that carries it.
// A warn is deliberately not a failure: dbt's own severity model treats it
// as something to surface, not something to stop for.
func (s NodeStatus) Failed() bool {
	return s == StatusError || s == StatusFail || s == StatusRuntime
}

// NodeResult is what one node did in one dbt invocation.
type NodeResult struct {
	// UniqueID matches the manifest's, so a result joins to the node it
	// came from without any name matching.
	UniqueID string
	Status   NodeStatus
	// ExecutionTime is what dbt measured for this node alone, which is
	// what a per-model duration should report -- not a share of the
	// invocation's wall clock, since dbt runs nodes concurrently.
	ExecutionTime time.Duration
	// Message is dbt's own explanation, populated on failure and
	// sometimes on success ("CREATE VIEW", row counts).
	Message string
	// Failures is the count a test reported. Nil when dbt did not report
	// one, which is the case for models -- distinct from zero, which for
	// a test means it passed with nothing failing.
	Failures *int
	// RelationName is the relation dbt built, when it built one. This is
	// what makes a completed model addressable as an ADR-023 TableRef.
	RelationName string
	// ThreadID is the dbt worker that ran it, kept because it is the only
	// evidence in the file of dbt's own concurrency.
	ThreadID string
}

// RunResults is one dbt invocation's outcome.
type RunResults struct {
	SchemaVersion int
	DBTVersion    string
	// InvocationID is dbt's own id for the run, worth carrying into logs
	// so a Brokoli run can be matched against dbt's artifacts afterwards.
	InvocationID string
	GeneratedAt  time.Time
	// Elapsed is the invocation's wall clock, which is not the sum of the
	// node times: dbt overlaps them across threads.
	Elapsed time.Duration

	Results []NodeResult
}

// ByUniqueID indexes the results for joining against a manifest.
func (r *RunResults) ByUniqueID() map[string]NodeResult {
	out := make(map[string]NodeResult, len(r.Results))
	for _, n := range r.Results {
		out[n.UniqueID] = n
	}
	return out
}

// Failed returns the results that should fail their node, in a stable
// order so an error message listing them does not vary between runs.
func (r *RunResults) Failed() []NodeResult {
	var out []NodeResult
	for _, n := range r.Results {
		if n.Status.Failed() {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UniqueID < out[j].UniqueID })
	return out
}

type rawRunResults struct {
	Metadata struct {
		SchemaVersion string `json:"dbt_schema_version"`
		DBTVersion    string `json:"dbt_version"`
		InvocationID  string `json:"invocation_id"`
		GeneratedAt   string `json:"generated_at"`
	} `json:"metadata"`
	ElapsedTime float64         `json:"elapsed_time"`
	Results     []rawNodeResult `json:"results"`
}

type rawNodeResult struct {
	UniqueID      string  `json:"unique_id"`
	Status        string  `json:"status"`
	ExecutionTime float64 `json:"execution_time"`
	Message       *string `json:"message"`
	// A pointer, because zero is a real answer for a test and "dbt did not
	// say" is a different one.
	Failures     *int   `json:"failures"`
	RelationName string `json:"relation_name"`
	ThreadID     string `json:"thread_id"`
}

// ParseRunResults reads one invocation's results.
func ParseRunResults(r io.Reader) (*RunResults, error) {
	var raw rawRunResults
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode dbt run results: %w", err)
	}

	version, err := schemaVersion(raw.Metadata.SchemaVersion)
	if err != nil {
		return nil, err
	}
	supportedRR := false
	for _, v := range SupportedRunResultsVersions {
		if v == version {
			supportedRR = true
			break
		}
	}
	if !supportedRR {
		return nil, fmt.Errorf(
			"dbt run-results schema v%d is not supported (written by dbt %s); this build reads %v -- "+
				"the format is dbt's own and changes between releases, so it is refused rather than "+
				"read into a shape that may have moved",
			version, orUnknown(raw.Metadata.DBTVersion), SupportedRunResultsVersions)
	}

	out := &RunResults{
		SchemaVersion: version,
		DBTVersion:    raw.Metadata.DBTVersion,
		InvocationID:  raw.Metadata.InvocationID,
		Elapsed:       durationFromSeconds(raw.ElapsedTime),
		Results:       make([]NodeResult, 0, len(raw.Results)),
	}
	if raw.Metadata.GeneratedAt != "" {
		// dbt writes RFC3339 with a Z; a parse failure here is not worth
		// failing the whole read over, since nothing depends on the
		// timestamp being present.
		if t, err := time.Parse(time.RFC3339, raw.Metadata.GeneratedAt); err == nil {
			out.GeneratedAt = t
		}
	}
	for _, rn := range raw.Results {
		n := NodeResult{
			UniqueID:      rn.UniqueID,
			Status:        NodeStatus(rn.Status),
			ExecutionTime: durationFromSeconds(rn.ExecutionTime),
			Failures:      rn.Failures,
			RelationName:  rn.RelationName,
			ThreadID:      rn.ThreadID,
		}
		if rn.Message != nil {
			n.Message = *rn.Message
		}
		out.Results = append(out.Results, n)
	}
	return out, nil
}

// ParseRunResultsFile reads results from disk.
func ParseRunResultsFile(path string) (*RunResults, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open dbt run results: %w", err)
	}
	defer f.Close()
	rr, err := ParseRunResults(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return rr, nil
}

// ParseRunResultsForProject reads results from a project's target
// directory. Resolved against the project, never the process working
// directory -- reading it relative to the engine's cwd is the defect #348
// shipped with.
func ParseRunResultsForProject(projectDir string) (*RunResults, error) {
	return ParseRunResultsFile(filepath.Join(projectDir, "target", "run_results.json"))
}

func durationFromSeconds(s float64) time.Duration {
	if s <= 0 {
		return 0
	}
	return time.Duration(s * float64(time.Second))
}
