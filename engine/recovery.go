package engine

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/tracing"
	"github.com/Tnsor-Labs/brokoli/store"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// recoveryBatchSize bounds how many non-terminal runs RecoverNonTerminalRuns
// loads into memory per store.Store.ListNonTerminalRuns page. Recovery runs
// once, at startup, off the request path, so batch size only trades memory
// for round-trips — this is a fixed default rather than a tunable, matching
// how e.g. maxParallel is a fixed constant in runner.go.
const recoveryBatchSize = 50

// recoveryTransitionGracePeriod guards a real gap in the liveLease check:
// reconcileExecutionAttempts only sees non-terminal execution_attempts rows,
// so the instant between one node going terminal (success/failed) and the
// Runner claiming the next node's attempt has no lease at all — not because
// the run is orphaned, but because nothing has asked for one yet. Before
// #167 added a periodic reclaim sweep, RecoverNonTerminalRuns only ran once
// per process at startup, so this window was effectively never hit in
// practice (a fresh process's own startup pass doesn't race its own
// in-flight runs). A sweep ticking every reclaimSweepInterval hits it
// routinely under real concurrent load — confirmed live: a run whose
// compute_totals node had just finished successfully was marked failed
// ("aggregate_by_region never started") while the owning process was very
// much alive and claimed aggregate_by_region six seconds later, orphaning
// that node run forever (its own eventual Complete call fails its fencing
// check against the run recovery already closed out).
//
// Node-to-node transition overhead is milliseconds in the healthy case, but
// "healthy" assumes an uncontended pod — found live re-testing this ADR's
// own admission-control fixes together with elastic worker scaling
// (EE-ADR-008): under real multi-tenant CPU contention (several concurrent
// runs sharing one worker pod, exactly what admission control lets through
// and what autoscaling exists to absorb), a single node's own execution can
// legitimately take several times longer than uncontended — one run's
// gen_orders took 21.5s where a quiet pod sees single digits — pushing the
// gap between two nodes past the original 10s grace period and reproducing
// the identical false-reclaim race a second time. 10s was sized off the
// "milliseconds, not seconds" uncontended case; it was never going to be
// enough once the exact scenario admission control is meant to make safe
// (several jobs genuinely running concurrently on one pod) actually
// happened.
//
// Widened to match reclaimSweepInterval: treating any run with events
// younger than one full sweep interval as possibly still live costs true
// orphan detection at most one extra sweep cycle — the same negligible
// cost the original 10s already accepted, just measured against a bound
// that reflects real contested-pod behavior instead of an idealized quiet
// one. Only gates the "no recoverable path" branch in recoverRun: a
// definite success or an explicit failed-node outcome is unambiguous
// regardless of timing and does not need this check.
//
// defaultRecoveryTransitionGracePeriod is the value every Engine starts
// with. The effective value lives on the Engine
// (Engine.RecoveryTransitionGracePeriod) rather than in a package
// variable: tests need to override it to a near-zero value to assert the
// "genuinely orphaned, fail now" path without a multi-second sleep, and a
// package-level var made that override process-wide — so no test touching
// it could ever run in parallel with another, and one test's override
// could be observed by an unrelated one. Per-engine state has neither
// problem (Tnsor-Labs/brokoli#264, #329).
const defaultRecoveryTransitionGracePeriod = 20 * time.Second

// recoveryOutcome classifies what RecoverNonTerminalRuns did with one
// non-terminal run, for RecoverySummary's counters.
type recoveryOutcome int

