package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/fetchers"
	"github.com/Tnsor-Labs/brokoli/pkg/secrets"
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
	return ExecuteInstanceWorkOrderResolving(ctx, wo, nil)
}

// ExecuteInstanceWorkOrderResolving is ExecuteInstanceWorkOrderContext on a
// worker that can resolve connections. A source_api page names its
// connection by conn_id and carries none of its credentials (#753), so the
// worker resolves it here, with cr, in the work order's workspace. A page
// that names a connection fails on a worker with no resolver, rather than
// fetching without the credentials.
func ExecuteInstanceWorkOrderResolving(ctx context.Context, wo *extensions.InstanceWorkOrder, cr *ConnectionResolver) (*common.DataSet, error) {
	if wo == nil {
		return nil, fmt.Errorf("execute instance work order: nil work order")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch wo.NodeType {
	case string(models.NodeTypeCode):
		return executeCodeWorkOrder(ctx, wo)
	case string(models.NodeTypeMigrate):
		return executeMigrateWorkOrder(ctx, wo, cr)
	case string(models.NodeTypeSourceAPI):
		result, err := executeSourceAPIPageWorkOrder(ctx, wo, cr)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return result, err
	default:
		return nil, fmt.Errorf("execute instance work order: unsupported node type %q", wo.NodeType)
	}
}

func executeMigrateWorkOrder(ctx context.Context, wo *extensions.InstanceWorkOrder, cr *ConnectionResolver) (*common.DataSet, error) {
	if wo.Migrate == nil {
		return nil, fmt.Errorf("execute migrate work order: migrate payload is required")
	}
	m := wo.Migrate
	sourceURI, err := resolveMigrateWorkOrderURI(cr, wo.WorkspaceID, wo.RunID, m.SourceConnID, m.SourceURI, models.NodeTypeSourceDB)
	if err != nil {
		return nil, fmt.Errorf("source: %w", err)
	}
	destURI, err := resolveMigrateWorkOrderURI(cr, wo.WorkspaceID, wo.RunID, m.DestConnID, m.DestURI, models.NodeTypeSinkDB)
	if err != nil {
		return nil, fmt.Errorf("destination: %w", err)
	}
	partition, ranges, err := parseMigratePartitionConfig(m.Partition)
	if err != nil {
		return nil, err
	}
	if m.PartitionIndex < 0 || m.PartitionIndex >= len(ranges) {
		return nil, fmt.Errorf("partition index %d is out of range", m.PartitionIndex)
	}
	dialect := m.Dialect
	if dialect == "" {
		dialect = dialectForURI(sourceURI)
	}
	query, args, err := migratePartitionQuery(m.SourceQuery, dialect, partition, ranges[m.PartitionIndex])
	if err != nil {
		return nil, err
	}
	config := map[string]interface{}{
		"source_uri":          sourceURI,
		"dest_uri":            destURI,
		"source_query":        query,
		"_partition_args":     args,
		"dest_table":          m.DestTable,
		"dialect":             dialect,
		"mode":                m.Mode,
		"key_columns":         m.KeyColumns,
		"chunk_size":          m.ChunkSize,
		"create_table":        false,
		"_partition_disjoint": true,
	}
	runner := &Runner{ctx: ctx}
	return runner.runMigrateSingle(models.Node{ID: wo.NodeType, Type: models.NodeTypeMigrate, Config: config}, 0)
}

func resolveMigrateWorkOrderURI(cr *ConnectionResolver, workspaceID, runID, connID, directURI string, nodeType models.NodeType) (string, error) {
	if connID == "" {
		if directURI == "" {
			return "", fmt.Errorf("connection reference is required")
		}
		return directURI, nil
	}
	if cr == nil {
		return "", fmt.Errorf("connection resolver is required for %q", connID)
	}
	config, warnings, err := cr.ResolveWithWarningsScoped(map[string]interface{}{"conn_id": connID}, nodeType, secrets.Scope{WorkspaceID: workspaceID, RunID: "workorder:" + runID})
	if err != nil {
		return "", err
	}
	if len(warnings) > 0 {
		return "", fmt.Errorf("%s", strings.Join(warnings, "; "))
	}
	uri, _ := config["uri"].(string)
	if uri == "" {
		return "", fmt.Errorf("connection %q did not resolve to a URI", connID)
	}
	return uri, nil
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
	result, _, err := ExecuteCodeNodeContext(ctx, wo.Script, itemDS, wo.Config, wo.RunParams, wo.TypedParams, wo.TimeoutSeconds)
	// wo's stderr is deliberately dropped here, not logged: this function
	// has no Runner (and so no run-scoped log sink) to attribute it to —
	// see ExecuteInstanceJob below, which is the layer that actually knows
	// which run/node/instance this belongs to.
	return result, err
}

func executeSourceAPIPageWorkOrder(ctx context.Context, wo *extensions.InstanceWorkOrder, cr *ConnectionResolver) (ds *common.DataSet, err error) {
	// The page resolves its connection here, outside any run's runner, and
	// its error is settled into the store from here (#782). So it gets a
	// redaction set of its own, keyed by this call rather than the run --
	// several pages of one run can be in flight on this worker -- and
	// masks its error before returning it.
	scope := secrets.Scope{WorkspaceID: wo.WorkspaceID, RunID: "workorder:" + common.NewID()}
	defer dropRunRedactions(scope.RunID)
	defer func() {
		if err != nil {
			if masked := redactRun(scope.RunID, err.Error()); masked != err.Error() {
				err = errors.New(masked)
			}
		}
	}()
	source, config := wo.SourceURL, wo.Config
	if connID, _ := config["conn_id"].(string); connID != "" {
		if cr == nil {
			return nil, fmt.Errorf("execute source_api page work order: the page uses connection %q, and this worker has no connection resolver to fetch its credentials with", connID)
		}
		resolved, warnings, err := cr.ResolveWithWarningsScoped(config, models.NodeTypeSourceAPI, scope)
		if err != nil {
			return nil, fmt.Errorf("execute source_api page work order: %w", err)
		}
		// A warning here means the connection was not resolved at all
		// (missing, another workspace's, or a store that cannot look it
		// up). The page would go out with no base URL and no credentials.
		if len(warnings) > 0 {
			return nil, fmt.Errorf("execute source_api page work order: %s", strings.Join(warnings, "; "))
		}
		config = resolved
		source, _ = resolved["url"].(string)
	}
	if source == "" {
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
		return cancellable.FetchPageContext(ctx, source, config, wo.PageURL, wo.PageParams)
	}
	return pageFetcher.FetchPage(source, config, wo.PageURL, wo.PageParams)
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
	return ExecuteInstanceJobResolving(s, artifacts, nil, job)
}

// ExecuteInstanceJobResolving is ExecuteInstanceJob on a worker that can
// resolve connections with cr; see ExecuteInstanceWorkOrderResolving.
func ExecuteInstanceJobResolving(s store.Store, artifacts ArtifactStore, cr *ConnectionResolver, job extensions.RunJob) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if cancelled, err := runCancellationRequested(s, job.RunID); err != nil {
		return fmt.Errorf("execute instance job: check run cancellation: %w", err)
	} else if cancelled {
		cancel()
	}
	go watchRunCancellation(ctx, cancel, s, job.RunID)

	return executeInstanceJobContext(ctx, s, artifacts, cr, job)
}

func executeInstanceJobContext(ctx context.Context, s store.Store, artifacts ArtifactStore, cr *ConnectionResolver, job extensions.RunJob) error {
	if job.WorkOrder == nil {
		return fmt.Errorf("execute instance job: job %s has no work order", job.ID)
	}
	attemptStore, ok := s.(store.ExecutionAttemptStore)
	if !ok {
		return fmt.Errorf("execute instance job: store does not support execution attempts")
	}

	// Task nodes are the one node type whose work order can't be executed
	// self-containedly (ExecuteInstanceWorkOrderContext has no store to
	// fetch a bundle with) -- routed here, before that function, to the
	// store-aware executor instead. See ExecuteTaskWorkOrderContext's own
	// doc comment for why this isn't just a case inside
	// ExecuteInstanceWorkOrderContext itself.
	var result *common.DataSet
	var execErr error
	if job.WorkOrder.NodeType == string(models.NodeTypeTask) {
		result, execErr = ExecuteTaskWorkOrderWithArtifacts(ctx, s, artifacts, job.RunID, job.NodeID, job.WorkOrder)
	} else {
		result, execErr = ExecuteInstanceWorkOrderResolving(ctx, job.WorkOrder, cr)
	}
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
