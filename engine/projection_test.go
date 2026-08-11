package engine

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// TestProjectionMatchesReality is the core "replay reproduces state" proof
// required by issue #6's acceptance criteria: it runs a real pipeline end to
// end, capturing both the real runs/node_runs rows the engine wrote via
// CreateRun/UpdateRun/CreateNodeRun/UpdateNodeRun AND the run_events appended
// alongside them, then asserts that folding the event stream through
// ProjectRun reproduces the real Run exactly.
func TestProjectionMatchesReality(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "projection.db")
	inputPath := filepath.Join(dir, "input.csv")
	outputPath := filepath.Join(dir, "output.csv")
	if err := os.WriteFile(inputPath, []byte("id,name\n1,brokoli\n2,sql\n"), 0o600); err != nil {
		t.Fatalf("write input: %v", err)
	}

	s, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	pipeline := &models.Pipeline{
		ID: "projection-pipeline", Name: "Projection Pipeline", Enabled: true,
		CreatedAt: now, UpdatedAt: now,
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceFile, Name: "Source", Config: map[string]interface{}{"path": inputPath, "format": "csv"}},
			{ID: "sink", Type: models.NodeTypeSinkFile, Name: "Sink", Config: map[string]interface{}{"path": outputPath, "format": "csv"}},
		},
		Edges: []models.Edge{{From: "source", To: "sink"}},
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	// Drain the engine's background event goroutines before t.TempDir()
	// cleanup, so a late event write doesn't recreate the SQLite WAL files
	// mid-RemoveAll ("directory not empty" flake).
	eng := NewEngine(s)
	defer eng.Close(context.Background())
	run, err := eng.RunPipeline(pipeline.ID)
	if err != nil {
		t.Fatalf("run pipeline: %v", err)
	}
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %q, want success", run.Status)
	}

	// The real state, as read the same way the rest of the app does today.
	real, err := s.GetRun(run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}

	// The event log captured alongside those same writes.
	events, err := s.ListEventsByRun(run.ID)
	if err != nil {
		t.Fatalf("ListEventsByRun: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("expected run_events to be populated by the run")
	}

	projected := ProjectRun(run.ID, events)

	assertRunsEqual(t, real, projected)
}

// TestProjectRun_MultipleAttempts is a pure, deterministic unit test for the
// retry path: a node that fails on attempt 0, has a retry scheduled, then
// succeeds on attempt 1. This exercises the same event sequence
// engine/runner.go's retry loop produces (AttemptStarted -> AttemptFailed ->
// RetryScheduled -> AttemptStarted -> AttemptCompleted) without depending on
// a specific node executor's failure semantics.
func TestProjectRun_MultipleAttempts(t *testing.T) {
	runID := "run-retry"
	startedAt := time.Now().UTC().Truncate(time.Millisecond)
	attempt0, attempt1 := 0, 1

	events := []models.RunEvent{
		{
			RunID: runID, EventType: models.RunEventCreated,
			Payload: models.RunEventPayload{Status: models.RunStatusRunning, PipelineID: "pipe-retry", StartedAt: &startedAt},
		},
		{
			RunID: runID, NodeID: "flaky", Attempt: &attempt0, EventType: models.AttemptStarted,
			Payload: models.RunEventPayload{Status: models.RunStatusRunning, NodeRunID: "nr-attempt0", StartedAt: &startedAt},
		},
		{
			RunID: runID, NodeID: "flaky", Attempt: &attempt0, EventType: models.AttemptFailed,
			Payload: models.RunEventPayload{Status: models.RunStatusFailed, NodeRunID: "nr-attempt0", DurationMs: 5, Error: "transient failure"},
		},
		{
			RunID: runID, NodeID: "flaky", Attempt: &attempt1, EventType: models.RetryScheduled,
			Payload: models.RunEventPayload{Error: "transient failure", BackoffMs: 1000},
		},
		{
			RunID: runID, NodeID: "flaky", Attempt: &attempt1, EventType: models.AttemptStarted,
			Payload: models.RunEventPayload{Status: models.RunStatusRunning, NodeRunID: "nr-attempt1", StartedAt: &startedAt},
		},
		{
			RunID: runID, NodeID: "flaky", Attempt: &attempt1, EventType: models.AttemptCompleted,
			Payload: models.RunEventPayload{Status: models.RunStatusSuccess, NodeRunID: "nr-attempt1", RowCount: 2, DurationMs: 8},
		},
		{
			RunID: runID, EventType: models.RunEventTerminal,
			Payload: models.RunEventPayload{Status: models.RunStatusSuccess, FinishedAt: &startedAt},
		},
	}

	projected := ProjectRun(runID, events)

	if projected.Status != models.RunStatusSuccess {
		t.Errorf("Status = %q, want success", projected.Status)
	}
	if len(projected.NodeRuns) != 2 {
		t.Fatalf("NodeRuns = %d, want 2 (one per attempt)", len(projected.NodeRuns))
	}
	sortNodeRuns(projected.NodeRuns)

	first, second := projected.NodeRuns[0], projected.NodeRuns[1]
	if first.ID != "nr-attempt0" || first.Attempt != 0 || first.Status != models.RunStatusFailed || first.Error != "transient failure" {
		t.Errorf("attempt 0 node run = %+v, want failed nr-attempt0 with error", first)
	}
	if second.ID != "nr-attempt1" || second.Attempt != 1 || second.Status != models.RunStatusSuccess || second.RowCount != 2 {
		t.Errorf("attempt 1 node run = %+v, want successful nr-attempt1 with 2 rows", second)
	}
}

