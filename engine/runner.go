package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/tracing"
	"github.com/Tnsor-Labs/brokoli/store"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Runner executes a single pipeline run.
type Runner struct {
	store         store.Store
	eventCh       chan<- models.Event
	varStore      VariableStore       // for ${var.key} resolution
	connResolver  *ConnectionResolver // for conn_id → URI
	ctx           context.Context
	cancel        context.CancelFunc
	run           *models.Run
	pipe          *models.Pipeline
	skipNodes     map[string]bool
	dryRun        bool
	dryRunMaxRows int
	dryRunResults map[string]*DryRunNodeResult
	params        map[string]string // runtime params
	varCtx        *VariableContext
	preRunID      string                          // pre-generated run ID (for registration before Execute)
	acceptedRun   *models.Run                     // queued run already persisted and atomically claimed
	orgID         string                          // tenant isolation for WebSocket events
	alertRaised   bool                            // guards raiseFailureAlert against double-firing
	traceID       string                          // distributed tracing correlation ID
	executors     []extensions.NodeExecutor       // enterprise: external executors (K8s, Docker)
	notifier      extensions.NotificationProvider // enterprise: Slack, PagerDuty, etc.

	// pipelineVersion pins a freshly created run (acceptedRun == nil) to the
	// store.PipelineVersion snapshot it was resolved against — see
	// Engine.resolveRunPipelineVersion and models.Run.PipelineVersion.
	// Ignored when acceptedRun != nil; that run's PipelineVersion was
	// already set (and persisted) when it was accepted.
	pipelineVersion int

	// resumedFromRunID is set by Engine.ResumeRun to the run ID being
	// resumed. It serves two purposes: it becomes models.Run.ResumedFromRunID
	// on the new run (lineage), and it is the run ID Runner reads durable
	// artifacts from when restoring a skipped node's output — a skipped
	// node's artifact was written under the ORIGINAL run's ID, not this new
	// run's ID. Empty for a run that is not a resume.
	resumedFromRunID string

	// artifactStore persists/restores durable node-output artifacts. See
	// ArtifactStore and Runner.restoreSkippedNodeOutput. Nil disables both
	// writing artifacts on node success and restoring them on resume (any
	// resume attempt against a nil artifactStore that needs to restore a
	// node with downstream consumers fails loudly rather than silently
	// substituting empty data).
	artifactStore ArtifactStore

	// spillThreshold is the estimated encoded size at or above which a
	// node's output is written to the artifact store instead of being held
	// in memory for the rest of the run (Tnsor-Labs/brokoli#38). Zero uses
	// DefaultSpillThresholdBytes; negative disables spilling.
	spillThreshold int64

	// checkpointStore persists/restores mid-pagination progress for
	// source_api nodes using execution().checkpoint_every. See
	// PaginationCheckpointStore and runSourceAPI in node_handlers.go. Nil
	// disables checkpointing entirely — a node with checkpoint_every set
	// against a nil checkpointStore just runs the full paginated fetch
	// every attempt, exactly like before issue #41 M2 existed.
	checkpointStore PaginationCheckpointStore

	// parentCtx, when set, is the context Execute derives r.ctx from instead
	// of context.Background() — used to propagate an OpenTelemetry trace
	// context into the run so a run claimed via Engine.ExecuteQueuedRun
	// stays in the same trace as the claim span that preceded it
	// (Tnsor-Labs/brokoli#11). Nil for runs that start their own trace (the
	// common RunPipeline/RunPipelineAsync in-process case).
	parentCtx context.Context

	// metrics carries pointers to Engine's node-attempt/event-append
	// counters (Tnsor-Labs/brokoli#11). Nil-safe throughout — see
	// incrMetric — so a Runner without one (DryRun, or a test constructing
	// Runner directly) simply skips these increments.
	metrics *runnerMetrics
}

// NewRunner creates a runner for the given pipeline.
func NewRunner(s store.Store, eventCh chan<- models.Event, pipe *models.Pipeline, vs VariableStore, cr *ConnectionResolver, execs []extensions.NodeExecutor, notifier extensions.NotificationProvider) *Runner {
	return &Runner{
		varStore:     vs,
		connResolver: cr,
		executors:    execs,
		notifier:     notifier,
		store:        s,
		eventCh:      eventCh,
		pipe:         pipe,
	}
}

// Cancel stops a running pipeline.
func (r *Runner) Cancel() {
	if r.cancel != nil {
		r.cancel()
	}
}