const (
	// recoveryOutcomeDeferred means the run was left exactly as found: an
	// execution attempt belonging to it still holds a live, unexpired
	// lease, so another process may still be actively executing it, and
	// recovery must not steal that work (issue #9's explicit requirement).
	recoveryOutcomeDeferred recoveryOutcome = iota
	// recoveryOutcomeReconciled means recovery determined the run's true
	// outcome from its event log (success or failure, matching what the
	// live process would have persisted) and finalized it.
	recoveryOutcomeReconciled
	// recoveryOutcomeRequeued means the run was interrupted before
	// anything could have been written outside Brokoli, so it went back on
	// the job queue rather than being failed (Tnsor-Labs/brokoli#289).
	recoveryOutcomeRequeued
	// recoveryOutcomeFailed means the run had no recoverable path — its
	// event log is incomplete or ambiguous — so recovery forced it to
	// failed with a specific reason rather than leaving it stuck.
	recoveryOutcomeFailed
)

// RecoverySummary aggregates the outcome of one RecoverNonTerminalRuns pass,
// both for the startup log line (operator diagnostics required by issue #9)
// and for tests.
type RecoverySummary struct {
	RunsScanned       int // total non-terminal runs examined
	RunsReconciled    int // resolved to a terminal state matching what the live runner would have persisted
	RunsFailed        int // forced to failed: no recoverable path existed
	RunsRequeued      int // put back on the queue: interrupted before anything could have been written
	RunsDeferred      int // left non-terminal: a live-looking lease means another process may still own the work
	AttemptsReclaimed int // execution_attempts rows whose expired lease was settled to failed
}

// RecoverNonTerminalRuns pages through every run left in a non-terminal
// status (pending/running) by store.Store.ListNonTerminalRuns, rebuilds
// each run's true state from its durable event log (ProjectRun) rather
// than trusting the runs/node_runs snapshot rows directly — those are
// exactly what can go stale when the process that owned a run is killed —
// and reconciles it. See recoverRun for the per-run decision tree.
//
// Call this once at startup, after store.NewStore/engine.NewEngine are
// constructed and before engine.Scheduler.Start or any worker loop begins
// accepting new work (see cmd/serve.go) — recovering a run concurrently
// with new dispatch could race a freshly created run into being observed
// mid-creation and misdiagnosed as orphaned.
//
// It is safe to call repeatedly (idempotent): once a run is reconciled to a
// terminal status, ListNonTerminalRuns no longer returns it on the next
// call, so re-running recovery after a second restart only examines
// whatever is still genuinely non-terminal (freshly created runs, or runs
// recovery previously and correctly deferred because a lease still looked
// live at the time).
func (e *Engine) RecoverNonTerminalRuns() (summary *RecoverySummary, err error) {
	summary = &RecoverySummary{}
	attemptStore, _ := e.store.(store.ExecutionAttemptStore)

	_, span := tracing.Tracer().Start(context.Background(), "brokoli.recovery")
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		} else {
			span.SetStatus(codes.Ok, "")
		}
		span.End()
	}()

	afterID := ""
	for {
		runs, hasNext, listErr := e.store.ListNonTerminalRuns(afterID, recoveryBatchSize)
		if listErr != nil {
			err = fmt.Errorf("list non-terminal runs: %w", listErr)
			return summary, err
		}
		if len(runs) == 0 {
			break
		}
		for i := range runs {
			run := &runs[i]
			summary.RunsScanned++
			outcome, reclaimed, runErr := e.recoverRun(run, attemptStore)
			summary.AttemptsReclaimed += reclaimed
			if reclaimed > 0 {
				atomic.AddInt64(&e.AttemptsReclaimed, int64(reclaimed))
			}
			if runErr != nil {
				common.SLog().Warn("recovery: run reconciliation error", common.RunAttr(run.ID), "error", runErr)
			}
			switch outcome {
			case recoveryOutcomeReconciled:
				summary.RunsReconciled++
			case recoveryOutcomeRequeued:
				summary.RunsRequeued++
			case recoveryOutcomeFailed:
				summary.RunsFailed++
			case recoveryOutcomeDeferred:
				summary.RunsDeferred++
			}
			afterID = run.ID
		}
		if !hasNext {
			break
		}
	}

	span.SetAttributes(
		attribute.Int("runs_scanned", summary.RunsScanned),
		attribute.Int("runs_reconciled", summary.RunsReconciled),
		attribute.Int("runs_failed", summary.RunsFailed),
		attribute.Int("runs_deferred", summary.RunsDeferred),
		attribute.Int("attempts_reclaimed", summary.AttemptsReclaimed),
	)
	// Info when there's something to report; Debug for an empty pass — this
	// function is called once at startup (always worth an Info line, even
	// with nothing found) and, as of the scheduler leader's periodic
	// reclaim sweep, roughly every reclaimSweepInterval thereafter. An
	// empty steady-state pass logging at Info every 20s would bury the
	// startup line's own signal in noise; RunsScanned > 0 is exactly the
	// cases an operator needs to see regardless of which caller this is.
	logArgs := []any{
		"runs_scanned", summary.RunsScanned, "runs_reconciled", summary.RunsReconciled,
		"runs_failed", summary.RunsFailed, "runs_deferred", summary.RunsDeferred,
		"attempts_reclaimed", summary.AttemptsReclaimed,
	}
	if summary.RunsScanned > 0 {
		common.SLog().Info("recovery: pass complete", logArgs...)
	} else {
		common.SLog().Debug("recovery: pass complete", logArgs...)
	}
	return summary, nil
}

