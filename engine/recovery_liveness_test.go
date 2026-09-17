package engine

// Recovery must not adopt a run somebody else is already executing.
//
// The bug these cover, measured in production: a run dispatched to a
// remote pool worker was executed TWICE, concurrently. The worker began
// node s1 at 19:32:55.823; the sweep fired at 19:32:56.176, found no
// events (the worker's first event did not land until 19:32:56.338),
// concluded "s1 never started", and requeued the run to an in-cluster
// worker, which ran all three nodes and marked the run success while the
// remote worker was still going. node_runs ended up with two rows per
// node.
//
// Neither existing safeguard could see it: Engine.active only knows
// runners in this process, and the worker's API-only store holds no
// execution-attempt lease. RecoveryTransitionGracePeriod keys on the last
// EVENT, and there were none yet.

import (
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

// startedAgo re-dates a seeded run so the age guard sees it as that old,
// in both the runs row and the in-memory copy the test asserts against.
func startedAgo(t *testing.T, s interface{ UpdateRun(*models.Run) error }, run *models.Run, d time.Duration) {
	t.Helper()
	at := time.Now().UTC().Add(-d)
	run.StartedAt = &at
	if err := s.UpdateRun(run); err != nil {
		t.Fatalf("UpdateRun: %v", err)
	}
}

// The headline case. A run that started moments ago is left alone, even
// though its durable trace looks exactly like an orphan: no events beyond
// creation, no lease, no local runner. Against main this requeues.
func TestRecoveryDefersARunThatOnlyJustStarted(t *testing.T) {
	eng, s, queue := newRequeueTestEngine(t)
	eng.RecoveryMinRunAge = defaultRecoveryMinRunAge // the shipped guard, not a test value
	seedRequeuePipeline(t, s, "p-young", models.NodeTypeTransform)
	run := seedInterruptedRun(t, s, "p-young", "run-young", "source")

	summary, err := eng.RecoverNonTerminalRuns()
	if err != nil {
		t.Fatalf("RecoverNonTerminalRuns: %v", err)
	}
	if summary.RunsDeferred != 1 || summary.RunsRequeued != 0 || summary.RunsFailed != 0 {
		t.Fatalf("deferred=%d requeued=%d failed=%d, want 1/0/0",
			summary.RunsDeferred, summary.RunsRequeued, summary.RunsFailed)
	}
	if _, queued := queue.job(run.ID); queued {
		t.Error("a live run was handed to a second executor via the job queue")
	}
	got, err := s.GetRun(run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Status != models.RunStatusRunning {
		t.Errorf("run status = %s, want it left running", got.Status)
	}
	// A deferred run must not accumulate recovery bookkeeping on every
	// sweep: the guards run before the recovery_started event.
	events, err := s.ListEventsByRun(run.ID)
	if err != nil {
		t.Fatalf("ListEventsByRun: %v", err)
	}
	for _, ev := range events {
		if ev.EventType == models.RunEventRecoveryStarted {
			t.Errorf("a deferred young run was given a %s event", ev.EventType)
		}
	}
}

// The guard must not swallow real recovery: the same fixture, older than
// the guard, still behaves exactly as it did before this change.
func TestRecoveryStillRequeuesAnOldEnoughRun(t *testing.T) {
	eng, s, queue := newRequeueTestEngine(t)
	eng.RecoveryMinRunAge = defaultRecoveryMinRunAge
	seedRequeuePipeline(t, s, "p-old", models.NodeTypeTransform)
	run := seedInterruptedRun(t, s, "p-old", "run-old", "source")
	startedAgo(t, s, run, 2*defaultRecoveryMinRunAge)

	summary, err := eng.RecoverNonTerminalRuns()
	if err != nil {
		t.Fatalf("RecoverNonTerminalRuns: %v", err)
	}
	if summary.RunsRequeued != 1 {
		t.Fatalf("requeued=%d deferred=%d, want the old run requeued as before",
			summary.RunsRequeued, summary.RunsDeferred)
	}
	if _, queued := queue.job(run.ID); !queued {
		t.Error("the old run should still have been put back on the queue")
	}
}

// A run with no StartedAt has not been claimed by anyone, so the age
// guard deliberately does not apply: deferring on a timestamp that may
// never arrive would leave it non-terminal forever.
func TestRecoveryAgeGuardDoesNotApplyWithoutAStartTime(t *testing.T) {
	eng, s, _ := newRequeueTestEngine(t)
	eng.RecoveryMinRunAge = defaultRecoveryMinRunAge
	seedRequeuePipeline(t, s, "p-nostart", models.NodeTypeTransform)
	run := seedInterruptedRun(t, s, "p-nostart", "run-nostart", "source")
	run.StartedAt = nil
	if err := s.UpdateRun(run); err != nil {
		t.Fatalf("UpdateRun: %v", err)
	}

	summary, err := eng.RecoverNonTerminalRuns()
	if err != nil {
		t.Fatalf("RecoverNonTerminalRuns: %v", err)
	}
	if summary.RunsDeferred != 0 {
		t.Fatalf("deferred=%d, want the age guard not to apply without a start time", summary.RunsDeferred)
	}
}

// The structural half: an owner this process cannot see. Old enough that
// the age guard is out of the way, so only the hook can defer it.
func TestRecoveryDefersWhenAnExternalClaimIsHeld(t *testing.T) {
	eng, s, queue := newRequeueTestEngine(t)
	eng.RecoveryMinRunAge = defaultRecoveryMinRunAge
	seedRequeuePipeline(t, s, "p-claimed", models.NodeTypeTransform)
	run := seedInterruptedRun(t, s, "p-claimed", "run-claimed", "source")
	startedAgo(t, s, run, 2*defaultRecoveryMinRunAge)

	var asked []string
	eng.ExternalRunClaim = func(runID string) bool {
		asked = append(asked, runID)
		return true
	}

	summary, err := eng.RecoverNonTerminalRuns()
	if err != nil {
		t.Fatalf("RecoverNonTerminalRuns: %v", err)
	}
	if summary.RunsDeferred != 1 || summary.RunsRequeued != 0 {
		t.Fatalf("deferred=%d requeued=%d, want the claimed run deferred",
			summary.RunsDeferred, summary.RunsRequeued)
	}
	if _, queued := queue.job(run.ID); queued {
		t.Error("a run somebody else owns was put back on the queue")
	}
	if len(asked) != 1 || asked[0] != run.ID {
		t.Errorf("hook consulted with %v, want exactly [%s]", asked, run.ID)
	}
}

// False means "no live owner known", which must leave every other check
// exactly as it was -- it is not an assertion that the run is dead.
func TestRecoveryProceedsWhenNoExternalClaimIsHeld(t *testing.T) {
	eng, s, queue := newRequeueTestEngine(t)
	eng.RecoveryMinRunAge = defaultRecoveryMinRunAge
	seedRequeuePipeline(t, s, "p-unclaimed", models.NodeTypeTransform)
	run := seedInterruptedRun(t, s, "p-unclaimed", "run-unclaimed", "source")
	startedAgo(t, s, run, 2*defaultRecoveryMinRunAge)
	eng.ExternalRunClaim = func(string) bool { return false }

	summary, err := eng.RecoverNonTerminalRuns()
	if err != nil {
		t.Fatalf("RecoverNonTerminalRuns: %v", err)
	}
	if summary.RunsRequeued != 1 {
		t.Fatalf("requeued=%d deferred=%d, want unchanged behaviour", summary.RunsRequeued, summary.RunsDeferred)
	}
	if _, queued := queue.job(run.ID); !queued {
		t.Error("want the run requeued when no external claim is held")
	}
}

// A nil hook is the OSS default and must behave exactly as before.
func TestRecoveryWithoutAnExternalClaimHookIsUnchanged(t *testing.T) {
	eng, s, queue := newRequeueTestEngine(t)
	eng.RecoveryMinRunAge = defaultRecoveryMinRunAge
	if eng.ExternalRunClaim != nil {
		t.Fatal("ExternalRunClaim should be nil by default")
	}
	seedRequeuePipeline(t, s, "p-nohook", models.NodeTypeTransform)
	run := seedInterruptedRun(t, s, "p-nohook", "run-nohook", "source")
	startedAgo(t, s, run, 2*defaultRecoveryMinRunAge)

	summary, err := eng.RecoverNonTerminalRuns()
	if err != nil {
		t.Fatalf("RecoverNonTerminalRuns: %v", err)
	}
	if _, queued := queue.job(run.ID); summary.RunsRequeued != 1 || !queued {
		t.Fatalf("requeued=%d queued=%v, want 1/true", summary.RunsRequeued, queued)
	}
}

// A hook that panics must not take down the sweep, and must fail closed:
// the run is left alone rather than handed to a second executor.
func TestRecoveryPanickingExternalClaimDefersAndSweepSurvives(t *testing.T) {
	eng, s, queue := newRequeueTestEngine(t)
	eng.RecoveryMinRunAge = defaultRecoveryMinRunAge
	seedRequeuePipeline(t, s, "p-panic", models.NodeTypeTransform)
	run := seedInterruptedRun(t, s, "p-panic", "run-panic", "source")
	startedAgo(t, s, run, 2*defaultRecoveryMinRunAge)
	eng.ExternalRunClaim = func(string) bool { panic("claim backend is down") }

	summary, err := eng.RecoverNonTerminalRuns()
	if err != nil {
		t.Fatalf("the sweep must survive a panicking hook, got: %v", err)
	}
	if summary.RunsDeferred != 1 || summary.RunsRequeued != 0 {
		t.Fatalf("deferred=%d requeued=%d, want the run deferred when the hook is broken",
			summary.RunsDeferred, summary.RunsRequeued)
	}
	if _, queued := queue.job(run.ID); queued {
		t.Error("a broken hook must cost recovery, not correctness: the run was requeued")
	}
	if got, _ := s.GetRun(run.ID); got != nil && got.Status != models.RunStatusRunning {
		t.Errorf("run status = %s, want it left running", got.Status)
	}
}

// The age guard is only meaningful if it outlasts the sweep that would
// otherwise adopt a starting run. Narrowing it below one sweep interval
// reintroduces the double-execution this file documents.
func TestRecoveryMinRunAgeOutlastsTheSweep(t *testing.T) {
	if defaultRecoveryMinRunAge <= reclaimSweepInterval {
		t.Fatalf("defaultRecoveryMinRunAge = %s, want > reclaimSweepInterval (%s): a run must survive the sweep that catches it mid-start",
			defaultRecoveryMinRunAge, reclaimSweepInterval)
	}
}
