package engine

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// recordingCancelRelay is a fake extensions.RunCancelRelay that records
// every broadcast instead of transporting it anywhere.
type recordingCancelRelay struct {
	mu         sync.Mutex
	broadcasts []string
}

func (r *recordingCancelRelay) BroadcastCancel(runID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.broadcasts = append(r.broadcasts, runID)
	return nil
}

func (r *recordingCancelRelay) SubscribeCancels(func(runID string)) error { return nil }

func (r *recordingCancelRelay) Close() error { return nil }

func (r *recordingCancelRelay) recorded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.broadcasts...)
}

func seedCancelTestPipeline(t *testing.T, s *store.SQLiteStore, pipeID string) {
	t.Helper()
	pipe := &models.Pipeline{
		ID: pipeID, Name: "Cancel Semantics", Enabled: true,
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceFile, Name: "Source", Config: map[string]interface{}{"path": execCtxSourceCSV(t), "format": "csv"}},
		},
	}
	if err := s.CreatePipeline(pipe); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
}

// TestCancelRun_PendingRunCancelledAtomically proves cancelling a queued,
// unclaimed run works at all (before the PendingRunCanceller capability it
// returned "not found or already completed" — the run then executed
// whenever a worker got to it), and that the cancel is a compare-and-swap
// no later claim can override: ClaimPendingRun on the cancelled run must
// lose.
func TestCancelRun_PendingRunCancelledAtomically(t *testing.T) {
	eng, s := newExecCtxTestEngine(t)
	seedCancelTestPipeline(t, s, "p-cancel-pending")

	run := &models.Run{ID: "run-pending-cancel", PipelineID: "p-cancel-pending", Status: models.RunStatusPending}
	if err := s.CreateRun(run); err != nil {
		t.Fatalf("create pending run: %v", err)
	}

	if err := eng.CancelRun(run.ID); err != nil {
		t.Fatalf("CancelRun on a pending run: %v", err)
	}

	stored, err := s.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != models.RunStatusCancelled {
		t.Fatalf("pending run status after cancel = %q, want cancelled", stored.Status)
	}
	if stored.FinishedAt == nil {
		t.Error("cancelled pending run has no FinishedAt")
	}

	claimed, err := s.ClaimPendingRun(run.ID, "p-cancel-pending", time.Now().UTC(), "trace")
	if err != nil {
		t.Fatal(err)
	}
	if claimed {
		t.Fatal("ClaimPendingRun claimed a run that was already cancelled — the conditional swap failed")
	}
}

// TestCancelRun_RunningElsewhere_Broadcasts proves the distributed path: a
// run that the store says is running but that has no Runner in this
// process (it executes on another instance) is broadcast through the
// configured relay instead of failing with "not found" — the exact live
// k3s failure where every API-pod cancel 404'd while the worker kept
// executing. Without a relay the error remains, loudly.
func TestCancelRun_RunningElsewhere_Broadcasts(t *testing.T) {
	eng, s := newExecCtxTestEngine(t)
	seedCancelTestPipeline(t, s, "p-cancel-remote")

	started := time.Now().UTC()
	run := &models.Run{ID: "run-remote", PipelineID: "p-cancel-remote", Status: models.RunStatusRunning, StartedAt: &started}
	if err := s.CreateRun(run); err != nil {
		t.Fatalf("create running run: %v", err)
	}

	// No relay configured: the cancel must fail loudly, not pretend.
	if err := eng.CancelRun(run.ID); err == nil {
		t.Fatal("CancelRun with no relay and no local runner should error")
	}

	relay := &recordingCancelRelay{}
	eng.CancelRelay = relay
	if err := eng.CancelRun(run.ID); err != nil {
		t.Fatalf("CancelRun with relay: %v", err)
	}
	got := relay.recorded()
	if len(got) != 1 || got[0] != run.ID {
		t.Fatalf("broadcasts = %v, want exactly [%s]", got, run.ID)
	}
}