// recoverRun examines one non-terminal run and decides its fate. Returns
// the outcome and how many expired execution-attempt leases it reclaimed
// along the way (reclaiming happens independently of the run-level
// decision — even a deferred run may have had other, already-expired
// attempts settled).
func (e *Engine) recoverRun(run *models.Run, attemptStore store.ExecutionAttemptStore) (outcome recoveryOutcome, reclaimed int, err error) {
	runCtx, span := tracing.Tracer().Start(context.Background(), "brokoli.recovery.run",
		trace.WithAttributes(attribute.String("run_id", run.ID)))
	defer func() {
		span.SetAttributes(attribute.Int("outcome", int(outcome)))
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		} else {
			span.SetStatus(codes.Ok, "")
		}
		span.End()
	}()
	_ = runCtx // no further nested spans in this pass; kept for future propagation

	e.appendEvent(&models.RunEvent{RunID: run.ID, EventType: models.RunEventRecoveryStarted})

	if attemptStore != nil {
		liveLease, n, aErr := reconcileExecutionAttempts(attemptStore, run.ID)
		reclaimed = n
		if aErr != nil {
			err = fmt.Errorf("reconcile execution attempts: %w", aErr)
			return recoveryOutcomeDeferred, reclaimed, err
		}
		if liveLease {
			common.SLog().Info("recovery: deferring run — live lease held, may still be owned by another process",
				common.RunAttr(run.ID))
			return recoveryOutcomeDeferred, reclaimed, nil
		}
	}

	events, eErr := e.store.ListEventsByRun(run.ID)
	if eErr != nil {
		err = fmt.Errorf("list events: %w", eErr)
		return recoveryOutcomeDeferred, reclaimed, err
	}
	projected := ProjectRun(run.ID, events)
	atomic.AddInt64(&e.EventsReplayed, int64(len(events)))

	if isTerminalRunStatus(projected.Status) {
		// Defensive: current write paths always persist the runs/node_runs
		// snapshot before appending the corresponding terminal event, so
		// this should not be reachable today — but issue #9 is explicit
		// that recovery must rebuild from the event log rather than trust
		// that invariant blindly, since it's exactly what can go stale in
		// the crash case this whole feature targets. If it ever does
		// happen, just sync the stale snapshot to the already-terminal
		// projection; don't re-append a RunEventTerminal that logically
		// already exists.
		if uErr := e.store.UpdateRun(projected); uErr != nil {
			err = fmt.Errorf("sync stale snapshot to already-terminal projection: %w", uErr)
			return recoveryOutcomeDeferred, reclaimed, err
		}
		e.appendEvent(&models.RunEvent{RunID: run.ID, EventType: models.RunEventRecoveryCompleted})
		atomic.AddInt64(&e.RunsRecovered, 1)
		common.SLog().Info("recovery: run snapshot was stale — resynced from event log",
			common.RunAttr(run.ID), "status", projected.Status)
		return recoveryOutcomeReconciled, reclaimed, nil
	}

	if projected.Status == models.RunStatusPending {
		// A pending run was never claimed — no Runner ever started it, so
		// there is no process-local execution state to have lost, and no
		// node ever failed or ran. This is not an orphaned run; it is
		// legitimately still awaiting a worker to dequeue and claim it
		// (Engine.ExecuteQueuedRun's ClaimPendingRun CAS). Redelivering
		// that dispatch is the job queue's responsibility, not recovery's
		// — treating "no node has started yet" as "no recoverable path"
		// would wrongly fail every queued-but-not-yet-claimed run on every
		// restart. Leave it exactly as found.
		common.SLog().Info("recovery: leaving run pending — never claimed, awaiting job-queue redelivery",
			common.RunAttr(run.ID))
		return recoveryOutcomeDeferred, reclaimed, nil
	}

	pipe, err := e.resolvePipelineForRun(run.PipelineID, run.PipelineVersion)
	if err != nil {
		outcome, ferr := e.markRecoveryFailed(run, fmt.Sprintf("cannot resolve pipeline definition (version %d): %v", run.PipelineVersion, err))
		return outcome, reclaimed, ferr
	}

	latest := latestNodeOutcome(projected.NodeRuns)
	var failedNode *models.NodeRun
	var ambiguousNode string             // stuck mid-attempt: genuinely unknown whether its side effect completed
	var ambiguousNodeRun *models.NodeRun // the dangling NodeRun row behind ambiguousNode, so it can be closed out below
	var neverStartedNode string          // no record at all: consistent with a normal fail-fast abort, or a run that never got anywhere
	allSuccess := true
	for _, n := range pipe.Nodes {
		nr, ok := latest[n.ID]
		if !ok {
			allSuccess = false
			if neverStartedNode == "" {
				neverStartedNode = n.ID
			}
			continue
		}
		switch nr.Status {
		case models.RunStatusSuccess, models.RunStatusSkipped:
			// fine
		case models.RunStatusFailed:
			allSuccess = false
			if failedNode == nil {
				fn := nr
				failedNode = &fn
			}
		default:
			// Still "running" (or any other non-terminal node status) in
			// the projection: the process died while this node's attempt
			// was in flight. Its side effect may or may not have completed
			// — there is no way to tell from the durable record alone, so
			// this can never be folded into "success" or "failed", only
			// "no recoverable path". Unlike a node that simply never
			// started, this ALWAYS blocks a definite determination, even if
			// another node also failed outright.
			allSuccess = false
			if ambiguousNode == "" {
				ambiguousNode = n.ID + " (attempt in progress when the process died)"
				anr := nr
				ambiguousNodeRun = &anr
			}
		}
	}

	switch {
	case allSuccess:
		outcome, ferr := e.finalizeRecoveredRun(run, models.RunStatusSuccess, "")
		return outcome, reclaimed, ferr
	case ambiguousNode == "" && failedNode != nil:
		// A definite node failure explains why any later-wave nodes never
		// started (Runner.Execute aborts the whole run on the first node
		// error within a wave) — that's the normal, expected shape of a
		// fail-fast abort, not ambiguity, so a never-started downstream
		// node does not block resolving this as the same failed outcome
		// Runner.failRun would have persisted.
		outcome, ferr := e.finalizeRecoveredRun(run, models.RunStatusFailed, failedNode.Error)
		return outcome, reclaimed, ferr
	default:
		if lastActivity, ok := lastGenuineActivity(events); ok {
			if time.Since(lastActivity) < e.RecoveryTransitionGracePeriod {
				common.SLog().Info("recovery: deferring run — last activity too recent to rule out a live in-process node transition",
					common.RunAttr(run.ID), "since_last_event", time.Since(lastActivity))
				return recoveryOutcomeDeferred, reclaimed, nil
			}
		}
		var reason string
		if ambiguousNode != "" {
			reason = fmt.Sprintf("run was interrupted mid-execution and cannot be safely resumed from process-local state (%s)", ambiguousNode)
		} else {
			reason = fmt.Sprintf("run was interrupted mid-execution and cannot be safely resumed from process-local state (%s never started)", neverStartedNode)
		}

		// #289: an interrupted run that provably wrote nothing goes back on
		// the queue instead of being failed. Checked before the dangling
		// node run is closed out, because a re-queued run's history should
		// not carry a node closed as failed on a run that is about to
		// execute again.
		if requeued, rErr := e.tryRequeueInterruptedRun(run, pipe, latest, events, reason); rErr != nil {
			return recoveryOutcomeDeferred, reclaimed, rErr
		} else if requeued {
			return recoveryOutcomeRequeued, reclaimed, nil
		}

		if ambiguousNodeRun != nil {
			e.closeDanglingNodeRun(ambiguousNodeRun, reason)
		}
		outcome, ferr := e.markRecoveryFailed(run, reason)
		return outcome, reclaimed, ferr
	}
}

