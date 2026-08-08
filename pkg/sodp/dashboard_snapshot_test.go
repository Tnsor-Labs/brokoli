package sodp

import (
	"testing"
	"time"
)

// dashboardSnapshot pulls the computed dashboard.{org} value back out of the
// state store so assertions can read it as the UI would receive it.
func dashboardSnapshot(t *testing.T, srv *Server, orgID string) map[string]any {
	t.Helper()
	v, version := srv.State.Get(dashboardKey(orgID))
	if version == 0 {
		t.Fatalf("no dashboard snapshot for org %q", orgID)
	}
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("dashboard snapshot is %T, want map[string]any", v)
	}
	return m
}

// Tnsor-Labs/brokoli#75. A run's pipeline name must be captured into run
// state when the run starts, so the dashboard can label it without looking
// the pipeline up. That lookup is impossible once a pipeline is deleted —
// the row is gone while its in-memory run state lives on — which is exactly
// when the UI used to fall back to rendering a bare UUID.
func TestRecomputeDashboard_RecentRunsCarryPipelineName(t *testing.T) {
	srv := NewServer()
	now := time.Now().UTC()

	bridgeEvent(srv, BridgeEvent{
		Type:         "run.started",
		RunID:        "run-1",
		PipelineID:   "pipe-1",
		PipelineName: "daily-sales-etl",
		Timestamp:    now,
	})
	recomputeDashboard(srv, "", now)

	snap := dashboardSnapshot(t, srv, "")
	recent, _ := snap["recent_runs"].([]map[string]any)
	if len(recent) != 1 {
		t.Fatalf("recent_runs = %d entries, want 1", len(recent))
	}
	if got := recent[0]["pipeline_name"]; got != "daily-sales-etl" {
		t.Errorf("pipeline_name = %v, want daily-sales-etl — the UI has nothing else to label the row with", got)
	}
	if got := recent[0]["pipeline_id"]; got != "pipe-1" {
		t.Errorf("pipeline_id = %v, want pipe-1", got)
	}
}

// The name is written once at run.started and must survive the merge that
// run.failed performs, so a failed run is still labelled.
func TestRecomputeDashboard_PipelineNameSurvivesFailure(t *testing.T) {
	srv := NewServer()
	now := time.Now().UTC()

	bridgeEvent(srv, BridgeEvent{
		Type: "run.started", RunID: "run-1", PipelineID: "pipe-1",
		PipelineName: "daily-sales-etl", Timestamp: now,
	})
	bridgeEvent(srv, BridgeEvent{
		Type: "run.failed", RunID: "run-1", PipelineID: "pipe-1",
		Error: "boom", Timestamp: now.Add(time.Second),
	})
	recomputeDashboard(srv, "", now)

	snap := dashboardSnapshot(t, srv, "")
	recent, _ := snap["recent_runs"].([]map[string]any)
	if len(recent) != 1 {
		t.Fatalf("recent_runs = %d entries, want 1", len(recent))
	}
	if got := recent[0]["pipeline_name"]; got != "daily-sales-etl" {
		t.Errorf("pipeline_name = %v after failure, want it preserved through the merge", got)
	}
	if got := recent[0]["status"]; got != "failed" {
		t.Errorf("status = %v, want failed", got)
	}
	// The error is surfaced too, so the dashboard can show *why* without a
	// second round trip per row.
	if got := recent[0]["error"]; got != "boom" {
		t.Errorf("error = %v, want boom", got)
	}
}

// top_failing previously carried only an ID and a count, so the one panel
// whose entire job is naming problem pipelines could not name them.
func TestRecomputeDashboard_TopFailingCarriesName(t *testing.T) {
	srv := NewServer()
	now := time.Now().UTC()

	for _, id := range []string{"run-1", "run-2"} {
		bridgeEvent(srv, BridgeEvent{
			Type: "run.started", RunID: id, PipelineID: "pipe-1",
			PipelineName: "flaky-sync", Timestamp: now,
		})
		bridgeEvent(srv, BridgeEvent{
			Type: "run.failed", RunID: id, PipelineID: "pipe-1",
			Error: "nope", Timestamp: now,
		})
	}
	recomputeDashboard(srv, "", now)

	snap := dashboardSnapshot(t, srv, "")
	top, _ := snap["top_failing"].([]map[string]any)
	if len(top) != 1 {
		t.Fatalf("top_failing = %d entries, want 1", len(top))
	}
	if got := top[0]["name"]; got != "flaky-sync" {
		t.Errorf("name = %v, want flaky-sync", got)
	}
	if got := top[0]["fail_count"]; got != 2 {
		t.Errorf("fail_count = %v, want 2", got)
	}
}
