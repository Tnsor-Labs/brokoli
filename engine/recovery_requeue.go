package engine

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// Automatic re-queue of interrupted runs that provably wrote nothing
// (Tnsor-Labs/brokoli#289).
//
// When a worker dies or is evicted mid-run, recovery cannot tell whether a
// node's side effect completed, so it fails the run rather than risk
// duplicating a write. That is the right default and it stays: re-running a
// pipeline whose sink already appended half its rows would double those
// rows, and no amount of convenience is worth that.
//
// But it is the right default for the wrong population. Worker autoscaling
// evicts pods, node drains happen, and rolling deploys are routine, so an
// interrupted run is now an ordinary event rather than an exceptional one --
// and a great many of them are interrupted before reaching anything that
// writes. Those can be re-queued with no risk at all, because there is
// nothing to duplicate.
//
// The test is not "did a sink run" but "could anything with a durable record
// have written outside Brokoli". That distinction matters: a node stuck
// mid-attempt is exactly the ambiguous case, so if such a node COULD write,
// the run still fails. Only when every node that got as far as having a
// record is provably incapable of writing does the run go back on the queue.

// requeueSafeNodeTypes are the node types that cannot write anything outside
// Brokoli, so re-running them from the beginning cannot duplicate a side
// effect.
//
// It is a whitelist, and unknown types -- including every plugin-provided
// node -- are deliberately absent: a node type this engine does not
// recognise gets no assumption made about it.
//
// The exclusions are as load-bearing as the inclusions:
//
//   - code runs arbitrary user code and can do anything at all.
//   - source_api looks like a read but its HTTP method is configurable
//     (pkg/fetchers/rest_fetcher.go), so a "source" can POST.
//   - migrate, dbt and notify all write by design, whatever their
//     capability tag says -- dbt builds models, notify sends a message
//     someone receives, and neither can be taken back.
//   - every sink_*, for the obvious reason.
//
// source_db is included, which is worth justifying because it executes SQL
// the user wrote and nothing forces that SQL to be a SELECT. The engine
// already treats these queries as freely re-runnable: pushdown planning
// executes them speculatively before the run (describeQueryColumns wraps
// the query in a zero-row probe), and ADR-023's composition re-executes
// them as part of a larger statement. A source_db query that mutated would
// already be unsafe under that machinery, so this adds no new assumption.
var requeueSafeNodeTypes = map[models.NodeType]bool{
	models.NodeTypeSourceFile: true,
	models.NodeTypeSourceDB:   true,

	models.NodeTypeTransform:     true,
	models.NodeTypeQualityCheck:  true,
	models.NodeTypeSQLGenerate:   true,
	models.NodeTypeJoin:          true,
	models.NodeTypeCondition:     true,
	models.NodeTypeUnion:         true,
	models.NodeTypeDatasetMap:    true,
	models.NodeTypeDatasetFilter: true,
}

// nodeCannotWriteExternally reports whether re-running this node from the
// start is guaranteed not to repeat a side effect outside Brokoli.
//
// A node carrying an explicit sink capability is refused regardless of its
// type: the SDK can declare capabilities on a node, and a declared sink is
// a stronger statement about intent than the type name.
func nodeCannotWriteExternally(n models.Node) bool {
	for _, c := range n.Capabilities {
		if c == models.CapabilitySink {
			return false
		}
	}
	return requeueSafeNodeTypes[n.Type]
}

// interruptedRunIsSafeToRequeue reports whether an interrupted run can go
// back on the queue instead of being failed, and when it cannot, why.
//
// Only nodes with a durable record are considered. A node that never
// started cannot have written anything, so a pipeline whose sink was never
// reached is safe even though the sink is obviously capable of writing --
// that is the whole point, and it is the common shape of an eviction.
func interruptedRunIsSafeToRequeue(pipe *models.Pipeline, latest map[string]models.NodeRun) (bool, string) {
	byID := make(map[string]models.Node, len(pipe.Nodes))
	for _, n := range pipe.Nodes {
		byID[n.ID] = n
	}
	for nodeID, nr := range latest {
		// Skipped nodes did not execute, so they cannot have written.
		if nr.Status == models.RunStatusSkipped {
			continue
		}
		n, known := byID[nodeID]
		if !known {
			// A record for a node the current pipeline version does not
			// contain. Nothing can be proven about what it did.
			return false, fmt.Sprintf("node %q ran but is not in this pipeline version, so what it wrote cannot be determined", nodeID)
		}
		if !nodeCannotWriteExternally(n) {
			return false, fmt.Sprintf("node %q (%s) may have written outside Brokoli, so re-running could duplicate it", nodeID, n.Type)
		}
	}
	return true, ""
}