// Execute runs the pipeline end-to-end.
func (r *Runner) Execute() (run *models.Run, err error) {
	base := context.Background()
	if r.parentCtx != nil {
		// Propagates the trace context from whatever preceded this Execute
		// call (e.g. Engine.ExecuteQueuedRun's claim span) so the whole run
		// lifecycle — claim through completion — is one OpenTelemetry trace
		// (Tnsor-Labs/brokoli#11) instead of an isolated one starting here.
		base = r.parentCtx
	}
	spanCtx, span := tracing.Tracer().Start(base, "brokoli.run.execute",
		trace.WithAttributes(attribute.String("pipeline_id", r.pipe.ID)))
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		} else {
			span.SetStatus(codes.Ok, "")
		}
		span.End()
	}()

	r.ctx, r.cancel = context.WithCancel(spanCtx)
	defer r.cancel()

	now := time.Now().UTC()
	if r.acceptedRun != nil {
		r.run = r.acceptedRun
		r.traceID = r.run.TraceID
		if r.run.StartedAt != nil {
			now = *r.run.StartedAt
		}
	} else {
		runID := r.preRunID
		if runID == "" {
			runID = common.NewID()
		}
		r.traceID = common.NewID()
		r.run = &models.Run{
			ID:               runID,
			PipelineID:       r.pipe.ID,
			Status:           models.RunStatusRunning,
			Params:           r.params,
			StartedAt:        &now,
			TraceID:          r.traceID,
			PipelineVersion:  r.pipelineVersion,
			ResumedFromRunID: r.resumedFromRunID,
			OrgID:            r.orgID,
		}
		crashAt(crashPointBeforeRunCreate, "")
		if err := r.store.CreateRun(r.run); err != nil {
			return nil, fmt.Errorf("create run: %w", err)
		}
		r.appendEvent(models.RunEvent{
			RunID:     r.run.ID,
			EventType: models.RunEventCreated,
			Payload: models.RunEventPayload{
				Status:           models.RunStatusRunning,
				PipelineID:       r.run.PipelineID,
				StartedAt:        r.run.StartedAt,
				TraceID:          r.run.TraceID,
				Params:           r.run.Params,
				PipelineVersion:  r.run.PipelineVersion,
				ResumedFromRunID: r.run.ResumedFromRunID,
			},
		})
		crashAt(crashPointAfterRunCreate, "")
	}
	span.SetAttributes(attribute.String("run_id", r.run.ID), attribute.String("trace_id", r.traceID))
	common.SLog().Info("run started",
		common.RunAttr(r.run.ID), common.PipelineAttr(r.pipe.ID), common.TraceAttr(r.traceID))

	r.emit(models.Event{Type: models.EventRunStarted, RunID: r.run.ID, PipelineID: r.pipe.ID})
	r.fireHook("on_start", nil)

	// Initialize variable context — merge pipeline default params with runtime params
	mergedParams := make(map[string]string)
	for k, v := range r.pipe.Params {
		mergedParams[k] = v
	}
	for k, v := range r.params {
		mergedParams[k] = v
	}
	r.varCtx = NewVariableContext(mergedParams, r.run.ID, now)
	r.varCtx.Vars = r.varStore // wire stored variables into resolver

	// Build dependency graph
	nodeMap := make(map[string]models.Node)
	inDegree := make(map[string]int)
	dependents := make(map[string][]string) // nodeID -> nodes that depend on it
	for _, n := range r.pipe.Nodes {
		nodeMap[n.ID] = n
		inDegree[n.ID] = 0
	}
	for _, e := range r.pipe.Edges {
		inDegree[e.To]++
		dependents[e.From] = append(dependents[e.From], e.To)
	}

	// Outputs map (thread-safe)
	// Node results for the rest of the run. Large outputs are written to
	// the artifact store and read back on demand rather than held for the
	// whole run — see nodeOutputs and Tnsor-Labs/brokoli#38.
	outputs := r.newOutputs()

	// Max parallelism semaphore (default 4, configurable later)
	maxParallel := 4
	sem := make(chan struct{}, maxParallel)

	// Execute in waves using Kahn's algorithm
	// Start with nodes that have no incoming edges
	remaining := make(map[string]int)
	for id, deg := range inDegree {
		remaining[id] = deg
	}

	var runErr error
	for {
		// Check if cancelled
		if r.ctx.Err() != nil {
			runErr = fmt.Errorf("pipeline cancelled")
			break
		}

		// Collect ready nodes (in-degree == 0 and not yet processed)
		var ready []models.Node
		for id, deg := range remaining {
			if deg == 0 {
				ready = append(ready, nodeMap[id])
			}
		}
		if len(ready) == 0 {
			break
		}

		// Remove ready nodes from remaining
		for _, n := range ready {
			delete(remaining, n.ID)
		}

		// Execute ready nodes in parallel
		var wg sync.WaitGroup
		errCh := make(chan error, len(ready))
		readyAt := time.Now().UTC() // all nodes in this wave became ready at this moment

		for _, node := range ready {
			wg.Add(1)
			sem <- struct{}{} // acquire semaphore
			go func(n models.Node) {
				defer wg.Done()
				defer func() { <-sem }() // release semaphore

				// Recover from panics — never let a node crash the server
				defer func() {
					if rec := recover(); rec != nil {
						r.log(n.ID, models.LogLevelError, "PANIC in node %s: %v", n.Name, rec)
						errCh <- fmt.Errorf("node %s panicked: %v", n.Name, rec)
					}
				}()

				// Check cancellation before starting node
				if r.ctx.Err() != nil {
					errCh <- fmt.Errorf("pipeline cancelled")
					return
				}

				if err := r.executeNode(n, outputs, readyAt); err != nil {
					errCh <- err
					return
				}
			}(node)
		}

		wg.Wait()
		close(errCh)

		// Check for errors
		for err := range errCh {
			if err != nil {
				runErr = err
				break
			}
		}
		if runErr != nil {
			return r.run, r.failRun(runErr)
		}

		// Decrement in-degree of dependents
		for _, n := range ready {
			for _, depID := range dependents[n.ID] {
				if _, ok := remaining[depID]; ok {
					remaining[depID]--
				}
			}
		}
	}

	finishTime := time.Now().UTC()

	// Check if already cancelled (by CancelRun)
	if r.ctx.Err() != nil {
		r.run.Status = models.RunStatusCancelled
		r.run.FinishedAt = &finishTime
		if err := r.store.UpdateRun(r.run); err != nil {
			return r.run, fmt.Errorf("persist cancelled run: %w", err)
		}
		r.appendEvent(models.RunEvent{
			RunID:     r.run.ID,
			EventType: models.RunEventCancelled,
			Payload: models.RunEventPayload{
				Status:     models.RunStatusCancelled,
				FinishedAt: r.run.FinishedAt,
				Error:      "cancelled",
			},
		})
		r.emit(models.Event{Type: models.EventRunFailed, RunID: r.run.ID, PipelineID: r.pipe.ID, Status: models.RunStatusCancelled, Error: "cancelled"})
		r.fireHook("on_failure", map[string]string{"error": "cancelled by user"})
		common.SLog().Info("run finished", common.RunAttr(r.run.ID), common.PipelineAttr(r.pipe.ID),
			"status", models.RunStatusCancelled)
		return r.run, fmt.Errorf("pipeline cancelled")
	}

	if runErr != nil {
		r.run.Status = models.RunStatusFailed
		r.run.FinishedAt = &finishTime
		if err := r.store.UpdateRun(r.run); err != nil {
			return r.run, fmt.Errorf("persist failed run: %w", err)
		}
		r.appendEvent(models.RunEvent{
			RunID:     r.run.ID,
			EventType: models.RunEventTerminal,
			Payload: models.RunEventPayload{
				Status:     models.RunStatusFailed,
				FinishedAt: r.run.FinishedAt,
				Error:      runErr.Error(),
			},
		})
		r.emit(models.Event{Type: models.EventRunFailed, RunID: r.run.ID, PipelineID: r.pipe.ID, Status: models.RunStatusFailed, Error: runErr.Error()})
		r.raiseFailureAlert(runErr)
		r.fireHook("on_failure", map[string]string{"error": runErr.Error()})
		r.sendNotification("run.failed", "critical", fmt.Sprintf("Pipeline \"%s\" failed", r.pipe.Name), runErr.Error())
		NotifyPipelineEvent(r.pipe, r.run, "run.failed", runErr.Error())
		common.SLog().Warn("run finished", common.RunAttr(r.run.ID), common.PipelineAttr(r.pipe.ID),
			"status", models.RunStatusFailed, "error", runErr)
		return r.run, runErr
	}

	r.run.Status = models.RunStatusSuccess
	r.run.FinishedAt = &finishTime
	crashAt(crashPointBeforeRunTerminalPersist, "")
	if err := r.store.UpdateRun(r.run); err != nil {
		return r.run, fmt.Errorf("persist successful run: %w", err)
	}
	r.appendEvent(models.RunEvent{
		RunID:     r.run.ID,
		EventType: models.RunEventTerminal,
		Payload: models.RunEventPayload{
			Status:     models.RunStatusSuccess,
			FinishedAt: r.run.FinishedAt,
		},
	})
	crashAt(crashPointAfterRunTerminalPersist, "")
	r.emit(models.Event{Type: models.EventRunCompleted, RunID: r.run.ID, PipelineID: r.pipe.ID, Status: models.RunStatusSuccess})
	r.fireHook("on_success", nil)
	r.sendNotification("run.completed", "info", fmt.Sprintf("Pipeline \"%s\" completed", r.pipe.Name), "Run finished successfully")
	NotifyPipelineEvent(r.pipe, r.run, "run.completed", "")
	common.SLog().Info("run finished", common.RunAttr(r.run.ID), common.PipelineAttr(r.pipe.ID),
		"status", models.RunStatusSuccess)
	return r.run, nil
}

