package engine

import (
	"time"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/store"
)

// Pending-run redispatch sweep (Tnsor-Labs/brokoli#216).
//
// RunPipelineAsync's outbox transaction guarantees that by the time a run
// is accepted, a durable execution_attempts record exists proving dispatch
// was intended — "making the run reconcilable on restart", as its comment
// promises. This sweep is the consumer that promise was waiting for: until
// it existed, nothing ever read the outbox after the fact, so a dispatch
// lost AFTER a successful enqueue — a Redis restart wiping a non-persistent
// queue (live-verified: 5 queued runs stranded pending forever), or a
// message dropped for any other reason — had no recovery path. Startup
// recovery deliberately leaves pending runs alone ("redelivery is the job
// queue's responsibility"), which is correct for a healthy queue and
// meaningless for a wiped one.
//
// The sweep is safe to run on every instance concurrently, without leader
// gating, because every step is idempotent: JobQueue.Enqueue is idempotent
// by job ID (re-enqueuing an identical job must not create another pending
// delivery — the interface contract), and even a genuinely duplicate
// delivery is settled harmlessly by ExecuteQueuedRun's ClaimPendingRun
// compare-and-swap. The grace window keeps the sweep from churning on
// runs that are simply waiting their turn in a healthy backlog — where
// re-enqueueing would be a dedup no-op anyway.
const (
	DefaultPendingRedispatchInterval = 60 * time.Second
	DefaultPendingRedispatchGrace    = 2 * time.Minute
)

// StartPendingRedispatchSweep launches the background sweep. Call it after
// JobQueue is wired; it is a no-op on engines without a queue (in-process
// mode runs everything directly and has no dispatch to lose).
func (e *Engine) StartPendingRedispatchSweep(interval, grace time.Duration) {
	if e.JobQueue == nil {
		return
	}
	e.goBG(func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-e.shutdown:
				return
			case <-ticker.C:
				n, err := e.redispatchPendingOnce(grace)
				if err != nil {
					common.SLog().Warn("pending-run redispatch sweep failed", "error", err)
					continue
				}
				if n > 0 {
					common.SLog().Info("pending-run redispatch sweep re-enqueued lost dispatches", "count", n)
				}
			}
		}
	})
}

// redispatchPendingOnce re-enqueues every pending run whose committed
// outbox intent is older than grace, returning how many it enqueued.
func (e *Engine) redispatchPendingOnce(grace time.Duration) (int, error) {
	attemptStore, ok := e.store.(store.ExecutionAttemptStore)
	if !ok {
		// Without an outbox there is no durable proof a pending run was
		// ever dispatched (versus e.g. mid-creation), so nothing safe to do.
		return 0, nil
	}
	redispatched := 0
	afterID := ""
	for {
		runs, hasNext, err := e.store.ListNonTerminalRuns(afterID, recoveryBatchSize)
		if err != nil {
			return redispatched, err
		}
		for i := range runs {
			run := &runs[i]
			if run.Status != models.RunStatusPending {
				continue
			}
			// The pipeline-level outbox row: node_id and instance_key
			// empty, attempt 0 — exactly what RunPipelineAsync commits.
			attempt, aErr := attemptStore.GetExecutionAttempt(run.ID, "", "", 0)
			if aErr != nil || attempt == nil {
				// No dispatch intent on record (in-process run mid-create,
				// or a pre-outbox row): not this sweep's to touch.
				continue
			}
			if attempt.Status != models.AttemptStatusQueued {
				continue
			}
			if time.Since(attempt.CreatedAt) < grace {
				continue
			}
			job := extensions.RunJob{
				ID:             run.ID,
				PipelineID:     run.PipelineID,
				RunID:          run.ID,
				OrgID:          run.OrgID,
				EnqueuedAt:     time.Now().UTC(),
				IdempotencyKey: run.ID,
			}
			if len(run.Params) > 0 {
				job.Params = run.Params
			}
			if enqErr := e.JobQueue.Enqueue(job); enqErr != nil {
				common.SLog().Warn("pending-run redispatch enqueue failed",
					common.RunAttr(run.ID), "error", enqErr)
				continue
			}
			redispatched++
		}
		if !hasNext || len(runs) == 0 {
			break
		}
		afterID = runs[len(runs)-1].ID
	}
	return redispatched, nil
}
