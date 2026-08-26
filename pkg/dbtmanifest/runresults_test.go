package dbtmanifest

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Real run_results.json from real dbt, two versions, from a project that
// deliberately contains a failing model so the failure path has genuine
// dbt output behind it rather than something hand-written.
//
// The same drift as the manifest fixtures: both write run-results v6, and
// 1.10.11 adds `batch_results` to every result where 1.8.9 has none.
var runResultFixtures = map[string]string{
	"dbt-1.8.9":   filepath.Join("testdata", "run-results-v6-dbt-1.8.9.json"),
	"dbt-1.10.11": filepath.Join("testdata", "run-results-v6-dbt-1.10.11.json"),
}

// The claim this whole design rests on: ONE invocation reports every node
// individually. If that were not true, model-level granularity would
// require model-level dispatch, which #353 measured at 30x slower.
func TestOneInvocationReportsEveryNode(t *testing.T) {
	for version, path := range runResultFixtures {
		rr, err := ParseRunResultsFile(path)
		if err != nil {
			t.Fatalf("%s: %v", version, err)
		}
		if rr.SchemaVersion != 6 {
			t.Errorf("%s: schema v%d, want v6", version, rr.SchemaVersion)
		}
		if len(rr.Results) < 5 {
			t.Fatalf("%s: %d results, expected the whole project", version, len(rr.Results))
		}

		// Each result must carry what a per-model node run needs.
		for _, n := range rr.Results {
			if n.UniqueID == "" {
				t.Errorf("%s: a result has no unique id, so it cannot be joined to a model", version)
			}
			if n.Status == "" {
				t.Errorf("%s: %s has no status", version, n.UniqueID)
			}
			if n.Status.Succeeded() && n.ExecutionTime <= 0 {
				t.Errorf("%s: %s succeeded with no duration", version, n.UniqueID)
			}
		}

		// dbt's own id for the invocation, so a Brokoli run can be matched
		// against dbt's artifacts after the fact.
		if rr.InvocationID == "" {
			t.Errorf("%s: no invocation id", version)
		}
		if rr.Elapsed <= 0 {
			t.Errorf("%s: no elapsed time", version)
		}
	}
}

// A failing model must be identifiable and must carry dbt's own
// explanation. An exit code cannot say which model failed; this can.
func TestFailureIsNamedWithDBTsOwnMessage(t *testing.T) {
	for version, path := range runResultFixtures {
		rr, err := ParseRunResultsFile(path)
		if err != nil {
			t.Fatal(err)
		}
		// The fixture project contains two models that fail at execution.
		// Two is more useful than one here: an exit code cannot tell you
		// that BOTH failed, or which, and that is the whole point.
		failed := rr.Failed()
		if len(failed) != 2 {
			t.Fatalf("%s: %d failures, want the two the fixture project contains", version, len(failed))
		}
		gotNames := make([]string, 0, len(failed))
		for _, f := range failed {
			gotNames = append(gotNames, f.UniqueID)
			if !f.Status.Failed() {
				t.Errorf("%s: %s has status %q, which does not read as a failure", version, f.UniqueID, f.Status)
			}
			if f.Message == "" {
				t.Errorf("%s: %s carries no message, which is all an exit code would have given us",
					version, f.UniqueID)
			}
		}
		// Failed() sorts, so the order is stable rather than dependent on
		// whichever thread finished first.
		if !strings.HasSuffix(gotNames[0], "always_fails") || !strings.HasSuffix(gotNames[1], "will_fail") {
			t.Errorf("%s: failures = %v, want always_fails then will_fail in sorted order", version, gotNames)
		}
		// Everything else ran. That is the property an orchestrator cannot
		// currently see: one model failing does not mean the run told you
		// nothing about the other seven.
		succeeded := 0
		for _, n := range rr.Results {
			if n.Status.Succeeded() {
				succeeded++
			}
		}
		if succeeded < 3 {
			t.Errorf("%s: only %d nodes succeeded alongside the failure", version, succeeded)
		}
	}
}

// Tests and models use different vocabularies, and collapsing them would
// lose the distinction between "a model errored" and "a test failed".
func TestStatusVocabularyIsKeptAsDBTWroteIt(t *testing.T) {
	rr, err := ParseRunResultsFile(runResultFixtures["dbt-1.8.9"])
	if err != nil {
		t.Fatal(err)
	}
	var sawPass, sawSuccess bool
	for _, n := range rr.Results {
		switch n.Status {
		case StatusPass:
			sawPass = true
			// A test that passed reports zero failures, which is not the
			// same as a model reporting none at all.
			if n.Failures == nil {
				t.Errorf("%s passed but reports no failure count", n.UniqueID)
			} else if *n.Failures != 0 {
				t.Errorf("%s passed with %d failures", n.UniqueID, *n.Failures)
			}
		case StatusSuccess:
			sawSuccess = true
			if n.Failures != nil {
				t.Errorf("%s is a model and should not report a failure count, got %d", n.UniqueID, *n.Failures)
			}
		}
	}
	if !sawPass || !sawSuccess {
		t.Fatalf("fixture should contain both a passing test and a succeeding model (pass=%v success=%v)", sawPass, sawSuccess)
	}

	// The collapsing helpers, stated as a table so a change is visible.
	for status, wantSucceeded := range map[NodeStatus]bool{
		StatusSuccess: true, StatusPass: true,
		StatusError: false, StatusFail: false, StatusSkipped: false,
		StatusWarn: false, StatusRuntime: false,
	} {
		if got := status.Succeeded(); got != wantSucceeded {
			t.Errorf("%q.Succeeded() = %v, want %v", status, got, wantSucceeded)
		}
	}
	for status, wantFailed := range map[NodeStatus]bool{
		StatusError: true, StatusFail: true, StatusRuntime: true,
		// A warn is deliberately not a failure: dbt's severity model
		// treats it as something to surface, not to stop for.
		StatusWarn: false, StatusSkipped: false,
		StatusSuccess: false, StatusPass: false,
	} {
		if got := status.Failed(); got != wantFailed {
			t.Errorf("%q.Failed() = %v, want %v", status, got, wantFailed)
		}
	}
}

