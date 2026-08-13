package engine

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// TestRecoveryTransitionGracePeriodCoversRealContention is the direct
// regression test for the second false-reclaim race found live: a node
// transition under real multi-tenant CPU contention (several concurrent
// runs sharing one worker pod — exactly what admission control lets
// through and what elastic scaling exists to absorb) took ~12s where an
// uncontended pod sees single-digit milliseconds, exceeding the original
// 10s grace period and reproducing the identical bug
// TestRecoverNonTerminalRunsDefersRecentNodeTransition already covers for
// the uncontended case. recoveryTransitionGracePeriod must stay at least
// as wide as reclaimSweepInterval — narrower defeats the whole point (an
// uncontended transition was already "milliseconds, not seconds"; the
// grace period exists specifically for the contended case, and shrinking
// it back below where contention was actually observed silently
// reintroduces this exact race).
func TestRecoveryTransitionGracePeriodCoversRealContention(t *testing.T) {
	if recoveryTransitionGracePeriod < reclaimSweepInterval {
		t.Fatalf("recoveryTransitionGracePeriod = %s, want >= reclaimSweepInterval (%s) — narrowing this reintroduces the false-reclaim race found under real pod contention",
			recoveryTransitionGracePeriod, reclaimSweepInterval)
	}
}

// newRecoveryTestEngine opens a fresh SQLite-backed Engine for
// RecoverNonTerminalRuns tests and returns both the Engine and the
// underlying store for direct fixture setup and post-hoc assertions.
func newRecoveryTestEngine(t *testing.T) (*Engine, *store.SQLiteStore) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.NewSQLiteStore(filepath.Join(dir, "recovery.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return NewEngine(s), s
}

// seedRecoveryPipeline creates a two-node source->sink pipeline. Recovery
// tests never actually execute it — they construct the run/event fixtures
// directly to simulate exactly where a crash left off — so the source path
// need not exist; only ValidatePipeline-independent shape matters (recovery
// itself never calls ValidatePipeline).
func seedRecoveryPipeline(t *testing.T, s *store.SQLiteStore, pipelineID string) *models.Pipeline {
	t.Helper()
	now := time.Now().UTC()
	pipe := &models.Pipeline{
		ID: pipelineID, Name: "Recovery Pipeline", Enabled: true,
		CreatedAt: now, UpdatedAt: now,
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceFile, Name: "Source", Config: map[string]interface{}{"path": "unused.csv"}},
			{ID: "sink", Type: models.NodeTypeSinkFile, Name: "Sink", Config: map[string]interface{}{"path": "unused-out.csv", "format": "csv"}},
		},
		Edges: []models.Edge{{From: "source", To: "sink"}},
	}
	if err := s.CreatePipeline(pipe); err != nil {
		t.Fatalf("CreatePipeline: %v", err)
	}
	return pipe
}

// seedOrphanedRun creates a run row directly with the given status —
// bypassing the real Runner entirely — so tests can plant exactly the
// runs/node_runs snapshot a crash would have left behind.
func seedOrphanedRun(t *testing.T, s *store.SQLiteStore, pipelineID, runID string, status models.RunStatus) *models.Run {
	t.Helper()
	now := time.Now().UTC()
	r := &models.Run{ID: runID, PipelineID: pipelineID, Status: status}
	if status != models.RunStatusPending {
		r.StartedAt = &now
	}
	if err := s.CreateRun(r); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	return r
}

func appendRecoveryEvent(t *testing.T, s *store.SQLiteStore, ev *models.RunEvent) {
	t.Helper()
	if err := s.AppendEvent(ev); err != nil {
		t.Fatalf("AppendEvent(%s): %v", ev.EventType, err)
	}
}

func attemptPtr(n int) *int { return &n }

// eventTypes extracts the ordered event_type sequence for a run, for
// assertions against the audit trail RecoverNonTerminalRuns is required to
// leave behind (issue #9's "recovery is itself auditable" requirement).
func eventTypes(t *testing.T, s *store.SQLiteStore, runID string) []models.RunEventType {
	t.Helper()
	events, err := s.ListEventsByRun(runID)
	if err != nil {
		t.Fatalf("ListEventsByRun: %v", err)
	}
	types := make([]models.RunEventType, len(events))
	for i, e := range events {
		types[i] = e.EventType
	}
	return types
}