// lastGenuineActivity returns the CreatedAt of the most recent event that
// reflects actual execution progress, skipping recovery's own bookkeeping
// events. recoverRun unconditionally appends RunEventRecoveryStarted at the
// top of every call, before this check ever runs — including on a pass that
// only ends up deferring — so without this filter, that just-appended event
// would always be the newest one, making every run that ever reaches the
// grace-period check look "active" on every single sweep pass forever, and
// a genuinely orphaned run would never be reclaimed. Found live: 4 runs a
// dead worker had left mid-flight sat "running" with since_last_event under
// 5ms on every 20s sweep tick for minutes straight.
func lastGenuineActivity(events []models.RunEvent) (time.Time, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		switch events[i].EventType {
		case models.RunEventRecoveryStarted, models.RunEventRecoveryCompleted, models.RunEventRecoveryFailed:
			continue
		default:
			return events[i].CreatedAt, true
		}
	}
	return time.Time{}, false
}

// reconcileExecutionAttempts inspects every execution_attempts row for a
// run. It reports liveLease=true if any non-terminal attempt still holds an
// unexpired lease (recovery must defer to whatever process holds it,
// rather than assume the run is orphaned); any non-terminal attempt whose
// lease HAS expired is reclaimed by settling it to failed via FailAttempt
// (fencing-checked, so a genuine concurrent claim by a live worker always
// wins over this best-effort cleanup). An attempt that was never claimed at
// all (no lease, still queued) is left untouched — that is the normal
// "awaiting worker dequeue" state, not evidence of a crash, and its
// redelivery is the job queue's responsibility, not recovery's.
func reconcileExecutionAttempts(attemptStore store.ExecutionAttemptStore, runID string) (liveLease bool, reclaimed int, err error) {
	attempts, err := attemptStore.ListExecutionAttemptsByRun(runID)
	if err != nil {
		return false, 0, err
	}
	now := time.Now().UTC()
	var expired []models.ExecutionAttempt
	for _, a := range attempts {
		if isTerminalAttemptStatus(a.Status) {
			continue
		}
		if a.LeaseExpiresAt == nil {
			continue // never claimed — leave for the job queue to redeliver
		}
		if a.LeaseExpiresAt.After(now) {
			liveLease = true
			continue
		}
		expired = append(expired, a)
	}
	if liveLease {
		// Don't reclaim anything else on this pass either: a live lease on
		// ANY attempt of this run means recovery cannot be sure it is safe
		// to touch attempt state that belongs to the same in-flight run —
		// simplest and safest is to leave the whole run alone this boot.
		return true, 0, nil
	}
	for _, a := range expired {
		reason := "startup recovery: lease expired, reclaiming"
		if err := attemptStore.FailAttempt(a.RunID, a.NodeID, a.InstanceKey, a.Attempt, a.FencingGeneration, reason); err != nil {
			common.SLog().Warn("recovery: reclaim expired lease failed",
				common.RunAttr(a.RunID), common.NodeAttr(a.NodeID), common.AttemptAttr(a.Attempt), "error", err)
			continue
		}
		reclaimed++
	}
	return false, reclaimed, nil
}