// TestCancelRun_TerminalRun_Errors proves terminal runs are never
// re-cancelled or broadcast — a relay must not receive noise for runs
// that already finished.
func TestCancelRun_TerminalRun_Errors(t *testing.T) {
	eng, s := newExecCtxTestEngine(t)
	seedCancelTestPipeline(t, s, "p-cancel-terminal")

	relay := &recordingCancelRelay{}
	eng.CancelRelay = relay

	for _, status := range []models.RunStatus{models.RunStatusSuccess, models.RunStatusFailed, models.RunStatusCancelled} {
		run := &models.Run{ID: "run-terminal-" + string(status), PipelineID: "p-cancel-terminal", Status: status}
		if err := s.CreateRun(run); err != nil {
			t.Fatal(err)
		}
		if err := eng.CancelRun(run.ID); err == nil {
			t.Fatalf("CancelRun on %s run should error", status)
		}
	}
	if got := relay.recorded(); len(got) != 0 {
		t.Fatalf("terminal-run cancels reached the relay: %v", got)
	}
}

// TestCancelRelayedRun_CancelsLocalRun proves the receiving side of the
// relay: a broadcast delivered to the instance that owns the run cancels
// it exactly like a direct CancelRun — the in-flight executor observes
// context.Canceled and the run finalizes as cancelled. A relayed ID this
// instance does not own is a quiet no-op.
func TestCancelRelayedRun_CancelsLocalRun(t *testing.T) {
	eng, s := newExecCtxTestEngine(t)

	// Unknown run ID: must not panic, must not do anything.
	eng.CancelRelayedRun("no-such-run")

	exec := &blockingUntilCancelExecutor{
		nodeType:  "blocking",
		startedCh: make(chan struct{}),
		observed:  make(chan error, 1),
	}
	eng.Executors = []extensions.NodeExecutor{exec}

	pipe := &models.Pipeline{
		ID: "p-relayed-cancel", Name: "Relayed Cancel", Enabled: true,
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceFile, Name: "Source", Config: map[string]interface{}{"path": execCtxSourceCSV(t), "format": "csv"}},
			{ID: "blocking", Type: models.NodeType("blocking"), Name: "Blocking"},
		},
		Edges: []models.Edge{{From: "source", To: "blocking"}},
	}
	if err := s.CreatePipeline(pipe); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	runID, err := eng.RunPipelineAsync(pipe.ID)
	if err != nil {
		t.Fatalf("run pipeline async: %v", err)
	}
	select {
	case <-exec.startedCh:
	case <-time.After(5 * time.Second):
		t.Fatal("executor was never dispatched")
	}

	eng.CancelRelayedRun(runID)

	select {
	case obsErr := <-exec.observed:
		if !errors.Is(obsErr, context.Canceled) {
			t.Fatalf("executor observed %v, want context.Canceled", obsErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("executor never observed the relayed cancellation")
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		stored, getErr := s.GetRun(runID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if stored.Status == models.RunStatusCancelled {
			return
		}
		if stored.Status == models.RunStatusFailed {
			t.Fatalf("relayed-cancelled run was recorded as failed: %s", stored.Error)
		}
		if time.Now().After(deadline) {
			t.Fatalf("run status remained %q after relayed cancellation", stored.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestRunnerCancelBeforeExecuteIsDurable proves Cancel is not lost when it
// lands before Execute has created the run context — the window where a
// runner is registered in Engine.active (RunPipeline registers before
// spawning the goroutine; ExecuteQueuedRun can wait on the concurrency
// semaphore for minutes under saturation) but r.cancel is still nil.
// Before this, such a Cancel was a silent no-op and the run executed to
// completion as if never cancelled.
func TestRunnerCancelBeforeExecuteIsDurable(t *testing.T) {
	eng, s := newExecCtxTestEngine(t)
	seedCancelTestPipeline(t, s, "p-precancel")
	pipe, err := s.GetPipeline("p-precancel")
	if err != nil {
		t.Fatal(err)
	}

	eventCh := make(chan models.Event, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range eventCh {
		}
	}()
	t.Cleanup(func() { close(eventCh); <-done })
	_ = eng // engine only used for store/harness lifecycle

	r := NewRunner(s, eventCh, pipe, nil, nil, nil, nil, "", nil)
	r.Cancel() // before Execute: previously a silent no-op

	run, execErr := r.Execute()
	if execErr == nil {
		t.Fatal("Execute after pre-Execute Cancel should return the cancellation error")
	}
	if run == nil {
		t.Fatal("Execute should still return the run it created")
	}
	stored, err := s.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != models.RunStatusCancelled {
		t.Fatalf("pre-cancelled run status = %q, want cancelled", stored.Status)
	}
}