func containsEventType(types []models.RunEventType, want models.RunEventType) bool {
	for _, t := range types {
		if t == want {
			return true
		}
	}
	return false
}

// TestRecoverNonTerminalRunsFinalizesCompletedDAGAsSuccess covers the
// crash_harness_test.go "after_node_completion_persist" / "before_run_
// terminal_persist" failpoints: every node in the pipeline succeeded, but
// the process died before the run-level UpdateRun(success) + RunEventTerminal
// ever persisted. Recovery must reconstruct that the DAG genuinely
// completed and finalize the run as success — not fail a run that actually
// finished correctly.
func TestRecoverNonTerminalRunsFinalizesCompletedDAGAsSuccess(t *testing.T) {
	eng, s := newRecoveryTestEngine(t)
	seedRecoveryPipeline(t, s, "pipe-success")
	run := seedOrphanedRun(t, s, "pipe-success", "run-success", models.RunStatusRunning)

	appendRecoveryEvent(t, s, &models.RunEvent{
		RunID: run.ID, EventType: models.RunEventCreated,
		Payload: models.RunEventPayload{Status: models.RunStatusRunning, PipelineID: run.PipelineID, StartedAt: run.StartedAt},
	})
	for _, nodeID := range []string{"source", "sink"} {
		appendRecoveryEvent(t, s, &models.RunEvent{
			RunID: run.ID, NodeID: nodeID, Attempt: attemptPtr(0), EventType: models.AttemptStarted,
			Payload: models.RunEventPayload{Status: models.RunStatusRunning, NodeRunID: nodeID + "-nr"},
		})
		appendRecoveryEvent(t, s, &models.RunEvent{
			RunID: run.ID, NodeID: nodeID, Attempt: attemptPtr(0), EventType: models.AttemptCompleted,
			Payload: models.RunEventPayload{Status: models.RunStatusSuccess, NodeRunID: nodeID + "-nr", RowCount: 1},
		})
	}

	summary, err := eng.RecoverNonTerminalRuns()
	if err != nil {
		t.Fatalf("RecoverNonTerminalRuns: %v", err)
	}
	if summary.RunsScanned != 1 || summary.RunsReconciled != 1 || summary.RunsFailed != 0 || summary.RunsDeferred != 0 {
		t.Fatalf("summary = %+v, want 1 scanned, 1 reconciled, 0 failed, 0 deferred", summary)
	}

	got, err := s.GetRun(run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Status != models.RunStatusSuccess {
		t.Fatalf("run status after recovery = %s, want success", got.Status)
	}
	if got.FinishedAt == nil {
		t.Fatal("FinishedAt not set after recovery")
	}

	types := eventTypes(t, s, run.ID)
	if !containsEventType(types, models.RunEventRecoveryStarted) {
		t.Errorf("events %v missing RunEventRecoveryStarted", types)
	}
	if !containsEventType(types, models.RunEventTerminal) {
		t.Errorf("events %v missing RunEventTerminal", types)
	}
	if !containsEventType(types, models.RunEventRecoveryCompleted) {
		t.Errorf("events %v missing RunEventRecoveryCompleted", types)
	}
	if containsEventType(types, models.RunEventRecoveryFailed) {
		t.Errorf("events %v should not contain RunEventRecoveryFailed for a cleanly completed DAG", types)
	}
	if eng.RunsRecovered != 1 {
		t.Errorf("eng.RunsRecovered = %d, want 1", eng.RunsRecovered)
	}
}

func TestRecoverNonTerminalRunsTreatsSkippedNodesAsComplete(t *testing.T) {
	eng, s := newRecoveryTestEngine(t)
	seedRecoveryPipeline(t, s, "pipe-skipped")
	run := seedOrphanedRun(t, s, "pipe-skipped", "run-skipped", models.RunStatusRunning)

	appendRecoveryEvent(t, s, &models.RunEvent{
		RunID: run.ID, EventType: models.RunEventCreated,
		Payload: models.RunEventPayload{Status: models.RunStatusRunning, PipelineID: run.PipelineID, StartedAt: run.StartedAt},
	})
	appendRecoveryEvent(t, s, &models.RunEvent{
		RunID: run.ID, NodeID: "source", Attempt: attemptPtr(0), EventType: models.AttemptStarted,
		Payload: models.RunEventPayload{Status: models.RunStatusRunning, NodeRunID: "source-nr"},
	})
	appendRecoveryEvent(t, s, &models.RunEvent{
		RunID: run.ID, NodeID: "source", Attempt: attemptPtr(0), EventType: models.AttemptCompleted,
		Payload: models.RunEventPayload{Status: models.RunStatusSuccess, NodeRunID: "source-nr", RowCount: 1},
	})
	appendRecoveryEvent(t, s, &models.RunEvent{
		RunID: run.ID, NodeID: "sink", Attempt: attemptPtr(0), EventType: models.AttemptSkipped,
		Payload: models.RunEventPayload{Status: models.RunStatusSkipped, NodeRunID: "sink-nr"},
	})

	if _, err := eng.RecoverNonTerminalRuns(); err != nil {
		t.Fatalf("recover: %v", err)
	}
	recovered, err := s.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != models.RunStatusSuccess {
		t.Fatalf("recovered status = %q, want success", recovered.Status)
	}
	events, err := s.ListEventsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	latest := latestNodeOutcome(ProjectRun(run.ID, events).NodeRuns)
	if latest["sink"].Status != models.RunStatusSkipped {
		t.Fatalf("recovered sink status = %q, want skipped", latest["sink"].Status)
	}
}

// TestRecoverNonTerminalRunsFinalizesFailedNodeAsFailed covers a run where
// one node definitively failed before the process died (e.g. the failure
// itself persisted, but the run-level UpdateRun(failed) never got to run) —
// recovery must reconstruct the same failed outcome Runner.failRun would
// have persisted, including its error message, not treat trailing
// never-started nodes as ambiguous.
func TestRecoverNonTerminalRunsFinalizesFailedNodeAsFailed(t *testing.T) {
	eng, s := newRecoveryTestEngine(t)
	seedRecoveryPipeline(t, s, "pipe-failed-node")
	run := seedOrphanedRun(t, s, "pipe-failed-node", "run-failed-node", models.RunStatusRunning)

	appendRecoveryEvent(t, s, &models.RunEvent{
		RunID: run.ID, EventType: models.RunEventCreated,
		Payload: models.RunEventPayload{Status: models.RunStatusRunning, PipelineID: run.PipelineID, StartedAt: run.StartedAt},
	})
	appendRecoveryEvent(t, s, &models.RunEvent{
		RunID: run.ID, NodeID: "source", Attempt: attemptPtr(0), EventType: models.AttemptStarted,
		Payload: models.RunEventPayload{Status: models.RunStatusRunning, NodeRunID: "source-nr"},
	})
	appendRecoveryEvent(t, s, &models.RunEvent{
		RunID: run.ID, NodeID: "source", Attempt: attemptPtr(0), EventType: models.AttemptFailed,
		Payload: models.RunEventPayload{Status: models.RunStatusFailed, NodeRunID: "source-nr", Error: "boom: source unreadable"},
	})
	// "sink" never started — the Runner aborts the whole DAG on the first
	// node error within a wave, so this is expected, not ambiguous.

	summary, err := eng.RecoverNonTerminalRuns()
	if err != nil {
		t.Fatalf("RecoverNonTerminalRuns: %v", err)
	}
	if summary.RunsReconciled != 1 || summary.RunsFailed != 0 {
		t.Fatalf("summary = %+v, want 1 reconciled, 0 (no-recoverable-path) failed", summary)
	}

	got, err := s.GetRun(run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Status != models.RunStatusFailed {
		t.Fatalf("run status = %s, want failed", got.Status)
	}
	if got.Error != "boom: source unreadable" {
		t.Fatalf("run error = %q, want the failed node's error", got.Error)
	}

	types := eventTypes(t, s, run.ID)
	if !containsEventType(types, models.RunEventTerminal) {
		t.Errorf("events %v missing RunEventTerminal", types)
	}
	if containsEventType(types, models.RunEventRecoveryFailed) {
		t.Errorf("events %v should use RunEventTerminal (a real node failure), not RunEventRecoveryFailed", types)
	}
}

// TestRecoverNonTerminalRunsMarksNoRecoverablePathFailed covers a run where
// no node ever started (crashed right after run creation) — there is
// nothing in the event log to reconstruct a real outcome from, so recovery
// must not guess; it marks the run failed via the dedicated
// RunEventRecoveryFailed event type instead of the ordinary RunEventTerminal.
func TestRecoverNonTerminalRunsMarksNoRecoverablePathFailed(t *testing.T) {
	old := recoveryTransitionGracePeriod
	recoveryTransitionGracePeriod = 0
	defer func() { recoveryTransitionGracePeriod = old }()

	eng, s := newRecoveryTestEngine(t)
	seedRecoveryPipeline(t, s, "pipe-no-nodes")
	run := seedOrphanedRun(t, s, "pipe-no-nodes", "run-no-nodes", models.RunStatusRunning)

	appendRecoveryEvent(t, s, &models.RunEvent{
		RunID: run.ID, EventType: models.RunEventCreated,
		Payload: models.RunEventPayload{Status: models.RunStatusRunning, PipelineID: run.PipelineID, StartedAt: run.StartedAt},
	})

	summary, err := eng.RecoverNonTerminalRuns()
	if err != nil {
		t.Fatalf("RecoverNonTerminalRuns: %v", err)
	}
	if summary.RunsFailed != 1 || summary.RunsReconciled != 0 {
		t.Fatalf("summary = %+v, want 1 no-recoverable-path failure, 0 reconciled", summary)
	}

	got, err := s.GetRun(run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Status != models.RunStatusFailed {
		t.Fatalf("run status = %s, want failed", got.Status)
	}
	if got.Error == "" {
		t.Fatal("expected a specific recovery-failed reason, got empty error")
	}

	types := eventTypes(t, s, run.ID)
	if !containsEventType(types, models.RunEventRecoveryFailed) {
		t.Errorf("events %v missing RunEventRecoveryFailed", types)
	}
	if containsEventType(types, models.RunEventTerminal) {
		t.Errorf("events %v should not contain a normal RunEventTerminal for a no-recoverable-path run", types)
	}
	if eng.RunsRecoveryFailed != 1 {
		t.Errorf("eng.RunsRecoveryFailed = %d, want 1", eng.RunsRecoveryFailed)
	}
}

// TestRecoverNonTerminalRunsMarksMidAttemptNodeAsNoRecoverablePath covers
// the crash_harness_test.go "after_node_attempt_create"/"before_node_
// execution" failpoints: a node's attempt started (AttemptStarted
// persisted) but never finished — the process died while it was actively
// running. Its side effect may or may not have completed; recovery cannot
// tell, so this must never be folded into success or failure, only
// "no recoverable path".
func TestRecoverNonTerminalRunsMarksMidAttemptNodeAsNoRecoverablePath(t *testing.T) {
	old := recoveryTransitionGracePeriod
	recoveryTransitionGracePeriod = 0
	defer func() { recoveryTransitionGracePeriod = old }()

	eng, s := newRecoveryTestEngine(t)
	seedRecoveryPipeline(t, s, "pipe-mid-attempt")
	run := seedOrphanedRun(t, s, "pipe-mid-attempt", "run-mid-attempt", models.RunStatusRunning)

	appendRecoveryEvent(t, s, &models.RunEvent{
		RunID: run.ID, EventType: models.RunEventCreated,
		Payload: models.RunEventPayload{Status: models.RunStatusRunning, PipelineID: run.PipelineID, StartedAt: run.StartedAt},
	})
	appendRecoveryEvent(t, s, &models.RunEvent{
		RunID: run.ID, NodeID: "source", Attempt: attemptPtr(0), EventType: models.AttemptStarted,
		Payload: models.RunEventPayload{Status: models.RunStatusRunning, NodeRunID: "source-nr"},
	})
	// No AttemptCompleted/AttemptFailed — the process died mid-attempt.

	summary, err := eng.RecoverNonTerminalRuns()
	if err != nil {
		t.Fatalf("RecoverNonTerminalRuns: %v", err)
	}
	if summary.RunsFailed != 1 {
		t.Fatalf("summary = %+v, want 1 no-recoverable-path failure", summary)
	}

	got, err := s.GetRun(run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Status != models.RunStatusFailed {
		t.Fatalf("run status = %s, want failed", got.Status)
	}

	types := eventTypes(t, s, run.ID)
	if !containsEventType(types, models.RunEventRecoveryFailed) {
		t.Errorf("events %v missing RunEventRecoveryFailed", types)
	}
	if !containsEventType(types, models.AttemptFailed) {
		t.Errorf("events %v missing AttemptFailed for the dangling mid-attempt node — closeDanglingNodeRun should have appended one", types)
	}

	// Direct regression check for the zombie-node-run bug found live: a run
	// correctly marked failed by recovery still showed its ambiguous node
	// stuck at "running" forever, since markRecoveryFailed only ever
	// touched the run row. closeDanglingNodeRun must settle it too.
	nodeRuns, err := s.ListNodeRunsByRun(run.ID)
	if err != nil {
		t.Fatalf("ListNodeRunsByRun: %v", err)
	}
	for _, nr := range nodeRuns {
		if nr.NodeID == "source" && nr.Status != models.RunStatusFailed {
			t.Fatalf("source node run status = %s, want failed (must not stay running forever on a failed run)", nr.Status)
		}
	}
}

// TestRecoverNonTerminalRunsDefersRecentNodeTransition is the direct
// regression test for the false-reclaim race found live-testing #167's
// periodic reclaim sweep under real concurrent load: "source" just finished
// successfully and "sink" has no execution_attempts row yet (the owning
// process's Runner is in the brief in-process gap between completing one
// node and claiming the next). reconcileExecutionAttempts alone sees no
// live lease at all in that instant, since the only two states its scan
// covers are terminal (source, just completed) and never-claimed (sink) —
// this must not be misread as an orphaned run, only as recent-enough
// activity to defer, exactly like TestRecoverNonTerminalRunsDefersToLiveLease
// does for an explicit lease.
func TestRecoverNonTerminalRunsDefersRecentNodeTransition(t *testing.T) {
	eng, s := newRecoveryTestEngine(t)
	seedRecoveryPipeline(t, s, "pipe-recent-transition")
	run := seedOrphanedRun(t, s, "pipe-recent-transition", "run-recent-transition", models.RunStatusRunning)

	appendRecoveryEvent(t, s, &models.RunEvent{
		RunID: run.ID, EventType: models.RunEventCreated,
		Payload: models.RunEventPayload{Status: models.RunStatusRunning, PipelineID: run.PipelineID, StartedAt: run.StartedAt},
	})
	appendRecoveryEvent(t, s, &models.RunEvent{
		RunID: run.ID, NodeID: "source", Attempt: attemptPtr(0), EventType: models.AttemptStarted,
		Payload: models.RunEventPayload{Status: models.RunStatusRunning, NodeRunID: "source-nr"},
	})
	appendRecoveryEvent(t, s, &models.RunEvent{
		RunID: run.ID, NodeID: "source", Attempt: attemptPtr(0), EventType: models.AttemptCompleted,
		Payload: models.RunEventPayload{Status: models.RunStatusSuccess, NodeRunID: "source-nr", RowCount: 1},
	})
	// No attempt for "sink" at all — recovery runs in the gap right after
	// "source" finished, before the still-live process claims it.

	summary, err := eng.RecoverNonTerminalRuns()
	if err != nil {
		t.Fatalf("RecoverNonTerminalRuns: %v", err)
	}
	if summary.RunsDeferred != 1 || summary.RunsFailed != 0 || summary.RunsReconciled != 0 {
		t.Fatalf("summary = %+v, want 1 deferred, 0 failed, 0 reconciled", summary)
	}

	got, err := s.GetRun(run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Status != models.RunStatusRunning {
		t.Fatalf("run status = %s, want running (left untouched, not force-failed)", got.Status)
	}

	types := eventTypes(t, s, run.ID)
	if containsEventType(types, models.RunEventRecoveryFailed) {
		t.Errorf("events %v should not contain RunEventRecoveryFailed — this run is still plausibly live", types)
	}
}

// TestRecoverNonTerminalRunsEventuallyFailsAfterRepeatedDefers is the direct
// regression test for a second bug found live testing right after the first
// (TestRecoverNonTerminalRunsDefersRecentNodeTransition): recoverRun
// unconditionally appends RunEventRecoveryStarted at the very top of every
// call, before the grace-period check ever runs — including a pass that
// only ends up deferring. Reading events[len(events)-1] naively for
// recency therefore always finds THIS SAME PASS's own just-appended
// bookkeeping event, which is by definition always fresh — so a genuinely
// orphaned run would defer forever, every 20s, and never actually get
// reclaimed. Live evidence: 4 runs left mid-flight by a dead worker sat
// "running" with since_last_event under 5ms on every sweep tick for
// several minutes straight. Calling RecoverNonTerminalRuns twice, with a
// real sleep past the grace period in between, proves the fix
// (lastGenuineActivity skipping recovery's own event types) actually lets
// the second pass reclaim it, despite the first pass's RecoveryStarted
// event being newer than the grace period at call time.
func TestRecoverNonTerminalRunsEventuallyFailsAfterRepeatedDefers(t *testing.T) {
	old := recoveryTransitionGracePeriod
	recoveryTransitionGracePeriod = 30 * time.Millisecond
	defer func() { recoveryTransitionGracePeriod = old }()

	eng, s := newRecoveryTestEngine(t)
	seedRecoveryPipeline(t, s, "pipe-repeated-defer")
	run := seedOrphanedRun(t, s, "pipe-repeated-defer", "run-repeated-defer", models.RunStatusRunning)

	appendRecoveryEvent(t, s, &models.RunEvent{
		RunID: run.ID, EventType: models.RunEventCreated,
		Payload: models.RunEventPayload{Status: models.RunStatusRunning, PipelineID: run.PipelineID, StartedAt: run.StartedAt},
	})
	appendRecoveryEvent(t, s, &models.RunEvent{
		RunID: run.ID, NodeID: "source", Attempt: attemptPtr(0), EventType: models.AttemptStarted,
		Payload: models.RunEventPayload{Status: models.RunStatusRunning, NodeRunID: "source-nr"},
	})
	// No AttemptCompleted/AttemptFailed — process died mid-attempt, exactly
	// like TestRecoverNonTerminalRunsMarksMidAttemptNodeAsNoRecoverablePath.

	summary1, err := eng.RecoverNonTerminalRuns()
	if err != nil {
		t.Fatalf("RecoverNonTerminalRuns (pass 1): %v", err)
	}
	if summary1.RunsDeferred != 1 || summary1.RunsFailed != 0 {
		t.Fatalf("pass 1 summary = %+v, want 1 deferred, 0 failed (events still fresh)", summary1)
	}
	got, err := s.GetRun(run.ID)
	if err != nil {
		t.Fatalf("GetRun after pass 1: %v", err)
	}
	if got.Status != models.RunStatusRunning {
		t.Fatalf("status after pass 1 = %s, want still running", got.Status)
	}

	time.Sleep(60 * time.Millisecond) // past recoveryTransitionGracePeriod

	summary2, err := eng.RecoverNonTerminalRuns()
	if err != nil {
		t.Fatalf("RecoverNonTerminalRuns (pass 2): %v", err)
	}
	if summary2.RunsFailed != 1 || summary2.RunsDeferred != 0 {
		t.Fatalf("pass 2 summary = %+v, want 1 failed, 0 deferred — a genuinely dead run must not defer forever just because recovery's own bookkeeping event from pass 1 looks recent", summary2)
	}
	got, err = s.GetRun(run.ID)
	if err != nil {
		t.Fatalf("GetRun after pass 2: %v", err)
	}
	if got.Status != models.RunStatusFailed {
		t.Fatalf("status after pass 2 = %s, want failed", got.Status)
	}
}

// TestRecoverNonTerminalRunsLeavesPendingRunsAlone proves a run that was
// queued but never claimed by any worker is not treated as orphaned — it
// legitimately has no node-execution history yet, and redelivering its
// dispatch is the job queue's responsibility, not recovery's.
func TestRecoverNonTerminalRunsLeavesPendingRunsAlone(t *testing.T) {
	eng, s := newRecoveryTestEngine(t)
	seedRecoveryPipeline(t, s, "pipe-pending")
	run := seedOrphanedRun(t, s, "pipe-pending", "run-pending", models.RunStatusPending)

	appendRecoveryEvent(t, s, &models.RunEvent{
		RunID: run.ID, EventType: models.RunEventCreated,
		Payload: models.RunEventPayload{Status: models.RunStatusPending, PipelineID: run.PipelineID},
	})

	summary, err := eng.RecoverNonTerminalRuns()
	if err != nil {
		t.Fatalf("RecoverNonTerminalRuns: %v", err)
	}
	if summary.RunsDeferred != 1 || summary.RunsFailed != 0 || summary.RunsReconciled != 0 {
		t.Fatalf("summary = %+v, want 1 deferred, 0 failed, 0 reconciled", summary)
	}

	got, err := s.GetRun(run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Status != models.RunStatusPending {
		t.Fatalf("run status after recovery = %s, want unchanged pending", got.Status)
	}
}

// TestRecoverNonTerminalRunsDefersToLiveLease proves recovery never marks a
// run failed while an execution attempt still holds an unexpired lease —
// another process (or, in a multi-process deployment, a different pod) may
// genuinely still be executing it, and stealing that work would be worse
// than leaving it alone for this boot.
func TestRecoverNonTerminalRunsDefersToLiveLease(t *testing.T) {
	eng, s := newRecoveryTestEngine(t)
	seedRecoveryPipeline(t, s, "pipe-live-lease")
	run := seedOrphanedRun(t, s, "pipe-live-lease", "run-live-lease", models.RunStatusRunning)
	appendRecoveryEvent(t, s, &models.RunEvent{
		RunID: run.ID, EventType: models.RunEventCreated,
		Payload: models.RunEventPayload{Status: models.RunStatusRunning, PipelineID: run.PipelineID, StartedAt: run.StartedAt},
	})

	createTestAttempt(t, s, run.ID, "", 0)
	gen, ok, err := s.ClaimAttempt(run.ID, "", "", 0, "other-process", time.Hour)
	if err != nil || !ok {
		t.Fatalf("ClaimAttempt: ok=%v err=%v", ok, err)
	}
	_ = gen

	summary, err := eng.RecoverNonTerminalRuns()
	if err != nil {
		t.Fatalf("RecoverNonTerminalRuns: %v", err)
	}
	if summary.RunsDeferred != 1 || summary.RunsFailed != 0 || summary.RunsReconciled != 0 {
		t.Fatalf("summary = %+v, want 1 deferred, 0 failed, 0 reconciled", summary)
	}

	got, err := s.GetRun(run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Status != models.RunStatusRunning {
		t.Fatalf("run status after recovery = %s, want unchanged running (live lease must not be stolen)", got.Status)
	}

	attempt, err := s.GetExecutionAttempt(run.ID, "", "", 0)
	if err != nil {
		t.Fatalf("GetExecutionAttempt: %v", err)
	}
	if attempt.Status != models.AttemptStatusClaimed {
		t.Fatalf("attempt status = %s, want still claimed (untouched)", attempt.Status)
	}
}

// TestRecoverNonTerminalRunsReclaimsExpiredLease proves an attempt whose
// lease has already lapsed is settled to failed (fencing-checked) rather
// than left claimed forever, and that the run itself still proceeds through
// the normal event-log reconciliation afterward.
func TestRecoverNonTerminalRunsReclaimsExpiredLease(t *testing.T) {
	old := recoveryTransitionGracePeriod
	recoveryTransitionGracePeriod = 0
	defer func() { recoveryTransitionGracePeriod = old }()

	eng, s := newRecoveryTestEngine(t)
	seedRecoveryPipeline(t, s, "pipe-expired-lease")
	run := seedOrphanedRun(t, s, "pipe-expired-lease", "run-expired-lease", models.RunStatusRunning)
	appendRecoveryEvent(t, s, &models.RunEvent{
		RunID: run.ID, EventType: models.RunEventCreated,
		Payload: models.RunEventPayload{Status: models.RunStatusRunning, PipelineID: run.PipelineID, StartedAt: run.StartedAt},
	})

	createTestAttempt(t, s, run.ID, "", 0)
	gen, ok, err := s.ClaimAttempt(run.ID, "", "", 0, "dead-process", -time.Second) // already expired
	if err != nil || !ok {
		t.Fatalf("ClaimAttempt: ok=%v err=%v", ok, err)
	}
	_ = gen

	summary, err := eng.RecoverNonTerminalRuns()
	if err != nil {
		t.Fatalf("RecoverNonTerminalRuns: %v", err)
	}
	if summary.AttemptsReclaimed != 1 {
		t.Fatalf("summary.AttemptsReclaimed = %d, want 1", summary.AttemptsReclaimed)
	}
	// No node ever started, so the run itself still has no recoverable path
	// — the lease reclaim and the run-level outcome are independent.
	if summary.RunsFailed != 1 {
		t.Fatalf("summary = %+v, want 1 no-recoverable-path failure", summary)
	}

	attempt, err := s.GetExecutionAttempt(run.ID, "", "", 0)
	if err != nil {
		t.Fatalf("GetExecutionAttempt: %v", err)
	}
	if attempt.Status != models.AttemptStatusFailed {
		t.Fatalf("attempt status = %s, want failed (reclaimed)", attempt.Status)
	}
	if eng.AttemptsReclaimed != 1 {
		t.Errorf("eng.AttemptsReclaimed = %d, want 1", eng.AttemptsReclaimed)
	}
}

// TestRecoverNonTerminalRunsIsIdempotentAcrossRepeatedRestarts is the
// acceptance criterion from issue #9 stated directly: running recovery
// twice must produce the same end state both times, and the second pass
// must not re-process a run the first pass already resolved.
func TestRecoverNonTerminalRunsIsIdempotentAcrossRepeatedRestarts(t *testing.T) {
	eng, s := newRecoveryTestEngine(t)
	seedRecoveryPipeline(t, s, "pipe-idempotent")
	run := seedOrphanedRun(t, s, "pipe-idempotent", "run-idempotent", models.RunStatusRunning)
	appendRecoveryEvent(t, s, &models.RunEvent{
		RunID: run.ID, EventType: models.RunEventCreated,
		Payload: models.RunEventPayload{Status: models.RunStatusRunning, PipelineID: run.PipelineID, StartedAt: run.StartedAt},
	})
	for _, nodeID := range []string{"source", "sink"} {
		appendRecoveryEvent(t, s, &models.RunEvent{
			RunID: run.ID, NodeID: nodeID, Attempt: attemptPtr(0), EventType: models.AttemptStarted,
			Payload: models.RunEventPayload{Status: models.RunStatusRunning, NodeRunID: nodeID + "-nr"},
		})
		appendRecoveryEvent(t, s, &models.RunEvent{
			RunID: run.ID, NodeID: nodeID, Attempt: attemptPtr(0), EventType: models.AttemptCompleted,
			Payload: models.RunEventPayload{Status: models.RunStatusSuccess, NodeRunID: nodeID + "-nr"},
		})
	}

	first, err := eng.RecoverNonTerminalRuns()
	if err != nil {
		t.Fatalf("first RecoverNonTerminalRuns: %v", err)
	}
	if first.RunsScanned != 1 || first.RunsReconciled != 1 {
		t.Fatalf("first summary = %+v, want 1 scanned/reconciled", first)
	}
	firstEventCount := len(eventTypes(t, s, run.ID))

	// Simulate a second restart of the same (now-terminal) database: call
	// recovery again exactly as cmd/serve.go would on the next boot.
	second, err := eng.RecoverNonTerminalRuns()
	if err != nil {
		t.Fatalf("second RecoverNonTerminalRuns: %v", err)
	}
	if second.RunsScanned != 0 {
		t.Fatalf("second summary.RunsScanned = %d, want 0 (the run is already terminal — ListNonTerminalRuns must exclude it)", second.RunsScanned)
	}

	got, err := s.GetRun(run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Status != models.RunStatusSuccess {
		t.Fatalf("run status after second recovery pass = %s, want still success", got.Status)
	}
	secondEventCount := len(eventTypes(t, s, run.ID))
	if secondEventCount != firstEventCount {
		t.Fatalf("event count changed from %d to %d across the second recovery pass — recovery re-processed an already-terminal run", firstEventCount, secondEventCount)
	}
}

// createTestAttempt inserts a queued outbox/intent row exactly the way
// Engine.RunPipelineAsync's dispatch outbox does, so tests can then claim it
// via ClaimAttempt to simulate a live or expired lease.
func createTestAttempt(t *testing.T, s *store.SQLiteStore, runID, nodeID string, attempt int) {
	t.Helper()
	err := s.WithTx(func(tx *sql.Tx) error {
		return s.CreateExecutionAttemptTx(tx, &models.ExecutionAttempt{
			RunID:          runID,
			NodeID:         nodeID,
			Attempt:        attempt,
			Status:         models.AttemptStatusQueued,
			IdempotencyKey: runID,
		})
	})
	if err != nil {
		t.Fatalf("createTestAttempt(%s,%s,%d): %v", runID, nodeID, attempt, err)
	}
}
