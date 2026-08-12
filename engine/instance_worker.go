package engine

import (
	"fmt"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/store"
)

// ExecuteInstanceWorkOrder runs a WorkOrder's actual work — the worker-side
// half of ADR-017's remote-instance dispatch. The dispatcher side is
// engine/expansion.go's dispatchExpansionInstanceRemotely; this is the
// shared primitive every dispatch-surface consumer of a WorkOrder-bearing
// job calls to actually do the work, so "how do I run one of these" is
// answered once, not once per transport. Today's only wired-in caller is
// ExecuteInstanceJob below (cmd/serve.go's Redis-JobQueue worker loop); an
// enterprise WorkPool worker reaching this same function (rather than
// re-implementing it) is the natural next caller.
//
// Only NodeTypeCode is dispatched remotely today (dynamic-expansion items
// — see dispatchExpansionInstanceRemotely's own scope). Anything else is a
// named, explicit failure rather than a silent mishandling, so a future
// WorkOrder producer that starts dispatching a different node type finds
// out immediately rather than getting a wrong result.
func ExecuteInstanceWorkOrder(wo *extensions.InstanceWorkOrder) (*common.DataSet, error) {
	if wo == nil {
		return nil, fmt.Errorf("execute instance work order: nil work order")
	}
	if wo.NodeType != string(models.NodeTypeCode) {
		return nil, fmt.Errorf("execute instance work order: unsupported node type %q (only %q dispatches remotely today)", wo.NodeType, models.NodeTypeCode)
	}

	itemDS := &common.DataSet{Columns: wo.ItemColumns}
	if wo.ItemRow != nil {
		itemDS.Rows = []common.DataRow{common.DataRow(wo.ItemRow)}
	}
	result, _, err := ExecuteCodeNode(wo.Script, itemDS, wo.Config, wo.RunParams, wo.TimeoutSeconds)
	// wo's stderr is deliberately dropped here, not logged: this function
	// has no Runner (and so no run-scoped log sink) to attribute it to —
	// see ExecuteInstanceJob below, which is the layer that actually knows
	// which run/node/instance this belongs to.
	return result, err
}

// ExecuteInstanceJob is the full worker-side handling of a WorkOrder-
// bearing job for a worker sharing the SAME store as the dispatcher — the
// case for e.g. a Redis-JobQueue worker in cmd/serve.go's RunMode=="worker"
// mode, which reads and writes the same database the dispatching engine
// does. It executes the work order, then settles the claim directly via
// CompleteAttempt/FailAttempt and writes the result through ArtifactStore
// — exactly the two actions the enterprise instance-result HTTP endpoint
// performs for a worker that does NOT share the database
// (HandleReportInstanceResult), just reached without an HTTP hop since
// none is needed here.
//
// A script/instance failure is not a job-queue-level failure: the job's
// own task — determine and durably record this instance's outcome — still
// succeeded, matching how the whole-pipeline case already Acks a job whose
// run failed (see the worker loop below). Only an infrastructure failure
// on this worker's own side (can't reach the store) is worth surfacing as
// a job failure, so the caller can Fail the job and let another worker's
// redelivery retry it — the underlying lease stays valid throughout
// (dispatchExpansionInstanceRemotely's own renewal goroutine keeps
// renewing it independently of job-queue redelivery), so a retry with the
// same FencingGeneration the job already carries is safe and meaningful,
// not a stale token.
func ExecuteInstanceJob(s store.Store, artifacts ArtifactStore, job extensions.RunJob) error {
	if job.WorkOrder == nil {
		return fmt.Errorf("execute instance job: job %s has no work order", job.ID)
	}
	attemptStore, ok := s.(store.ExecutionAttemptStore)
	if !ok {
		return fmt.Errorf("execute instance job: store does not support execution attempts")
	}

	result, execErr := ExecuteInstanceWorkOrder(job.WorkOrder)
	if execErr != nil {
		if failErr := attemptStore.FailAttempt(job.RunID, job.NodeID, job.InstanceKey, job.Attempt, job.FencingGeneration, execErr.Error()); failErr != nil {
			return fmt.Errorf("execute instance job: instance failed (%v), and settling that failure also failed: %w", execErr, failErr)
		}
		return nil
	}

	if artifacts == nil {
		reason := "execute instance job: no artifact store configured on this worker"
		if failErr := attemptStore.FailAttempt(job.RunID, job.NodeID, job.InstanceKey, job.Attempt, job.FencingGeneration, reason); failErr != nil {
			return fmt.Errorf("%s, and settling that failure also failed: %w", reason, failErr)
		}
		return fmt.Errorf("%s", reason)
	}
	if writeErr := artifacts.WriteArtifact(job.RunID, job.NodeID, job.InstanceKey, result); writeErr != nil {
		reason := fmt.Sprintf("failed to persist instance result: %v", writeErr)
		if failErr := attemptStore.FailAttempt(job.RunID, job.NodeID, job.InstanceKey, job.Attempt, job.FencingGeneration, reason); failErr != nil {
			return fmt.Errorf("execute instance job: %s, and settling that failure also failed: %w", reason, failErr)
		}
		return fmt.Errorf("execute instance job: %s", reason)
	}
	if err := attemptStore.CompleteAttempt(job.RunID, job.NodeID, job.InstanceKey, job.Attempt, job.FencingGeneration); err != nil {
		return fmt.Errorf("execute instance job: failed to settle completed instance: %w", err)
	}
	return nil
}
