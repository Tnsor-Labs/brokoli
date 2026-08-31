package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/fetchers"
	"github.com/Tnsor-Labs/brokoli/store"
)

// ExecuteInstanceWorkOrder runs a WorkOrder's actual work — the worker-side
// half of ADR-017's remote-instance dispatch. The dispatcher side is
// engine/expansion.go's dispatchExpansionInstanceRemotely; this is the
// shared primitive every dispatch-surface consumer of a WorkOrder-bearing
// job calls to actually do the work, so "how do I run one of these" is
// answered once, not once per transport. ExecuteInstanceJob below and the
// Enterprise WorkPool worker both reach this same function rather than
// re-implementing execution per transport.
//
// Code expansion items and source_api pagination pages are dispatched
// remotely today. Anything else is a named, explicit failure rather than a
// silent mishandling, so a future WorkOrder producer that starts dispatching a
// different node type finds out immediately rather than getting a wrong
// result.
func ExecuteInstanceWorkOrder(wo *extensions.InstanceWorkOrder) (*common.DataSet, error) {
	return ExecuteInstanceWorkOrderContext(context.Background(), wo)
}

// ExecuteInstanceWorkOrderContext runs a WorkOrder until it completes or the
// caller cancels it. Code nodes propagate the context to their subprocess;
// other node types still observe cancellation before and after their fetch.
func ExecuteInstanceWorkOrderContext(ctx context.Context, wo *extensions.InstanceWorkOrder) (*common.DataSet, error) {
	if wo == nil {
		return nil, fmt.Errorf("execute instance work order: nil work order")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch wo.NodeType {
	case string(models.NodeTypeCode):
		return executeCodeWorkOrder(ctx, wo)
	case string(models.NodeTypeSourceAPI):
		result, err := executeSourceAPIPageWorkOrder(ctx, wo)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return result, err
	default:
		return nil, fmt.Errorf("execute instance work order: unsupported node type %q", wo.NodeType)
	}
}

func executeCodeWorkOrder(ctx context.Context, wo *extensions.InstanceWorkOrder) (*common.DataSet, error) {
	itemDS := &common.DataSet{Columns: wo.ItemColumns}
	if wo.ItemRow != nil {
		itemDS.Rows = []common.DataRow{common.DataRow(wo.ItemRow)}
	}
	if digest, isBundle, tbErr := taskBundleReference(wo.Config); tbErr != nil {
		return nil, fmt.Errorf("execute code instance work order: %w", tbErr)
	} else if isBundle {
		return nil, fmt.Errorf("execute code instance work order: task_bundle code nodes (%s) are not dispatched to remote instance workers in this version — the remote-instance path runs bare-script code nodes only", digest)
	}
	result, _, err := ExecuteCodeNodeContext(ctx, wo.Script, itemDS, wo.Config, wo.RunParams, wo.TimeoutSeconds)
	// wo's stderr is deliberately dropped here, not logged: this function
	// has no Runner (and so no run-scoped log sink) to attribute it to —
	// see ExecuteInstanceJob below, which is the layer that actually knows
	// which run/node/instance this belongs to.
	return result, err
}

func executeSourceAPIPageWorkOrder(ctx context.Context, wo *extensions.InstanceWorkOrder) (*common.DataSet, error) {
	if wo.SourceURL == "" {
		return nil, fmt.Errorf("execute source_api page work order: source URL is required")
	}
	sourceType := wo.SourceType
	if sourceType == "" {
		sourceType = "rest"
	}
	fetcher, err := fetchers.GetFetcher(sourceType)
	if err != nil {
		return nil, fmt.Errorf("execute source_api page work order: get fetcher: %w", err)
	}
	pageFetcher, ok := fetcher.(fetchers.PageFetcher)
	if !ok {
		return nil, fmt.Errorf("execute source_api page work order: source type %q does not support page execution", sourceType)
	}
	if cancellable, ok := pageFetcher.(fetchers.ContextPageFetcher); ok {
		return cancellable.FetchPageContext(ctx, wo.SourceURL, wo.Config, wo.PageURL, wo.PageParams)
	}
	return pageFetcher.FetchPage(wo.SourceURL, wo.Config, wo.PageURL, wo.PageParams)
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if cancelled, err := runCancellationRequested(s, job.RunID); err != nil {
		return fmt.Errorf("execute instance job: check run cancellation: %w", err)
	} else if cancelled {
		cancel()
	}
	go watchRunCancellation(ctx, cancel, s, job.RunID)

	return executeInstanceJobContext(ctx, s, artifacts, job)
}

func executeInstanceJobContext(ctx context.Context, s store.Store, artifacts ArtifactStore, job extensions.RunJob) error {
	if job.WorkOrder == nil {
		return fmt.Errorf("execute instance job: job %s has no work order", job.ID)
	}
	attemptStore, ok := s.(store.ExecutionAttemptStore)
	if !ok {
		return fmt.Errorf("execute instance job: store does not support execution attempts")
	}

	result, execErr := ExecuteInstanceWorkOrderContext(ctx, job.WorkOrder)
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
	// Fenced when the store supports it (SQLArtifactStore — the store
	// distributed instance dispatch actually runs with): a fencing
	// generation at least as high already owning this key means a retry
	// already completed while this attempt was still in flight, and this
	// write must not clobber that result. See FencedArtifactWriter's own
	// doc comment.
	if fenced, ok := artifacts.(FencedArtifactWriter); ok {
		written, writeErr := fenced.WriteArtifactFenced(job.RunID, job.NodeID, job.InstanceKey, result, job.Attempt, job.FencingGeneration)
		if writeErr != nil {
			reason := fmt.Sprintf("failed to persist instance result: %v", writeErr)
			if failErr := attemptStore.FailAttempt(job.RunID, job.NodeID, job.InstanceKey, job.Attempt, job.FencingGeneration, reason); failErr != nil {
				return fmt.Errorf("execute instance job: %s, and settling that failure also failed: %w", reason, failErr)
			}
			return fmt.Errorf("execute instance job: %s", reason)
		}
		if !written {
			// Lost the race to a newer attempt. Not this call's failure to
			// report — there is nothing left to settle.
			return nil
		}
	} else if writeErr := artifacts.WriteArtifact(job.RunID, job.NodeID, job.InstanceKey, result); writeErr != nil {
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

func runCancellationRequested(s store.Store, runID string) (bool, error) {
	run, err := s.GetRun(runID)
	if err != nil {
		return false, err
	}
	return run.CancelRequested || run.Status == models.RunStatusCancelled, nil
}

func watchRunCancellation(ctx context.Context, cancel context.CancelFunc, s store.Store, runID string) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cancelled, err := runCancellationRequested(s, runID)
			if err == nil && cancelled {
				cancel()
				return
			}
		}
	}
}