// finalizeRecoveredRun persists a definite outcome recovery derived from
// the event log — the run's terminal event and status are exactly what
// Runner.Execute would itself have appended had it survived to finish, so
// this uses the ordinary RunEventTerminal type (paired with an informational
// RunEventRecoveryCompleted marking that recovery, not the live runner, is
// what produced it) rather than RunEventRecoveryFailed.
func (e *Engine) finalizeRecoveredRun(run *models.Run, status models.RunStatus, errMsg string) (recoveryOutcome, error) {
	now := time.Now().UTC()
	run.Status = status
	run.FinishedAt = &now
	run.Error = errMsg
	if err := e.store.UpdateRun(run); err != nil {
		return recoveryOutcomeDeferred, fmt.Errorf("persist recovered run: %w", err)
	}
	e.appendEvent(&models.RunEvent{
		RunID:     run.ID,
		EventType: models.RunEventTerminal,
		Payload: models.RunEventPayload{
			Status:     status,
			FinishedAt: run.FinishedAt,
			Error:      errMsg,
		},
	})
	e.appendEvent(&models.RunEvent{RunID: run.ID, EventType: models.RunEventRecoveryCompleted})
	atomic.AddInt64(&e.RunsRecovered, 1)
	if status == models.RunStatusFailed {
		atomic.AddInt64(&e.RunsFailed, 1)
	} else {
		atomic.AddInt64(&e.RunsSucceeded, 1)
	}
	common.SLog().Info("recovery: run reconciled from event log", common.RunAttr(run.ID), "status", status)
	return recoveryOutcomeReconciled, nil
}