func (r *Runner) executeNode(node models.Node, outputs *nodeOutputs, readyAt time.Time) error {
	// Skip nodes that already succeeded (resume mode). Restore the node's
	// real durable output into the outputs map so downstream nodes see the
	// original data — never leave it unpopulated, which previously caused
	// executeNode's nil-input fallback further down to silently substitute
	// an empty dataset (Tnsor-Labs/brokoli#8).
	if r.skipNodes != nil && r.skipNodes[node.ID] {
		ds, restored, err := r.restoreSkippedNodeOutput(node)
		if err != nil {
			return err
		}
		if restored {
			if err := outputs.Put(node.ID, ds); err != nil {
				r.log(node.ID, models.LogLevelWarning, "Could not spill restored output, keeping it in memory: %v", err)
			}
			rowCount := 0
			if ds != nil {
				rowCount = len(ds.Rows)
			}
			r.log(node.ID, models.LogLevelInfo, "Resumed node %s (already succeeded) — restored %d row(s) from durable artifact", node.Name, rowCount)
		} else {
			r.log(node.ID, models.LogLevelInfo, "Skipping node %s (already succeeded, no downstream consumers of its output)", node.Name)
		}
		return nil
	}

	// Resolve variables in node config
	if r.varCtx != nil && node.Config != nil {
		node.Config = r.varCtx.ResolveConfig(node.Config)
	}

	// Resolve connection (conn_id → URI/headers)
	if r.connResolver != nil && node.Config != nil {
		node.Config = r.connResolver.Resolve(node.Config, node.Type)
	}

	// Find input data from connected upstream nodes. nodeOutputs is
	// internally synchronized, so no lock is held here.
	var input *common.DataSet
	var allInputs []*common.DataSet
	// edgeInputsByFrom indexes the same upstream outputs by upstream node
	// ID, alongside input/allInputs above — needed by dynamic-expansion
	// nodes (#31) to resolve expansion.over[param], which names upstream
	// nodes by ID rather than relying on edge order the way allInputs does.
	edgeInputsByFrom := make(map[string]*common.DataSet)
	for _, edge := range r.pipe.Edges {
		if edge.To == node.ID {
			ds, ok, err := outputs.Get(edge.From)
			if err != nil {
				// The upstream output was spilled and cannot be read back.
				// Continuing would hand this node an empty input, which is
				// the silent data loss Tnsor-Labs/brokoli#8 fixed; fail
				// instead.
				return fmt.Errorf("resolve input from node %s: %w", edge.From, err)
			}
			if ok {
				if input == nil {
					input = ds
				}
				allInputs = append(allInputs, ds)
				edgeInputsByFrom[edge.From] = ds
			}
		}
	}

	// Ensure input is never nil for non-source nodes (prevents panics)
	if input == nil && node.Type != models.NodeTypeSourceFile &&
		node.Type != models.NodeTypeSourceAPI && node.Type != models.NodeTypeSourceDB {
		input = &common.DataSet{Columns: []string{}, Rows: []common.DataRow{}}
	}

	// Retry config
	maxRetries := 0
	if mr, ok := node.Config["max_retries"].(float64); ok {
		maxRetries = int(mr)
	}
	baseDelay := time.Second
	if rd, ok := node.Config["retry_delay"].(float64); ok && rd > 0 {
		baseDelay = time.Duration(rd) * time.Millisecond
	}
	nodeTimeout := 30 * time.Minute
	if t, ok := node.Config["timeout"].(float64); ok && t > 0 {
		nodeTimeout = time.Duration(t) * time.Second
	}

	type nodeResult struct {
		output *common.DataSet
		err    error
	}

	r.emit(models.Event{Type: models.EventNodeStarted, RunID: r.run.ID, NodeID: node.ID})

	var output *common.DataSet
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff: baseDelay * 2^(attempt-1), max 60s
			delay := baseDelay * time.Duration(1<<uint(attempt-1))
			if delay > 60*time.Second {
				delay = 60 * time.Second
			}
			r.logWithTrace(node.ID, models.LogLevelWarning, "", attempt, nil,
				"Retry %d/%d in %v (exponential backoff)", attempt, maxRetries, delay)
			r.emit(models.Event{
				Type: models.EventNodeStarted, RunID: r.run.ID, NodeID: node.ID,
				Status: "retrying", Error: fmt.Sprintf("retry %d/%d", attempt, maxRetries),
			})
			// Persist the retry decision — previously this only reached the
			// ephemeral WebSocket event above, with no durable counterpart.
			if !r.dryRun {
				retryAttempt := attempt
				r.appendEvent(models.RunEvent{
					RunID:     r.run.ID,
					NodeID:    node.ID,
					Attempt:   &retryAttempt,
					EventType: models.RetryScheduled,
					Payload: models.RunEventPayload{
						Error:     lastErr.Error(),
						BackoffMs: delay.Milliseconds(),
					},
				})
			}
			select {
			case <-time.After(delay):
			case <-r.ctx.Done():
				return fmt.Errorf("cancelled during retry wait")
			}
		}

		// Create a NodeRun for THIS attempt
		spanID := common.NewID()
		startTime := time.Now().UTC()
		queueMs := int64(0)
		if attempt == 0 {
			queueMs = startTime.Sub(readyAt).Milliseconds()
		}

		nr := &models.NodeRun{
			ID:        common.NewID(),
			RunID:     r.run.ID,
			NodeID:    node.ID,
			Status:    models.RunStatusRunning,
			StartedAt: &startTime,
			Attempt:   attempt,
			ReadyAt:   &readyAt,
			QueueMs:   queueMs,
			TraceID:   r.traceID,
			SpanID:    spanID,
		}
		crashAt(crashPointBeforeNodeAttemptCreate, node.ID)
		if !r.dryRun {
			// CreateNodeRun is the durable attempt record: no external
			// side effect (runNodeLogic below) is permitted to start until
			// it is persisted. Matches the already-correct run-level
			// pattern (CreateRun above checks and propagates); previously
			// this error was discarded entirely.
			if err := r.store.CreateNodeRun(nr); err != nil {
				return fmt.Errorf("persist node attempt for %s (attempt %d): %w", node.Name, attempt, err)
			}
			attemptNum := attempt
			r.appendEvent(models.RunEvent{
				RunID:     r.run.ID,
				NodeID:    node.ID,
				Attempt:   &attemptNum,
				EventType: models.AttemptStarted,
				Payload: models.RunEventPayload{
					Status:    models.RunStatusRunning,
					NodeRunID: nr.ID,
					StartedAt: nr.StartedAt,
					ReadyAt:   nr.ReadyAt,
					QueueMs:   nr.QueueMs,
					TraceID:   nr.TraceID,
					SpanID:    nr.SpanID,
				},
			})
		}
		crashAt(crashPointAfterNodeAttemptCreate, node.ID)

		// Span/metrics for this attempt only apply to real (non-dry-run)
		// execution — a dry run never persists a NodeRun record either (see
		// the `if !r.dryRun` block above), so it has no durable attempt
		// identity to attach a span to. attemptSpan stays a nil-safe no-op
		// trace.Span (the zero value returned by an unstarted span variable
		// is never dereferenced below since every use is gated on
		// !r.dryRun too) when this is a dry run.
		var attemptSpan trace.Span
		if !r.dryRun {
			_, attemptSpan = tracing.Tracer().Start(r.ctx, "brokoli.node.attempt",
				trace.WithAttributes(
					attribute.String("run_id", r.run.ID),
					attribute.String("node_id", node.ID),
					attribute.Int("attempt", attempt),
				))
			r.metrics.incrAttemptsStarted()
			if attempt > 0 {
				r.metrics.incrAttemptsRetried()
			}
		}

		if attempt == 0 {
			r.logWithTrace(node.ID, models.LogLevelInfo, spanID, attempt,
				map[string]string{"queue_ms": fmt.Sprintf("%d", queueMs)},
				"Starting node: %s (%s)", node.Name, node.Type)
		} else {
			r.logWithTrace(node.ID, models.LogLevelInfo, spanID, attempt, nil,
				"Retry attempt %d for node: %s", attempt, node.Name)
		}

		// Execute with timeout. attemptCtx is derived from r.ctx (the
		// pipeline run's own cancellable context) with this attempt's
		// timeout applied — its Done() channel closes on EITHER pipeline
		// cancellation or the attempt timing out, whichever comes first.
		// It is handed to external NodeExecutors (extensions.ExecutionContext.Context)
		// below via runNodeLogic so they can observe cancellation/deadline
		// instead of being silently abandoned when this select moves on.
		attemptCtx, attemptCancel := context.WithTimeout(r.ctx, nodeTimeout)
		idempotencyKey := nodeAttemptIdempotencyKey(r.run.ID, node.ID, attempt)

		resultCh := make(chan nodeResult, 1)
		go func() {
			crashAt(crashPointBeforeNodeExecution, node.ID)
			out, e := r.runNodeLogic(node, input, allInputs, edgeInputsByFrom, attempt, idempotencyKey, attemptCtx)
			resultCh <- nodeResult{out, e}
		}()

		var err error
		select {
		case result := <-resultCh:
			output, err = result.output, result.err
		case <-attemptCtx.Done():
			if r.ctx.Err() != nil {
				err = fmt.Errorf("pipeline cancelled")
			} else {
				err = fmt.Errorf("node timed out after %s", nodeTimeout)
			}
		}
		attemptCancel()
		crashAt(crashPointAfterNodeExecutionBeforePersist, node.ID)

		duration := time.Since(startTime).Milliseconds()

		if err == nil {
			// ── Success ──
			rowCount := 0
			if output != nil {
				rowCount = len(output.Rows)
			}
			rowsPerSec := float64(0)
			if duration > 0 && rowCount > 0 {
				rowsPerSec = float64(rowCount) / (float64(duration) / 1000.0)
			}

			nr.Status = models.RunStatusSuccess
			nr.DurationMs = duration
			nr.RowCount = rowCount
			nr.RowsPerSec = rowsPerSec
			if !r.dryRun {
				// If the success can't be durably recorded, the node is not
				// considered done — same "storage failures are explicit"
				// contract as CreateNodeRun above and run-level UpdateRun.
				if err := r.store.UpdateNodeRun(nr); err != nil {
					attemptSpan.RecordError(err)
					attemptSpan.SetStatus(codes.Error, err.Error())
					attemptSpan.End()
					return fmt.Errorf("persist successful node attempt for %s (attempt %d): %w", node.Name, attempt, err)
				}
				attemptNum := attempt
				r.appendEvent(models.RunEvent{
					RunID:     r.run.ID,
					NodeID:    node.ID,
					Attempt:   &attemptNum,
					EventType: models.AttemptCompleted,
					Payload: models.RunEventPayload{
						Status:     models.RunStatusSuccess,
						NodeRunID:  nr.ID,
						RowCount:   nr.RowCount,
						DurationMs: nr.DurationMs,
						RowsPerSec: nr.RowsPerSec,
					},
				})
			}
			crashAt(crashPointAfterNodeCompletionPersist, node.ID)

			if attempt > 0 {
				r.logWithTrace(node.ID, models.LogLevelInfo, spanID, attempt, nil,
					"Succeeded after %d retries", attempt)
			}

			// Store output
			if output != nil {
				if r.dryRun && r.dryRunMaxRows > 0 && len(output.Rows) > r.dryRunMaxRows {
					output.Rows = output.Rows[:r.dryRunMaxRows]
				}
				if err := outputs.Put(node.ID, output); err != nil {
					r.log(node.ID, models.LogLevelWarning, "Could not spill output, keeping it in memory: %v", err)
				}

				if r.dryRun {
					if r.dryRunResults == nil {
						r.dryRunResults = make(map[string]*DryRunNodeResult)
					}
					previewRows := make([]map[string]interface{}, len(output.Rows))
					for i, row := range output.Rows {
						previewRows[i] = map[string]interface{}(row)
					}
					r.dryRunResults[node.ID] = &DryRunNodeResult{
						NodeID: node.ID, Name: node.Name, Status: "success",
						Columns: output.Columns, Rows: previewRows,
					}
				} else {
					if err := r.store.SaveNodePreview(r.run.ID, node.ID, output.Columns, output.Rows); err != nil {
						attemptSpan.RecordError(err)
						attemptSpan.SetStatus(codes.Error, err.Error())
						attemptSpan.End()
						return fmt.Errorf("persist node preview for %s (attempt %d): %w", node.Name, attempt, err)
					}
					// Durable full-output artifact — distinct from
					// SaveNodePreview above, which truncates to 50 rows for
					// UI display and is not sufficient to correctly resume
					// a downstream node. See ArtifactStore and
					// docs/adr/010-run-artifact-and-resume-semantics.md.
					// Node types in nonResumableNodeTypes never get an
					// artifact even on success — see that var's doc comment.
					if r.artifactStore != nil && !nonResumableNodeTypes[node.Type] {
						if err := r.artifactStore.WriteArtifact(r.run.ID, node.ID, output); err != nil {
							// Not fatal to this node's success — mirrors
							// AppendLog's "log, don't abort" policy below —
							// but it does mean a future resume that needs
							// this node's output will fail loudly instead
							// of silently substituting empty data, which is
							// the correct failure mode either way.
							r.log(node.ID, models.LogLevelWarning, "Failed to persist durable artifact for node %s (a future resume needing its output will fail loudly): %v", node.Name, err)
						}
					}
				}
			}

			// Completion log with throughput
			durStr := fmt.Sprintf("%dms", duration)
			if duration >= 1000 {
				durStr = fmt.Sprintf("%.1fs", float64(duration)/1000)
			}
			tpStr := ""
			if rowsPerSec >= 1000 {
				tpStr = fmt.Sprintf(" (%.0fK rows/sec)", rowsPerSec/1000)
			} else if rowsPerSec > 0 {
				tpStr = fmt.Sprintf(" (%.0f rows/sec)", rowsPerSec)
			}
			colInfo := ""
			if output != nil && len(output.Columns) > 0 {
				colInfo = fmt.Sprintf(", columns: [%s]", truncateList(output.Columns, 8))
			}
			r.logWithTrace(node.ID, models.LogLevelInfo, spanID, attempt,
				map[string]string{
					"duration_ms":  fmt.Sprintf("%d", duration),
					"row_count":    fmt.Sprintf("%d", rowCount),
					"rows_per_sec": fmt.Sprintf("%.1f", rowsPerSec),
					"queue_ms":     fmt.Sprintf("%d", queueMs),
				},
				"Node completed: %d rows in %s%s%s", rowCount, durStr, tpStr, colInfo)
			r.emit(models.Event{Type: models.EventNodeCompleted, RunID: r.run.ID, NodeID: node.ID, RowCount: rowCount, DurationMs: duration})
			if !r.dryRun {
				attemptSpan.SetAttributes(
					attribute.Int("row_count", rowCount),
					attribute.Int64("duration_ms", duration),
				)
				attemptSpan.SetStatus(codes.Ok, "")
				attemptSpan.End()
				r.metrics.incrAttemptsSucceeded()
				common.SLog().Info("node attempt succeeded",
					common.RunAttr(r.run.ID), common.NodeAttr(node.ID), common.AttemptAttr(attempt),
					"row_count", rowCount, "duration_ms", duration)
			}
			lastErr = nil
			break
		}

		// ── Failure for this attempt ──
		nr.Status = models.RunStatusFailed
		nr.DurationMs = duration
		nr.Error = err.Error()
		if !r.dryRun {
			// If we can't durably record that this attempt failed, don't
			// keep retrying against a store we can no longer trust to
			// track attempt state — surface both errors and stop, rather
			// than silently continuing (previous behavior).
			if persistErr := r.store.UpdateNodeRun(nr); persistErr != nil {
				attemptSpan.RecordError(persistErr)
				attemptSpan.SetStatus(codes.Error, persistErr.Error())
				attemptSpan.End()
				return fmt.Errorf("node %s (%s) failed: %v; persist failed node attempt (attempt %d): %w", node.Name, node.ID, err, attempt, persistErr)
			}
			attemptNum := attempt
			r.appendEvent(models.RunEvent{
				RunID:     r.run.ID,
				NodeID:    node.ID,
				Attempt:   &attemptNum,
				EventType: models.AttemptFailed,
				Payload: models.RunEventPayload{
					Status:     models.RunStatusFailed,
					NodeRunID:  nr.ID,
					DurationMs: nr.DurationMs,
					Error:      nr.Error,
				},
			})
			attemptSpan.RecordError(err)
			attemptSpan.SetStatus(codes.Error, err.Error())
			attemptSpan.End()
			r.metrics.incrAttemptsFailed()
			common.SLog().Warn("node attempt failed",
				common.RunAttr(r.run.ID), common.NodeAttr(node.ID), common.AttemptAttr(attempt),
				"error", err, "duration_ms", duration)
		}
		lastErr = err

		r.logWithTrace(node.ID, models.LogLevelWarning, spanID, attempt,
			map[string]string{"error": err.Error(), "duration_ms": fmt.Sprintf("%d", duration)},
			"Attempt %d/%d failed: %v", attempt+1, maxRetries+1, err)
	}

	if lastErr != nil {
		r.logWithTrace(node.ID, models.LogLevelError, "", maxRetries, nil,
			"Node %s failed after %d attempt(s): %v", node.Name, maxRetries+1, lastErr)
		r.emit(models.Event{Type: models.EventNodeFailed, RunID: r.run.ID, NodeID: node.ID, Error: lastErr.Error()})
		common.SLog().Error("node failed",
			common.RunAttr(r.run.ID), common.NodeAttr(node.ID), "attempts", maxRetries+1, "error", lastErr)
		return fmt.Errorf("node %s (%s) failed: %w", node.Name, node.ID, lastErr)
	}
	return nil
}