// assertRunsEqual compares a real store.Store-backed Run against a
// ProjectRun-reconstructed Run field by field. NodeRuns are compared after
// sorting by (NodeID, Attempt) since the store does not guarantee row order.
func assertRunsEqual(t *testing.T, real, projected *models.Run) {
	t.Helper()

	if real.ID != projected.ID {
		t.Errorf("ID: real=%q projected=%q", real.ID, projected.ID)
	}
	if real.PipelineID != projected.PipelineID {
		t.Errorf("PipelineID: real=%q projected=%q", real.PipelineID, projected.PipelineID)
	}
	if real.Status != projected.Status {
		t.Errorf("Status: real=%q projected=%q", real.Status, projected.Status)
	}
	if real.Error != projected.Error {
		t.Errorf("Error: real=%q projected=%q", real.Error, projected.Error)
	}
	if real.TraceID != projected.TraceID {
		t.Errorf("TraceID: real=%q projected=%q", real.TraceID, projected.TraceID)
	}
	if real.PipelineVersion != projected.PipelineVersion {
		t.Errorf("PipelineVersion: real=%d projected=%d", real.PipelineVersion, projected.PipelineVersion)
	}
	if real.ResumedFromRunID != projected.ResumedFromRunID {
		t.Errorf("ResumedFromRunID: real=%q projected=%q", real.ResumedFromRunID, projected.ResumedFromRunID)
	}
	if !timeEqual(real.StartedAt, projected.StartedAt) {
		t.Errorf("StartedAt: real=%v projected=%v", real.StartedAt, projected.StartedAt)
	}
	if !timeEqual(real.FinishedAt, projected.FinishedAt) {
		t.Errorf("FinishedAt: real=%v projected=%v", real.FinishedAt, projected.FinishedAt)
	}

	realNodeRuns := append([]models.NodeRun(nil), real.NodeRuns...)
	projectedNodeRuns := append([]models.NodeRun(nil), projected.NodeRuns...)
	sortNodeRuns(realNodeRuns)
	sortNodeRuns(projectedNodeRuns)

	if len(realNodeRuns) != len(projectedNodeRuns) {
		t.Fatalf("NodeRuns count: real=%d projected=%d", len(realNodeRuns), len(projectedNodeRuns))
	}
	for i := range realNodeRuns {
		r, p := realNodeRuns[i], projectedNodeRuns[i]
		if r.ID != p.ID {
			t.Errorf("NodeRuns[%d].ID: real=%q projected=%q", i, r.ID, p.ID)
		}
		if r.NodeID != p.NodeID {
			t.Errorf("NodeRuns[%d].NodeID: real=%q projected=%q", i, r.NodeID, p.NodeID)
		}
		if r.Attempt != p.Attempt {
			t.Errorf("NodeRuns[%d].Attempt: real=%d projected=%d", i, r.Attempt, p.Attempt)
		}
		if r.Status != p.Status {
			t.Errorf("NodeRuns[%d].Status: real=%q projected=%q", i, r.Status, p.Status)
		}
		if r.RowCount != p.RowCount {
			t.Errorf("NodeRuns[%d].RowCount: real=%d projected=%d", i, r.RowCount, p.RowCount)
		}
		if r.DurationMs != p.DurationMs {
			t.Errorf("NodeRuns[%d].DurationMs: real=%d projected=%d", i, r.DurationMs, p.DurationMs)
		}
		if r.Error != p.Error {
			t.Errorf("NodeRuns[%d].Error: real=%q projected=%q", i, r.Error, p.Error)
		}
		if !timeEqual(r.StartedAt, p.StartedAt) {
			t.Errorf("NodeRuns[%d].StartedAt: real=%v projected=%v", i, r.StartedAt, p.StartedAt)
		}
	}
}

func sortNodeRuns(nrs []models.NodeRun) {
	sort.Slice(nrs, func(i, j int) bool {
		if nrs[i].NodeID != nrs[j].NodeID {
			return nrs[i].NodeID < nrs[j].NodeID
		}
		return nrs[i].Attempt < nrs[j].Attempt
	})
}

func timeEqual(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}