// markRecoveryFailed forces a non-terminal run to failed because it has no
// recoverable path — its event log cannot confirm what the live process
// would have persisted. Unlike finalizeRecoveredRun, this is recorded with
// the dedicated RunEventRecoveryFailed event type (Tnsor-Labs/brokoli#9),
// distinguishing "recovery invented this outcome because it had no choice"
// from "recovery discovered exactly what the live process would have
// persisted" in the audit trail.
func (e *Engine) markRecoveryFailed(run *models.Run, reason string) (recoveryOutcome, error) {
	// A run with durable cancel intent (models.Run.CancelRequested) whose
	// event log has no recoverable path is the crash-during-cancel case:
	// the user asked for cancellation, the owning process died before
	// finalizing, and recovery is now inventing the outcome. Honor the
	// recorded intent — close it out as cancelled, not failed, so a
	// cancel is never rewritten into a failure by the recovery path the
	// same way #203 stopped the live path from doing it. Runs whose log
	// PROVES a terminal outcome never reach here (finalizeRecoveredRun
	// reconciles those): work that genuinely completed before the crash
	// stays completed even if a cancel arrived too late.
	if run.CancelRequested {
		now := time.Now().UTC()
		errMsg := "recovery: cancellation was requested before the owning process died (" + reason + ")"
		run.Status = models.RunStatusCancelled
		run.FinishedAt = &now
		run.Error = errMsg
		if err := e.store.UpdateRun(run); err != nil {
			return recoveryOutcomeDeferred, fmt.Errorf("persist recovery-cancelled run: %w", err)
		}
		e.appendEvent(&models.RunEvent{
			RunID:     run.ID,
			EventType: models.RunEventCancelled,
			Payload: models.RunEventPayload{
				Status:     models.RunStatusCancelled,
				FinishedAt: run.FinishedAt,
				Error:      errMsg,
			},
		})
		e.appendEvent(&models.RunEvent{RunID: run.ID, EventType: models.RunEventRecoveryCompleted})
		atomic.AddInt64(&e.RunsRecovered, 1)
		common.SLog().Info("recovery: run closed out as cancelled per durable cancel intent", common.RunAttr(run.ID), "reason", reason)
		return recoveryOutcomeReconciled, nil
	}

	now := time.Now().UTC()
	errMsg := "recovery: " + reason
	run.Status = models.RunStatusFailed
	run.FinishedAt = &now
	run.Error = errMsg
	if err := e.store.UpdateRun(run); err != nil {
		return recoveryOutcomeDeferred, fmt.Errorf("persist recovery-failed run: %w", err)
	}
	e.appendEvent(&models.RunEvent{
		RunID:     run.ID,
		EventType: models.RunEventRecoveryFailed,
		Payload: models.RunEventPayload{
			Status:     models.RunStatusFailed,
			FinishedAt: run.FinishedAt,
			Error:      errMsg,
		},
	})
	atomic.AddInt64(&e.RunsRecoveryFailed, 1)
	atomic.AddInt64(&e.RunsFailed, 1)
	common.SLog().Error("recovery: run has no recoverable path — marked failed", common.RunAttr(run.ID), "reason", reason)
	return recoveryOutcomeFailed, nil
}