// countPriorRequeues returns how many times recovery has already put this
// run back on the queue.
//
// A run that is interrupted, re-queued, and interrupted again is not
// obviously making progress -- it may be hitting an eviction loop, or a node
// that reliably outlives its worker. Bounding the count means such a run
// ends up failed and visible rather than cycling silently forever, which is
// the failure mode this feature could otherwise introduce.
func countPriorRequeues(events []models.RunEvent) int {
	n := 0
	for _, e := range events {
		if e.EventType == models.RunEventRecoveryRequeued {
			n++
		}
	}
	return n
}

// maxRecoveryRequeues bounds how many times one run may be automatically
// re-queued before recovery gives up and fails it. Three is chosen to
// survive an ordinary rolling deploy (which can evict the same run more
// than once) while still terminating quickly on a run that cannot get
// through.
const maxRecoveryRequeues = 3

// tryRequeueInterruptedRun puts an interrupted run back on the job queue
// when nothing it touched could have written outside Brokoli. It reports
// whether it did; a false return means the caller should fail the run as
// before, and the reason is logged rather than returned because every
// refusal leaves the existing failure message intact.
func (e *Engine) tryRequeueInterruptedRun(
	run *models.Run,
	pipe *models.Pipeline,
	latest map[string]models.NodeRun,
	events []models.RunEvent,
	interruptReason string,
) (bool, error) {
	// A run nobody can dispatch must not be parked as pending: without a
	// queue there is nothing to pick it up again, and "pending forever" is
	// strictly worse than "failed, visible, and re-runnable by hand".
	if e.JobQueue == nil {
		return false, nil
	}
	// Durable cancel intent outranks this entirely. The user asked for the
	// run to stop; putting it back on the queue would restart work they
	// cancelled. markRecoveryFailed honours the intent by closing the run
	// out as cancelled.
	if run.CancelRequested {
		return false, nil
	}
	if n := countPriorRequeues(events); n >= maxRecoveryRequeues {
		common.SLog().Warn("recovery: not re-queueing an interrupted run again — it has already been re-queued the maximum number of times",
			common.RunAttr(run.ID), "requeues", n, "max", maxRecoveryRequeues)
		return false, nil
	}
	safe, why := interruptedRunIsSafeToRequeue(pipe, latest)
	if !safe {
		common.SLog().Info("recovery: interrupted run is not safe to re-queue — failing it",
			common.RunAttr(run.ID), "reason", why)
		return false, nil
	}

	msg := "recovery: " + interruptReason + "; nothing it reached could have written outside Brokoli, so it was put back on the queue"

	run.Status = models.RunStatusPending
	run.StartedAt = nil
	run.FinishedAt = nil
	run.Error = msg
	if err := e.store.UpdateRun(run); err != nil {
		return false, fmt.Errorf("persist re-queued run: %w", err)
	}
	e.appendEvent(&models.RunEvent{
		RunID:     run.ID,
		EventType: models.RunEventRecoveryRequeued,
		Payload: models.RunEventPayload{
			Status: models.RunStatusPending,
			Error:  msg,
		},
	})

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
	if err := e.JobQueue.Enqueue(job); err != nil {
		// The run is already pending with the event recorded, so the
		// pending-run redispatch sweep (#216) is the backstop: it
		// re-enqueues pending runs whose delivery was lost, which is
		// exactly what this is. Not an error for recovery to return.
		common.SLog().Warn("recovery: re-queued run could not be enqueued — leaving it to the redispatch sweep",
			common.RunAttr(run.ID), "error", err)
	}

	atomic.AddInt64(&e.RunsRecoveryRequeued, 1)
	common.SLog().Info("recovery: interrupted run put back on the queue — nothing it reached could have written",
		common.RunAttr(run.ID), "reason", interruptReason)
	return true, nil
}
