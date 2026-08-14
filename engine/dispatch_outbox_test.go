package engine

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// newDispatchOutboxTestEngine returns an Engine (over a fault-injecting
// store wrapping a real SQLite store) and a JobQueue, both wired for
// RunPipelineAsync's queued-dispatch path, plus the real store and pipeline
// ID for post-hoc assertions.
func newDispatchOutboxTestEngine(t *testing.T) (*Engine, *faultInjectingStore, *store.SQLiteStore, string) {
	t.Helper()
	dir := t.TempDir()
	real, err := store.NewSQLiteStore(filepath.Join(dir, "outbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = real.Close() })

	// This pipeline is never actually executed by these tests (they only
	// exercise RunPipelineAsync's dispatch outbox, not ExecuteQueuedRun), so
	// the source path need not exist — only ValidatePipeline's "at least one
	// node" requirement matters here.
	pipeline := &models.Pipeline{
		ID:   "outbox-pipeline",
		Name: "Outbox pipeline",
		Nodes: []models.Node{{
			ID:     "source",
			Type:   models.NodeTypeSourceFile,
			Name:   "Source",
			Config: map[string]interface{}{"path": filepath.Join(dir, "unused.csv"), "format": "csv"},
		}},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := real.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}

	fault := &faultInjectingStore{SQLiteStore: real}
	eng := drainEngineOnCleanup(t, NewEngine(fault))
	eng.JobQueue = &recordingJobQueue{}
	return eng, fault, real, pipeline.ID
}

// TestRunPipelineAsyncOutboxCommitsRunEventAndAttemptTogether is the happy
// path: RunPipelineAsync's dispatch outbox (Tnsor-Labs/brokoli#7) must
// commit the pending run row, its run.created event, and its durable
// execution_attempts outbox record together, before the job is even
// enqueued.
func TestRunPipelineAsyncOutboxCommitsRunEventAndAttemptTogether(t *testing.T) {
	eng, _, real, pipelineID := newDispatchOutboxTestEngine(t)

	runID, err := eng.RunPipelineAsync(pipelineID)
	if err != nil {
		t.Fatalf("RunPipelineAsync: %v", err)
	}

	run, err := real.GetRun(runID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != models.RunStatusPending {
		t.Fatalf("run status = %s, want pending", run.Status)
	}

	events, err := real.ListEventsByRun(runID)
	if err != nil {
		t.Fatalf("ListEventsByRun: %v", err)
	}
	foundCreated := false
	for _, ev := range events {
		if ev.EventType == models.RunEventCreated {
			foundCreated = true
		}
	}
	if !foundCreated {
		t.Fatalf("run_events for %s = %v, want a run.created event", runID, events)
	}

	attempt, err := real.GetExecutionAttempt(runID, "", "", 0)
	if err != nil {
		t.Fatalf("GetExecutionAttempt: %v (the outbox record must exist once RunPipelineAsync returns successfully)", err)
	}
	if attempt.Status != models.AttemptStatusQueued {
		t.Fatalf("outbox attempt status = %s, want queued", attempt.Status)
	}
	if attempt.IdempotencyKey != runID {
		t.Fatalf("outbox attempt idempotency key = %q, want %q", attempt.IdempotencyKey, runID)
	}
}

// TestRunPipelineAsyncOutboxTransactionRollsBackOnPartialFailure proves the
// dispatch outbox is genuinely one transaction, not the old two independent
// calls: if any write inside it fails, none of them are left behind — in
// particular, no orphaned `pending` run row is left with nothing durable
// recording that dispatch was ever intended. That specific "crash between
// CreateRun and JobQueue.Enqueue" gap is what issue #7 closes.
func TestRunPipelineAsyncOutboxTransactionRollsBackOnPartialFailure(t *testing.T) {
	cases := []struct {
		name       string
		injectFail func(*faultInjectingStore)
	}{
		{"AppendEventTx fails after CreateRunTx succeeded", func(f *faultInjectingStore) { f.failAppendEventTx = true }},
		{"CreateExecutionAttemptTx fails after CreateRunTx and AppendEventTx succeeded", func(f *faultInjectingStore) { f.failCreateExecutionAttemptTx = true }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng, fault, real, pipelineID := newDispatchOutboxTestEngine(t)
			tc.injectFail(fault)

			runID, err := eng.RunPipelineAsync(pipelineID)
			if err == nil {
				t.Fatalf("RunPipelineAsync returned nil error despite an injected mid-transaction failure (run ID %q)", runID)
			}

			runs, listErr := real.ListRunsByPipeline(pipelineID, 10)
			if listErr != nil {
				t.Fatal(listErr)
			}
			// Before this fix, CreateRun and the rest were independent
			// calls: a failure here would still have left a `pending` run
			// row behind with no event and no outbox record — exactly the
			// orphaned-run bug. Now that they are one transaction, the
			// CreateRunTx write must have rolled back too.
			if len(runs) != 0 {
				t.Fatalf("runs after mid-transaction failure = %+v, want 0 (CreateRunTx must have rolled back with the rest of the transaction)", runs)
			}
		})
	}
}

// TestRunPipelineAsyncOutboxSurvivesEnqueueFailureAfterCommit checks the
// boundary the transaction does NOT cover: JobQueue.Enqueue happens after
// the DB transaction commits, so its own failure is handled by the
// existing (pre-#7) fail-the-run compensation path, not by rolling back the
// outbox — the outbox record intentionally persists as evidence dispatch
// was durably intended even though publication failed.
func TestRunPipelineAsyncOutboxSurvivesEnqueueFailureAfterCommit(t *testing.T) {
	eng, _, real, pipelineID := newDispatchOutboxTestEngine(t)
	eng.JobQueue = &recordingJobQueue{enqueueErr: errInjected}

	runID, err := eng.RunPipelineAsync(pipelineID)
	if err == nil {
		t.Fatal("RunPipelineAsync returned nil error despite JobQueue.Enqueue failing")
	}
	_ = runID

	runs, err := real.ListRunsByPipeline(pipelineID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Status != models.RunStatusFailed {
		t.Fatalf("runs after enqueue failure = %+v, want exactly 1 run marked failed", runs)
	}

	attempt, err := real.GetExecutionAttempt(runs[0].ID, "", "", 0)
	if err != nil {
		t.Fatalf("GetExecutionAttempt: %v (the outbox record committed before Enqueue was even attempted, so it must still exist)", err)
	}
	if attempt.Status != models.AttemptStatusQueued {
		t.Fatalf("outbox attempt status = %s, want queued (still reflects the dispatch intent; reconciling it against the failed run is follow-up work)", attempt.Status)
	}
}