// closeDanglingNodeRun settles the specific NodeRun a run-level recovery
// failure was blamed on, so it does not sit at status "running" forever on
// a run whose own top-level status is now terminal. markRecoveryFailed only
// ever touched the run row and appended a run-level event — the node's own
// snapshot row (and its event history, which ProjectRun rebuilds state
// from) was left exactly as the dead process last wrote it. Found live:
// runs correctly marked failed by recovery still showed a node stuck at
// "running" in the API/UI indefinitely. Mirrors the normal AttemptFailed
// path in runner.go's executeNode, so a future ProjectRun replay of this
// run sees a consistent, fully-terminal history rather than rediscovering
// the same ambiguity. Best-effort: a failure here is logged, not fatal —
// the run's own terminal status (set right after by markRecoveryFailed)
// is what actually matters for "is this run done."
func (e *Engine) closeDanglingNodeRun(nr *models.NodeRun, reason string) {
	nr.Status = models.RunStatusFailed
	nr.Error = reason
	if err := e.store.UpdateNodeRun(nr); err != nil {
		common.SLog().Warn("recovery: failed to close dangling node run", common.RunAttr(nr.RunID), common.NodeAttr(nr.NodeID), "error", err)
	}
	attempt := nr.Attempt
	e.appendEvent(&models.RunEvent{
		RunID:     nr.RunID,
		NodeID:    nr.NodeID,
		Attempt:   &attempt,
		EventType: models.AttemptFailed,
		Payload: models.RunEventPayload{
			Status:     models.RunStatusFailed,
			NodeRunID:  nr.ID,
			DurationMs: nr.DurationMs,
			Error:      reason,
		},
	})
}

// latestNodeOutcome collapses projected node-run events (one entry per
// (node, attempt) pair, see ProjectRun) down to the highest-attempt entry
// per node — the same "only the last attempt counts" rule
// models.Run.PopulateError already applies for surfacing a run's top-level
// error from retried node attempts.
func latestNodeOutcome(nodeRuns []models.NodeRun) map[string]models.NodeRun {
	latest := make(map[string]models.NodeRun, len(nodeRuns))
	for _, nr := range nodeRuns {
		if existing, ok := latest[nr.NodeID]; !ok || nr.Attempt > existing.Attempt {
			latest[nr.NodeID] = nr
		}
	}
	return latest
}

func isTerminalAttemptStatus(s models.AttemptStatus) bool {
	return s == models.AttemptStatusCompleted || s == models.AttemptStatusFailed
}