// A result joins to its manifest node by unique id, with no name matching.
// That join is what turns an invocation into per-model pipeline nodes.
func TestResultsJoinToManifestNodes(t *testing.T) {
	p, err := ParseFile(fixtures["dbt-1.8.9"])
	if err != nil {
		t.Fatal(err)
	}
	rr, err := ParseRunResultsFile(runResultFixtures["dbt-1.8.9"])
	if err != nil {
		t.Fatal(err)
	}

	byID := rr.ByUniqueID()
	matched := 0
	for _, m := range p.Models() {
		if res, ok := byID[m.UniqueID]; ok {
			matched++
			if res.UniqueID != m.UniqueID {
				t.Errorf("join mismatch: %s vs %s", res.UniqueID, m.UniqueID)
			}
		}
	}
	if matched != len(p.Models()) {
		t.Errorf("joined %d of %d models; every model dbt ran should have a result",
			matched, len(p.Models()))
	}

	// A built model has to be addressable for ADR-023 composition. dbt
	// reports the relation it built, so the result carries it.
	for _, res := range rr.Results {
		if res.Status.Succeeded() && strings.Contains(res.UniqueID, "model.") {
			if res.RelationName == "" {
				t.Errorf("%s succeeded but reports no relation, so it could not become a TableRef", res.UniqueID)
			}
		}
	}
}

// Elapsed wall clock is not the sum of node times, because dbt overlaps
// them. A caller reporting per-model durations must use the node's own,
// and this pins that they are genuinely different numbers.
func TestNodeDurationsAreNotTheInvocationWallClock(t *testing.T) {
	rr, err := ParseRunResultsFile(runResultFixtures["dbt-1.8.9"])
	if err != nil {
		t.Fatal(err)
	}
	var sum time.Duration
	for _, n := range rr.Results {
		sum += n.ExecutionTime
	}
	if sum == 0 {
		t.Fatal("no node durations at all")
	}
	if rr.Elapsed == sum {
		t.Error("elapsed equals the sum of node times exactly, which would mean dbt ran nothing concurrently — " +
			"if that is now true the concurrency assumption in #353 needs revisiting")
	}
}

// The refusal that keeps this honest about reading someone else's format.
// run-results is versioned separately from the manifest, so it has its own.
func TestRefusesUnknownRunResultsVersion(t *testing.T) {
	const future = `{"metadata":{"dbt_schema_version":"https://schemas.getdbt.com/dbt/run-results/v99.json",
		"dbt_version":"2.5.0"},"results":[]}`
	_, err := ParseRunResults(strings.NewReader(future))
	if err == nil {
		t.Fatal("an unsupported run-results version must be refused")
	}
	for _, want := range []string{"v99", "2.5.0", "run-results"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal should mention %q, got: %v", want, err)
		}
	}
}

// Forward compatibility: 1.10.11 already adds batch_results, and the next
// version will add something else.
func TestRunResultsIgnoresUnknownFields(t *testing.T) {
	const withExtras = `{
		"metadata":{"dbt_schema_version":"https://schemas.getdbt.com/dbt/run-results/v6.json",
			"dbt_version":"1.99.0","invocation_id":"abc","generated_at":"2026-08-26T00:00:00Z"},
		"elapsed_time": 1.5,
		"results":[{"unique_id":"model.p.a","status":"success","execution_time":0.25,
			"message":"CREATE VIEW","relation_name":"\"db\".\"s\".\"a\"","thread_id":"Thread-1",
			"batch_results":null,"a_field_from_2030":42}],
		"a_new_top_level":{}
	}`
	rr, err := ParseRunResults(strings.NewReader(withExtras))
	if err != nil {
		t.Fatalf("unknown fields must be ignored, not fatal: %v", err)
	}
	if len(rr.Results) != 1 {
		t.Fatalf("results = %d", len(rr.Results))
	}
	n := rr.Results[0]
	if n.ExecutionTime != 250*time.Millisecond {
		t.Errorf("execution time = %s, want 250ms", n.ExecutionTime)
	}
	if rr.Elapsed != 1500*time.Millisecond {
		t.Errorf("elapsed = %s, want 1.5s", rr.Elapsed)
	}
	if rr.InvocationID != "abc" {
		t.Errorf("invocation id = %q", rr.InvocationID)
	}
}

// Reading results is on the path of every dbt-backed run, immediately after
// an invocation that costs seconds. Worth knowing it is not a second cost.
func BenchmarkParseRunResults(b *testing.B) {
	for version, path := range runResultFixtures {
		b.Run(version, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				rr, err := ParseRunResultsFile(path)
				if err != nil {
					b.Fatal(err)
				}
				if len(rr.Results) == 0 {
					b.Fatal("no results")
				}
			}
		})
	}
}
