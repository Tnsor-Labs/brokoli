package engine

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// Recovery records why it put a run back on the queue in run.Error, so an
// operator looking at the pending run can see what happened to it. That
// explanation describes the execution that was interrupted. Once the run
// executes again and succeeds, it is no longer true of the run, and leaving
// it there made a green run render with a recovery error against it.
//
// The audit trail is not what was wrong, so it must survive: the
// run.recovery_requeued event keeps the message, timestamped.

// seedExecutableRequeuePipeline is seedRequeuePipeline's runnable twin:
// source -> sink over a real CSV, so the same fixture that gets re-queued
// can then be executed to completion.
func seedExecutableRequeuePipeline(t *testing.T, s *store.SQLiteStore, dir, pipelineID string) *models.Pipeline {
	t.Helper()
	csv := filepath.Join(dir, "in.csv")
	if err := os.WriteFile(csv, []byte("id\n1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	pipe := &models.Pipeline{
		ID: pipelineID, Name: "Requeue then succeed", Enabled: true,
		CreatedAt: now, UpdatedAt: now,
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceFile, Name: "Source", Config: map[string]interface{}{"path": csv, "format": "csv"}},
			{ID: "sink", Type: models.NodeTypeSinkFile, Name: "Sink", Config: map[string]interface{}{"path": filepath.Join(dir, "out.csv"), "format": "csv"}},
		},
		Edges: []models.Edge{{From: "source", To: "sink"}},
	}
	if err := s.CreatePipeline(pipe); err != nil {
		t.Fatalf("CreatePipeline: %v", err)
	}
	return pipe
}

func TestRequeuedRunClearsTheRecoveryErrorWhenItSucceeds(t *testing.T) {
	eng, s, queue := newRequeueTestEngine(t)
	dir := t.TempDir()
	pipe := seedExecutableRequeuePipeline(t, s, dir, "p-requeue-success")
	run := seedInterruptedRun(t, s, pipe.ID, "run-requeue-success", "source")

	summary, err := eng.RecoverNonTerminalRuns()
	if err != nil {
		t.Fatal(err)
	}
	if summary.RunsRequeued != 1 {
		t.Fatalf("summary = %+v, want the run re-queued", summary)
	}
	if _, ok := queue.job(run.ID); !ok {
		t.Fatal("fixture: run was not enqueued")
	}
	requeued, err := s.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if requeued.Error == "" {
		t.Fatal("fixture: recovery is expected to record why it re-queued the run")
	}
	recoveryMsg := requeued.Error

	// The queue consumer picks it up; this time it runs to the end.
	final, err := eng.ExecuteQueuedRun(run.ID, pipe.ID, nil)
	if err != nil {
		t.Fatalf("ExecuteQueuedRun: %v", err)
	}
	if final.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %s (error %q), want success", final.Status, final.Error)
	}

	stored, err := s.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != models.RunStatusSuccess {
		t.Fatalf("stored status = %s, want success", stored.Status)
	}
	if stored.Error != "" {
		t.Errorf("successful run still carries the recovery error %q -- this is what made a green run look broken", stored.Error)
	}

	// The reason is still on the record, where it belongs.
	events, err := s.ListEventsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var kept string
	for _, ev := range events {
		if ev.EventType == models.RunEventRecoveryRequeued {
			kept = ev.Payload.Error
		}
	}
	if kept != recoveryMsg {
		t.Errorf("recovery_requeued event error = %q, want the audit trail preserved (%q)", kept, recoveryMsg)
	}
}

// The other direction: clearing on success must not become clearing on the
// way out, or a failed run would lose the only explanation its row carries.
//
// Worth being precise about what that explanation is, so this test is not
// misread later: the runner does not write the failing node's error into
// runs.error at all (only failAcceptedRun does, for runs that fail before
// execution). What a failed re-queued run's row holds is recovery's own
// message, and the failure reason lives on the terminal event. Both are
// asserted below; neither may go missing.
func TestRequeuedRunKeepsAnErrorWhenItFails(t *testing.T) {
	eng, s, _ := newRequeueTestEngine(t)
	dir := t.TempDir()
	pipe := seedExecutableRequeuePipeline(t, s, dir, "p-requeue-failure")
	run := seedInterruptedRun(t, s, pipe.ID, "run-requeue-failure", "source")

	if _, err := eng.RecoverNonTerminalRuns(); err != nil {
		t.Fatal(err)
	}
	requeued, err := s.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	recoveryMsg := requeued.Error
	if recoveryMsg == "" {
		t.Fatal("fixture: recovery is expected to record why it re-queued the run")
	}

	// Make the second execution fail: the source file is gone by the time
	// the queue gets to it, which is an ordinary node failure.
	if err := os.Remove(filepath.Join(dir, "in.csv")); err != nil {
		t.Fatal(err)
	}

	final, _ := eng.ExecuteQueuedRun(run.ID, pipe.ID, nil)
	if final == nil {
		t.Fatal("ExecuteQueuedRun returned no run")
	}
	if final.Status != models.RunStatusFailed {
		t.Fatalf("run status = %s, want failed", final.Status)
	}

	stored, err := s.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Error != recoveryMsg {
		t.Errorf("failed run's error = %q, want recovery's explanation kept (%q)", stored.Error, recoveryMsg)
	}

	events, err := s.ListEventsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var terminalErr string
	for _, ev := range events {
		if ev.EventType == models.RunEventTerminal && ev.Payload.Status == models.RunStatusFailed {
			terminalErr = ev.Payload.Error
		}
	}
	if terminalErr == "" {
		t.Error("a failed run must say why it failed somewhere; the terminal event is where the reason lives")
	}
}
