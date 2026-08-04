package engine

import (
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// recoveryBatchSize bounds how many non-terminal runs RecoverNonTerminalRuns
// loads into memory per store.Store.ListNonTerminalRuns page. Recovery runs
// once, at startup, off the request path, so batch size only trades memory
// for round-trips — this is a fixed default rather than a tunable, matching
// how e.g. maxParallel is a fixed constant in runner.go.
const recoveryBatchSize = 50

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
func (e *Engine) RecoverNonTerminalRuns() (*RecoverySummary, error) {
	summary := &RecoverySummary{}
	attemptStore, _ := e.store.(store.ExecutionAttemptStore)

	afterID := ""
	for {
		runs, hasNext, err := e.store.ListNonTerminalRuns(afterID, recoveryBatchSize)
		if err != nil {
			return summary, fmt.Errorf("list non-terminal runs: %w", err)
		}
		if len(runs) == 0 {
			break
		}
		for i := range runs {
			run := &runs[i]
			summary.RunsScanned++
			outcome, reclaimed, err := e.recoverRun(run, attemptStore)
			summary.AttemptsReclaimed += reclaimed
			if reclaimed > 0 {
				atomic.AddInt64(&e.AttemptsReclaimed, int64(reclaimed))
			}
			if err != nil {
				log.Printf("recovery: run %s: %v", run.ID, err)
			}
			switch outcome {
			case recoveryOutcomeReconciled:
				summary.RunsReconciled++
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

	log.Printf("recovery: found %d non-terminal run(s) at startup, reconciled %d, failed %d, deferred %d (%d expired attempt lease(s) reclaimed)",
		summary.RunsScanned, summary.RunsReconciled, summary.RunsFailed, summary.RunsDeferred, summary.AttemptsReclaimed)
	return summary, nil
}

// recoverRun examines one non-terminal run and decides its fate. Returns
// the outcome and how many expired execution-attempt leases it reclaimed
// along the way (reclaiming happens independently of the run-level
// decision — even a deferred run may have had other, already-expired
// attempts settled).
func (e *Engine) recoverRun(run *models.Run, attemptStore store.ExecutionAttemptStore) (recoveryOutcome, int, error) {
	e.appendEvent(&models.RunEvent{RunID: run.ID, EventType: models.RunEventRecoveryStarted})

	reclaimed := 0
	if attemptStore != nil {
		liveLease, n, err := reconcileExecutionAttempts(attemptStore, run.ID)
		reclaimed = n
		if err != nil {
			return recoveryOutcomeDeferred, reclaimed, fmt.Errorf("reconcile execution attempts: %w", err)
		}
		if liveLease {
			log.Printf("recovery: deferring run %s — an execution attempt still holds a live lease, may still be owned by another process", run.ID)
			return recoveryOutcomeDeferred, reclaimed, nil
		}
	}

	events, err := e.store.ListEventsByRun(run.ID)
	if err != nil {
		return recoveryOutcomeDeferred, reclaimed, fmt.Errorf("list events: %w", err)
	}
	projected := ProjectRun(run.ID, events)

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
		if err := e.store.UpdateRun(projected); err != nil {
			return recoveryOutcomeDeferred, reclaimed, fmt.Errorf("sync stale snapshot to already-terminal projection: %w", err)
		}
		e.appendEvent(&models.RunEvent{RunID: run.ID, EventType: models.RunEventRecoveryCompleted})
		atomic.AddInt64(&e.RunsRecovered, 1)
		log.Printf("recovery: run %s snapshot was stale (already %s in its event log) — resynced", run.ID, projected.Status)
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
		log.Printf("recovery: leaving run %s pending — never claimed, awaiting job-queue redelivery", run.ID)
		return recoveryOutcomeDeferred, reclaimed, nil
	}

	pipe, err := e.resolvePipelineForRun(run.PipelineID, run.PipelineVersion)
	if err != nil {
		outcome, ferr := e.markRecoveryFailed(run, fmt.Sprintf("cannot resolve pipeline definition (version %d): %v", run.PipelineVersion, err))
		return outcome, reclaimed, ferr
	}

	latest := latestNodeOutcome(projected.NodeRuns)
	var failedNode *models.NodeRun
	var ambiguousNode string    // stuck mid-attempt: genuinely unknown whether its side effect completed
	var neverStartedNode string // no record at all: consistent with a normal fail-fast abort, or a run that never got anywhere
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
		case models.RunStatusSuccess:
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
		var reason string
		if ambiguousNode != "" {
			reason = fmt.Sprintf("run was interrupted mid-execution and cannot be safely resumed from process-local state (%s)", ambiguousNode)
		} else {
			reason = fmt.Sprintf("run was interrupted mid-execution and cannot be safely resumed from process-local state (%s never started)", neverStartedNode)
		}
		outcome, ferr := e.markRecoveryFailed(run, reason)
		return outcome, reclaimed, ferr
	}
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
		if err := attemptStore.FailAttempt(a.RunID, a.NodeID, a.Attempt, a.FencingGeneration, reason); err != nil {
			log.Printf("recovery: reclaim expired lease for run %s node %q attempt %d: %v", a.RunID, a.NodeID, a.Attempt, err)
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
	log.Printf("recovery: run %s reconciled from event log to %s", run.ID, status)
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
	log.Printf("recovery: run %s has no recoverable path (%s) — marked failed", run.ID, reason)
	return recoveryOutcomeFailed, nil
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