// restoreSkippedNodeOutput recovers the durable output artifact for a node
// being skipped during resume (Tnsor-Labs/brokoli#8). It never falls back to
// synthesizing empty data for a node that matters to the rest of the DAG:
//
//   - restored=true: ds is the node's real prior output, read from the
//     source run's artifact store — restore it into the outputs map.
//   - restored=false, err=nil: no artifact exists, but nothing in the
//     CURRENT pipeline reads this node's output (no outgoing edges), so an
//     absent artifact cannot produce wrong data further along the DAG. This
//     is the normal case for a sink node, or for a non-resumable node type
//     (see nonResumableNodeTypes) with no downstream consumers.
//   - err != nil: this node's output is unavailable AND at least one node
//     downstream of it depends on that data. Resuming would otherwise feed
//     that downstream node an empty/synthesized dataset exactly like the
//     bug this fixes — fail the whole resume attempt loudly instead,
//     naming the node, rather than proceed with corrupted data.
func (r *Runner) restoreSkippedNodeOutput(node models.Node) (ds *common.DataSet, restored bool, err error) {
	hasDownstream := false
	for _, edge := range r.pipe.Edges {
		if edge.From == node.ID {
			hasDownstream = true
			break
		}
	}

	sourceRunID := r.resumedFromRunID
	if sourceRunID == "" {
		// Not a resume (skipNodes set some other way, e.g. a future
		// caller) — fall back to this run's own ID so ReadArtifact still
		// has a well-defined key to look up, even though nothing will have
		// been written under it for a node that hasn't executed yet.
		sourceRunID = r.run.ID
	}

	if r.artifactStore == nil {
		if hasDownstream {
			return nil, false, fmt.Errorf("cannot resume: node %s (%s) has downstream consumers but no artifact store is configured to restore its output", node.Name, node.ID)
		}
		return nil, false, nil
	}

	ds, readErr := r.artifactStore.ReadArtifact(sourceRunID, node.ID)
	if readErr != nil {
		if !hasDownstream {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("resume: node %s (%s) is non-resumable — no durable output artifact from run %s (%v); refusing to feed downstream nodes an empty or synthesized dataset", node.Name, node.ID, sourceRunID, readErr)
	}
	return ds, true, nil
}

// nodeAttemptIdempotencyKey derives a stable identifier for one (run, node,
// attempt) unit of dispatchable work, for external NodeExecutors that need
// to recognize a redispatch of the same attempt rather than starting a new
// one — e.g. a Kubernetes executor naming its Job deterministically so a
// crash-and-redispatch doesn't create a duplicate Job. It mirrors the
// (run_id, node_id, attempt) identity models.ExecutionAttempt will use once
// node-level claim/lease (Tnsor-Labs/brokoli#7) is wired into this loop;
// until then, the retry loop's own attempt counter below is the actual
// source of truth for "which attempt is this", so it's what we derive from.
func nodeAttemptIdempotencyKey(runID, nodeID string, attempt int) string {
	return fmt.Sprintf("%s:%s:%d", runID, nodeID, attempt)
}

func (r *Runner) runNodeLogic(node models.Node, input *common.DataSet, allInputs []*common.DataSet, edgeInputsByFrom map[string]*common.DataSet, attempt int, idempotencyKey string, ctx context.Context) (*common.DataSet, error) {
	// Check if the org's plan allows this node type. Deliberately checked
	// before the executor loop below, not after: a NodeExecutor can claim
	// any node type string (a plugin-registered type, an enterprise K8s-
	// dispatched type, etc.), and this gate is meant to apply uniformly to
	// every node type regardless of which mechanism ultimately executes
	// it — a gate that only fired for the built-in switch further down
	// would silently never apply to executor-dispatched types at all.
	if extensions.NodeTypeGateFunc != nil {
		if msg := extensions.NodeTypeGateFunc(r.pipe.OrgID, string(node.Type)); msg != "" {
			return nil, fmt.Errorf("%s", msg)
		}
	}

	// Check if an external executor handles this node type (enterprise: K8s, Docker)
	for _, exec := range r.executors {
		if exec != nil && exec.CanHandle(string(node.Type)) {
			r.log(node.ID, models.LogLevelInfo, "Dispatching to %s executor", exec.Name())
			result, err := exec.Execute(extensions.ExecutionContext{
				RunID:      r.run.ID,
				NodeID:     node.ID,
				NodeType:   string(node.Type),
				NodeName:   node.Name,
				Config:     node.Config,
				InputData:  input,
				PipelineID: r.pipe.ID,

				Attempt:        attempt,
				IdempotencyKey: idempotencyKey,
				// FencingGeneration left at its zero value — see
				// extensions.ExecutionContext's doc comment.
				Context: ctx,
			})
			if err != nil {
				return nil, err
			}
			for _, logLine := range result.Logs {
				if logLine != "" {
					r.log(node.ID, models.LogLevelInfo, "[%s] %s", exec.Name(), logLine)
				}
			}
			if result.OutputData != nil {
				if ds, ok := result.OutputData.(*common.DataSet); ok {
					return ds, nil
				}
			}
			return nil, nil
		}
	}

	switch node.Type {
	case models.NodeTypeSourceFile:
		return r.runSourceFile(node)
	case models.NodeTypeSourceAPI:
		return r.runSourceAPI(node)
	case models.NodeTypeSourceDB:
		return r.runSourceDB(node)
	case models.NodeTypeTransform:
		return r.runTransform(node, input)
	case models.NodeTypeQualityCheck:
		return r.runQualityCheck(node, input)
	case models.NodeTypeCode:
		// Dynamic node expansion (#31): a `code` node carrying an
		// `expansion` config block (brokoli-sdk's _TaskWrapper.expand())
		// fans out into one execution per item instead of running once —
		// see engine/expansion.go's top-of-file doc comment for the full
		// architectural resolution.
		if nodeHasExpansion(node) {
			return r.runCodeExpansion(node, edgeInputsByFrom, attempt)
		}
		return r.runCode(node, input)
	case models.NodeTypeJoin:
		return r.runJoin(node, allInputs)
	case models.NodeTypeSQLGenerate:
		return r.runSQLGenerate(node, input)
	case models.NodeTypeSinkFile:
		return r.runSinkFile(node, input)
	case models.NodeTypeSinkDB:
		return r.runSinkDB(node, input)
	case models.NodeTypeSinkAPI:
		return r.runSinkAPI(node, input)
	case models.NodeTypeMigrate:
		return r.runMigrate(node)
	case models.NodeTypeCondition:
		return r.runCondition(node, input)
	case models.NodeTypeDBT:
		return r.runDBT(node)
	case models.NodeTypeNotify:
		return r.runNotify(node, input)
	case models.NodeTypeUnion:
		return r.runUnion(node, allInputs)
	case models.NodeTypeDatasetMap:
		return r.runDatasetMap(node, input)
	case models.NodeTypeDatasetFilter:
		return r.runDatasetFilter(node, input)
	default:
		return input, nil
	}
}

func (r *Runner) failRun(err error) error {
	finishTime := time.Now().UTC()
	r.run.Status = models.RunStatusFailed
	r.run.FinishedAt = &finishTime
	if persistErr := r.store.UpdateRun(r.run); persistErr != nil {
		return fmt.Errorf("run failed: %v; persist failed run: %w", err, persistErr)
	}
	r.appendEvent(models.RunEvent{
		RunID:     r.run.ID,
		EventType: models.RunEventTerminal,
		Payload: models.RunEventPayload{
			Status:     models.RunStatusFailed,
			FinishedAt: r.run.FinishedAt,
			Error:      err.Error(),
		},
	})
	r.emit(models.Event{Type: models.EventRunFailed, RunID: r.run.ID, PipelineID: r.pipe.ID, Error: err.Error()})
	r.raiseFailureAlert(err)
	r.sendNotification("run.failed", "critical", fmt.Sprintf("Pipeline \"%s\" failed", r.pipe.Name), err.Error())
	NotifyPipelineEvent(r.pipe, r.run, "run.failed", err.Error())
	// Add to dead letter queue
	if r.store != nil {
		if dlqErr := r.store.AddToDLQ(r.pipe.ID, r.run.ID, "", "", err.Error(), ""); dlqErr != nil {
			return fmt.Errorf("run failed: %v; persist dlq entry: %w", err, dlqErr)
		}
	}
	return err
}

func (r *Runner) emit(e models.Event) {
	e.Timestamp = time.Now().UTC()
	e.OrgID = r.orgID // tenant isolation
	// Stamp the pipeline's name at emit time so downstream consumers never
	// have to resolve an ID back to a name. That resolution is impossible
	// once a pipeline is deleted — the row is gone, but its historical runs
	// remain and should still say what they ran (Tnsor-Labs/brokoli#75).
	// r.pipe is nil for Runners built without a pipeline (DryRun, and tests
	// that construct a Runner directly), and emit is reached from the
	// logging path in those cases — so this must stay nil-safe.
	if e.PipelineName == "" && r.pipe != nil {
		e.PipelineName = r.pipe.Name
	}
	select {
	case r.eventCh <- e:
	default:
	}
}

// raiseFailureAlert records a durable, readable alert for a failed run.
// Distinct from sendNotification just below: that pushes outbound to Slack
// and is lost if nobody is watching, this persists so the failure can be
// read back later from the alerts inbox.
//
// Best-effort by design — a storage failure here must never turn a failed
// run into a *differently* failed run, so it is logged and swallowed,
// matching how appendEvent and AppendLog treat their own write failures.
// There are two mutually exclusive terminal-failure paths — the DAG loop's
// runErr branch and failRun's early exit — and both raise an alert. The
// guard makes a second call a no-op so a future refactor that lets both run
// can't produce a duplicate inbox entry for one failure.
func (r *Runner) raiseFailureAlert(runErr error) {
	if r.alertRaised {
		return
	}
	r.alertRaised = true

	alert := &models.Alert{
		ID:           common.NewID(),
		OrgID:        r.orgID,
		Kind:         models.AlertKindRunFailure,
		Severity:     models.AlertSeverityCritical,
		Title:        fmt.Sprintf("Pipeline %q failed", r.pipe.Name),
		Body:         runErr.Error(),
		PipelineID:   r.pipe.ID,
		PipelineName: r.pipe.Name,
		RunID:        r.run.ID,
		CreatedAt:    time.Now().UTC(),
	}
	if err := r.store.CreateAlert(alert); err != nil {
		common.SLog().Warn("create failure alert", common.RunAttr(r.run.ID),
			common.PipelineAttr(r.pipe.ID), "error", err)
	}
}

// appendEvent persists a run/node-attempt lifecycle event to the durable
// run_events log, dual-written alongside the CreateRun/UpdateRun/
// CreateNodeRun/UpdateNodeRun calls above. Like AppendLog just below,
// failures are logged rather than propagated — making this write
// transactionally required (e.g. via an outbox) is tracked separately
// (Tnsor-Labs/brokoli#7).
func (r *Runner) appendEvent(ev models.RunEvent) {
	if err := r.store.AppendEvent(&ev); err != nil {
		r.metrics.incrEventsAppendFailed()
		r.log(ev.NodeID, models.LogLevelWarning, "append run event %s: %v", ev.EventType, err)
		return
	}
	r.metrics.incrEventsAppended()
}

func (r *Runner) log(nodeID string, level models.LogLevel, format string, args ...interface{}) {
	r.logWithTrace(nodeID, level, "", 0, nil, format, args...)
}

func (r *Runner) logWithTrace(nodeID string, level models.LogLevel, spanID string, attempt int, metadata map[string]string, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	// AppendLog is called from ~70 sites across engine/*.go for routine
	// informational logging (source/transform/sink node bodies, retries,
	// hooks, etc.). Unlike CreateNodeRun/UpdateNodeRun/SaveNodePreview/
	// AddToDLQ above — each a single call site guarding a specific
	// durability-critical transition — making every one of those ~70 sites
	// check and propagate a log-write failure would mean a transient log
	// persistence error aborts an otherwise-successful pipeline node, a much
	// larger behavior change than this fix warrants. Surface the failure
	// loudly via the process log instead of silently discarding it (the
	// previous behavior) without changing this function's signature or
	// making log persistence load-bearing for node success.
	if err := r.store.AppendLog(&models.LogEntry{
		RunID:     r.run.ID,
		NodeID:    nodeID,
		Level:     level,
		Message:   msg,
		Timestamp: time.Now().UTC(),
		TraceID:   r.traceID,
		SpanID:    spanID,
		Attempt:   attempt,
		Metadata:  metadata,
	}); err != nil {
		common.SLog().Warn("append log entry failed",
			common.RunAttr(r.run.ID), common.NodeAttr(nodeID), "error", err)
	}
	r.emit(models.Event{
		Type:    models.EventLog,
		RunID:   r.run.ID,
		NodeID:  nodeID,
		Level:   level,
		Message: msg,
	})
}

// topoSort performs Kahn's algorithm for topological ordering.
func topoSort(nodes []models.Node, edges []models.Edge) ([]models.Node, error) {
	nodeMap := make(map[string]models.Node)
	inDegree := make(map[string]int)
	adj := make(map[string][]string)

	for _, n := range nodes {
		nodeMap[n.ID] = n
		inDegree[n.ID] = 0
	}

	for _, e := range edges {
		adj[e.From] = append(adj[e.From], e.To)
		inDegree[e.To]++
	}

	var queue []string
	for _, n := range nodes {
		if inDegree[n.ID] == 0 {
			queue = append(queue, n.ID)
		}
	}

	var sorted []models.Node
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		sorted = append(sorted, nodeMap[id])

		for _, next := range adj[id] {
			inDegree[next]--
			if inDegree[next] == 0 {
				queue = append(queue, next)
			}
		}
	}

	if len(sorted) != len(nodes) {
		return nil, fmt.Errorf("cycle detected in pipeline graph")
	}

	return sorted, nil
}

// ── Lifecycle Hooks ─────────────────────────────────────────

func (r *Runner) fireHook(hookName string, extra map[string]string) {
	if r.pipe.Hooks == nil {
		return
	}
	hook, ok := r.pipe.Hooks[hookName]
	if !ok || !hook.Enabled || hook.URL == "" {
		return
	}

	payload := map[string]interface{}{
		"event":       hookName,
		"pipeline_id": r.pipe.ID,
		"pipeline":    r.pipe.Name,
		"run_id":      r.run.ID,
		"timestamp":   time.Now().UTC().Format(time.RFC3339),
	}
	for k, v := range extra {
		payload[k] = v
	}

	data, _ := json.Marshal(payload)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", hook.URL, strings.NewReader(string(data)))
	if err != nil {
		r.log("", models.LogLevelWarning, "Hook %s: failed to create request: %v", hookName, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		r.log("", models.LogLevelWarning, "Hook %s: request failed: %v", hookName, err)
		return
	}
	resp.Body.Close()
	r.log("", models.LogLevelInfo, "Hook %s fired: HTTP %d", hookName, resp.StatusCode)
}
