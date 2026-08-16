package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/artifact"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/tracing"
	"github.com/Tnsor-Labs/brokoli/store"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Runner executes a single pipeline run.
type Runner struct {
	store        store.Store
	eventCh      chan<- models.Event
	varStore     VariableStore       // for ${var.key} resolution
	connResolver *ConnectionResolver // for conn_id → URI
	ctx          context.Context
	cancel       context.CancelFunc
	// cancelMu guards cancel/preCancelled so Cancel is durable across the
	// window before Execute creates r.ctx. Without it, a Cancel that lands
	// while the runner is registered in Engine.active but not yet executing
	// (RunPipeline's pre-goroutine registration; ExecuteQueuedRun waiting on
	// the concurrency semaphore, which under saturation can be minutes) hit
	// a nil r.cancel and was silently lost — the run then executed to
	// completion as if never cancelled.
	cancelMu     sync.Mutex
	preCancelled bool
	run          *models.Run
	pipe         *models.Pipeline
	skipNodes    map[string]bool
	// conditionResults restores durable IR 2.1 decisions for condition
	// nodes skipped during resume.
	conditionResults map[string]bool
	// artifactSourceRunIDs resolves each resumed node to the ancestor run
	// that durably owns its output.
	artifactSourceRunIDs map[string]string
	dryRun               bool
	dryRunMaxRows        int
	dryRunResults        map[string]*DryRunNodeResult
	params               map[string]string // runtime params
	varCtx               *VariableContext
	preRunID             string                          // pre-generated run ID (for registration before Execute)
	acceptedRun          *models.Run                     // queued run already persisted and atomically claimed
	orgID                string                          // tenant isolation for WebSocket events
	alertRaised          bool                            // guards raiseFailureAlert against double-firing
	traceID              string                          // distributed tracing correlation ID
	executors            []extensions.NodeExecutor       // enterprise: external executors (K8s, Docker)
	notifier             extensions.NotificationProvider // enterprise: Slack, PagerDuty, etc.
	// instanceID is this Runner's Engine's InstanceID (Tnsor-Labs/brokoli#90
	// M3) — the claimedBy identity passed to store.ExecutionAttemptStore's
	// ClaimAttempt/AckAttempt when the node dispatch loop wires through it.
	instanceID string

	// instanceJobQueue is this Runner's Engine's InstanceJobQueue (ADR-017),
	// nil by default — see that field's doc comment. Non-nil opts dynamic-
	// expansion instances into remote dispatch instead of local execution.
	instanceJobQueue extensions.JobQueue
	// requiredCapabilities is copied onto every physical WorkOrder emitted by
	// this run so Nodus can place it only on compatible workers.
	requiredCapabilities []string

	// expansionResults caches a dynamic-expansion item's successful output
	// across that same expansion node's own retry attempts within this run
	// (Tnsor-Labs/brokoli#90 M3, ADR-015 §6's "retry without rerunning
	// successful siblings" guarantee) — keyed nodeID -> instanceKey. Scoped
	// to this Runner's lifetime (one run): it survives a node-level retry
	// within the same process, not a process crash, since the actual item
	// output isn't durably persisted anywhere per-instance today (only
	// models.ExpansionInstance's status/row-count metadata is) — true
	// crash-survivable per-item retry needs durable per-instance output
	// storage, deferred to #90 M3's larger per-instance dispatch work, same
	// as #143's paginated-source case.
	expansionResults map[string]map[string]*common.DataSet

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
	// streamThreshold is the ADR-019 reference-passing engagement size —
	// see Engine.StreamThresholdBytes.
	streamThreshold int64

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

type edgeResolution uint8

const (
	edgeUnresolved edgeResolution = iota
	edgeInactive
	edgeActive
)

type nodeScheduleState uint8

const (
	nodePending nodeScheduleState = iota
	nodeQueued
	nodeExecuted
	nodeSkipped
)

type nodeExecutionResult struct {
	output    *common.DataSet
	condition *bool
	// outputRef is the ADR-019 Milestone 1 alternative to output: the
	// node's result already in the blob store, never materialized. At
	// most one of output/outputRef is set; every consumer of output in
	// executeNode's success path has a ref-aware branch.
	outputRef *artifact.DatasetRef
}

// NewRunner creates a runner for the given pipeline.
func NewRunner(s store.Store, eventCh chan<- models.Event, pipe *models.Pipeline, vs VariableStore, cr *ConnectionResolver, execs []extensions.NodeExecutor, notifier extensions.NotificationProvider, instanceID string, instanceJobQueue extensions.JobQueue, requiredCapabilities ...[]string) *Runner {
	r := &Runner{
		varStore:         vs,
		connResolver:     cr,
		executors:        execs,
		notifier:         notifier,
		store:            s,
		eventCh:          eventCh,
		pipe:             pipe,
		instanceID:       instanceID,
		instanceJobQueue: instanceJobQueue,
	}
	if len(requiredCapabilities) > 0 {
		r.requiredCapabilities = append([]string(nil), requiredCapabilities[0]...)
	}
	return r
}

// Cancel stops a running pipeline. Safe to call at any point in the
// runner's lifecycle: before Execute has created the run context, the
// cancellation is remembered and applied the moment the context exists.
func (r *Runner) Cancel() {
	r.cancelMu.Lock()
	r.preCancelled = true
	cancel := r.cancel
	r.cancelMu.Unlock()
	if cancel != nil {
		cancel()
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

	r.cancelMu.Lock()
	r.ctx, r.cancel = context.WithCancel(spanCtx)
	if r.preCancelled {
		// A Cancel arrived before the run context existed (see Cancel).
		// Cancelling the fresh context up front routes the whole execution
		// through the standard cancellation exits: the pre-node ctx checks
		// fire immediately and the run finalizes as cancelled.
		r.cancel()
	}
	r.cancelMu.Unlock()
	defer r.cancel()
	// Reclaim the run's transient blob scratch once it is terminal
	// (Tnsor-Labs/brokoli#215) — see TransientBlobJanitor. Registered
	// before the run row exists, so the helper no-ops if Execute bails
	// early, and it skips non-terminal exits: an unsettled outcome (e.g.
	// a persist failure mid-finalize) keeps its state for recovery.
	defer r.reclaimTransientBlobs()

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
	// Persist the physical plan as-decided for this run (ADR-015, #90 M2),
	// once the run row exists so the FK parent is present. Best-effort and
	// optional-capability: a store that doesn't implement PhysicalPlanStore
	// (e.g. a hand-written one that hasn't opted in yet) simply skips it,
	// and a save failure must never fail the run — the plan is for audit
	// and recovery, not execution.
	r.savePhysicalPlan()
	// Persist the run's physical instances once execution finishes, by
	// every exit path (success, failure, cancellation). Best-effort and
	// optional-capability, like the plan save: the durable per-instance
	// record (ADR-015 point 3, #90 M3) that GET /runs/{id}/instances
	// reads authoritatively and that dispatch will later lease against.
	defer r.savePhysicalInstances()
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

	// Build the runtime graph. Edges resolve active or inactive; data
	// emptiness never stands in for control flow.
	incoming := make(map[string][]int)
	outgoing := make(map[string][]int)
	unresolvedInputs := make(map[string]int)
	activeInputs := make(map[string]int)
	nodeStates := make(map[string]nodeScheduleState)
	for _, n := range r.pipe.Nodes {
		nodeStates[n.ID] = nodePending
	}
	for i, e := range r.pipe.Edges {
		incoming[e.To] = append(incoming[e.To], i)
		outgoing[e.From] = append(outgoing[e.From], i)
		unresolvedInputs[e.To]++
	}
	edgeStates := make([]edgeResolution, len(r.pipe.Edges))
	routingEnabled := r.pipe.IRVersion == models.ConditionalEdgesIRVersion

	// Outputs map (thread-safe)
	// Node results for the rest of the run. Large outputs are written to
	// the artifact store and read back on demand rather than held for the
	// whole run — see nodeOutputs and Tnsor-Labs/brokoli#38.
	outputs := r.newOutputs()

	// Max parallelism semaphore (default 4, configurable later)
	maxParallel := 4
	sem := make(chan struct{}, maxParallel)

	var runErr error
	terminalNodes := 0

	resolveEdge := func(edgeIndex int, active bool) error {
		if edgeIndex < 0 || edgeIndex >= len(edgeStates) {
			return fmt.Errorf("scheduler resolved invalid edge index %d", edgeIndex)
		}
		if edgeStates[edgeIndex] != edgeUnresolved {
			return fmt.Errorf("scheduler resolved edge %d more than once", edgeIndex)
		}
		edge := r.pipe.Edges[edgeIndex]
		if active {
			edgeStates[edgeIndex] = edgeActive
			activeInputs[edge.To]++
		} else {
			edgeStates[edgeIndex] = edgeInactive
		}
		unresolvedInputs[edge.To]--
		if unresolvedInputs[edge.To] < 0 {
			return fmt.Errorf("scheduler input count underflow for node %s", edge.To)
		}
		return nil
	}

	// Watch the durable cancel flag while nodes execute — see
	// startCancelIntentWatcher for why the wave-boundary check below is
	// not enough on its own.
	stopIntentWatcher := r.startCancelIntentWatcher()
	defer stopIntentWatcher()

	for terminalNodes < len(r.pipe.Nodes) {
		// Durable cancel intent (store.RunCancelRequester): a cancel whose
		// delivery was lost — relay message dropped, or requested while no
		// process owned the run — still lands here, at the next wave
		// boundary. One cheap primary-key read per wave; a read failure is
		// ignored (the relay and local paths remain the fast, primary
		// mechanisms — this is the convergence backstop).
		if r.run != nil {
			if stored, err := r.store.GetRun(r.run.ID); err == nil && stored.CancelRequested {
				r.Cancel()
			}
		}

		// Check if cancelled
		if r.ctx.Err() != nil {
			runErr = fmt.Errorf("pipeline cancelled")
			break
		}

		// Resolve skip chains before each execution wave. A node runs only
		// after every incoming edge resolves and at least one is active.
		var ready []models.Node
		for {
			madeProgress := false
			for _, node := range r.pipe.Nodes {
				if nodeStates[node.ID] != nodePending {
					continue
				}
				inputCount := len(incoming[node.ID])
				if inputCount == 0 || (unresolvedInputs[node.ID] == 0 && activeInputs[node.ID] > 0) {
					if node.Type == models.NodeTypeJoin && inputCount > 0 && activeInputs[node.ID] != inputCount {
						runErr = fmt.Errorf("join node %s (%s) has an inactive required input: %d of %d inputs active", node.Name, node.ID, activeInputs[node.ID], inputCount)
						break
					}
					nodeStates[node.ID] = nodeQueued
					ready = append(ready, node)
					madeProgress = true
					continue
				}
				if inputCount > 0 && unresolvedInputs[node.ID] == 0 {
					readyAt := time.Now().UTC()
					if err := r.persistSkippedNode(node, readyAt); err != nil {
						runErr = err
						break
					}
					nodeStates[node.ID] = nodeSkipped
					terminalNodes++
					for _, edgeIndex := range outgoing[node.ID] {
						if err := resolveEdge(edgeIndex, false); err != nil {
							runErr = err
							break
						}
					}
					if runErr != nil {
						break
					}
					madeProgress = true
				}
			}
			if runErr != nil || !madeProgress {
				break
			}
		}
		if runErr != nil {
			if r.ctx.Err() != nil {
				break
			}
			return r.run, r.failRun(runErr)
		}
		if len(ready) == 0 {
			var unresolved []string
			for _, node := range r.pipe.Nodes {
				if nodeStates[node.ID] == nodePending || nodeStates[node.ID] == nodeQueued {
					unresolved = append(unresolved, fmt.Sprintf("%s(unresolved=%d,active=%d)", node.ID, unresolvedInputs[node.ID], activeInputs[node.ID]))
				}
			}
			runErr = fmt.Errorf("scheduler stalled with unresolved nodes: %s", strings.Join(unresolved, ", "))
			return r.run, r.failRun(runErr)
		}

		// Execute ready nodes in parallel
		var wg sync.WaitGroup
		type completedNode struct {
			node      models.Node
			condition *bool
			err       error
		}
		completedCh := make(chan completedNode, len(ready))
		readyAt := time.Now().UTC() // all nodes in this wave became ready at this moment

		for _, node := range ready {
			wg.Add(1)
			sem <- struct{}{} // acquire semaphore
			go func(n models.Node) {
				defer wg.Done()
				defer func() { <-sem }() // release semaphore
				var decision *bool
				var nodeErr error
				defer func() {
					if rec := recover(); rec != nil {
						r.log(n.ID, models.LogLevelError, "PANIC in node %s: %v", n.Name, rec)
						nodeErr = fmt.Errorf("node %s panicked: %v", n.Name, rec)
					}
					completedCh <- completedNode{node: n, condition: decision, err: nodeErr}
				}()

				// Check cancellation before starting node
				if r.ctx.Err() != nil {
					nodeErr = fmt.Errorf("pipeline cancelled")
					return
				}

				decision, nodeErr = r.executeNode(n, outputs, edgeStates, readyAt)
			}(node)
		}

		wg.Wait()
		close(completedCh)

		var completed []completedNode
		for result := range completedCh {
			completed = append(completed, result)
			if result.err != nil && runErr == nil {
				runErr = result.err
				break
			}
		}
		if runErr != nil {
			return r.run, r.failRun(runErr)
		}

		// Release outgoing edges only after the whole wave succeeds.
		for _, result := range completed {
			nodeStates[result.node.ID] = nodeExecuted
			terminalNodes++
			for _, edgeIndex := range outgoing[result.node.ID] {
				edge := r.pipe.Edges[edgeIndex]
				active := true
				if routingEnabled && edge.Condition != nil {
					if result.node.Type != models.NodeTypeCondition {
						runErr = fmt.Errorf("conditional edge %s -> %s originates from non-condition node", edge.From, edge.To)
						break
					}
					if result.condition == nil {
						runErr = fmt.Errorf("condition node %s (%s) completed without a routing decision", result.node.Name, result.node.ID)
						break
					}
					active = *edge.Condition == *result.condition
				}
				if err := resolveEdge(edgeIndex, active); err != nil {
					runErr = err
					break
				}
			}
			if runErr != nil {
				break
			}
		}
		if runErr != nil {
			return r.run, r.failRun(runErr)
		}
	}

	finishTime := time.Now().UTC()

	// Check if already cancelled (by CancelRun)
	if r.ctx.Err() != nil {
		return r.run, r.finalizeCancelled()
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

func (r *Runner) executeNode(node models.Node, outputs *nodeOutputs, edgeStates []edgeResolution, readyAt time.Time) (*bool, error) {
	// Skip nodes that already succeeded (resume mode). Restore the node's
	// real durable output into the outputs map so downstream nodes see the
	// original data — never leave it unpopulated, which previously caused
	// executeNode's nil-input fallback further down to silently substitute
	// an empty dataset (Tnsor-Labs/brokoli#8).
	if r.skipNodes != nil && r.skipNodes[node.ID] {
		ds, restored, err := r.restoreSkippedNodeOutput(node)
		if err != nil {
			return nil, err
		}
		if restored {
			// Carry the artifact into this run so a resume-of-a-resume can
			// restore from its immediate parent without losing lineage.
			if r.artifactStore != nil {
				if err := r.artifactStore.WriteArtifact(r.run.ID, node.ID, "", ds); err != nil {
					return nil, fmt.Errorf("carry resumed artifact for node %s: %w", node.Name, err)
				}
			}
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
		var decision *bool
		if selected, ok := r.conditionResults[node.ID]; ok {
			decision = &selected
		}
		artifactRunID := ""
		if restored {
			artifactRunID = r.run.ID
		}
		if err := r.persistSkippedOutcome(node, readyAt, true, decision, artifactRunID); err != nil {
			return nil, err
		}
		return decision, nil
	}

	// Resolve variables in node config
	if r.varCtx != nil && node.Config != nil {
		node.Config = r.varCtx.ResolveConfig(node.Config)
	}

	// Resolve connection (conn_id → URI/headers)
	if r.connResolver != nil && node.Config != nil {
		node.Config = r.connResolver.Resolve(node.Config, node.Type)
	}

	// ADR-019 Milestone 1: decide whether this node takes the
	// reference-passing path BEFORE resolving inputs, because the whole
	// point is not materializing them. streamEligible is a static
	// property of the node; whether streaming actually engages also
	// depends on what the inputs turn out to be (a ref, or nothing),
	// resolved in the loop below. Any node this isn't certain about
	// takes exactly the path it always took.
	activeInputs := 0
	for edgeIndex, edge := range r.pipe.Edges {
		if edge.To == node.ID && edgeStates[edgeIndex] == edgeActive {
			activeInputs++
		}
	}
	streamable := r.streamEligible(node, outputs) && activeInputs <= 1

	// Find input data from connected upstream nodes. nodeOutputs is
	// internally synchronized, so no lock is held here.
	var input *common.DataSet
	var inputRef *artifact.DatasetRef
	var allInputs []*common.DataSet
	// edgeInputsByFrom indexes the same upstream outputs by upstream node
	// ID, alongside input/allInputs above — needed by dynamic-expansion
	// nodes (#31) to resolve expansion.over[param], which names upstream
	// nodes by ID rather than relying on edge order the way allInputs does.
	edgeInputsByFrom := make(map[string]*common.DataSet)
	for edgeIndex, edge := range r.pipe.Edges {
		if edge.To == node.ID && edgeStates[edgeIndex] == edgeActive {
			if streamable && inputRef == nil {
				if ref, ok := outputs.GetRef(edge.From); ok {
					// Held by reference: flow it through without ever
					// decoding the whole thing. Inline (small) inputs
					// fall through to Get below — batch is faster and
					// their memory doesn't matter, per the ADR's
					// engagement rule.
					inputRef = ref
					continue
				}
			}
			ds, ok, err := outputs.Get(edge.From)
			if err != nil {
				// The upstream output was spilled and cannot be read back.
				// Continuing would hand this node an empty input, which is
				// the silent data loss Tnsor-Labs/brokoli#8 fixed; fail
				// instead.
				return nil, fmt.Errorf("resolve input from node %s: %w", edge.From, err)
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
	// A transform only streams FROM a ref; a code node also streams with
	// no input at all (its output side is the win — the benchmark's own
	// generator node is exactly this shape). Everything else needs the
	// materialized input it just got.
	if streamable {
		switch node.Type {
		case models.NodeTypeTransform:
			streamable = inputRef != nil
		case models.NodeTypeCode:
			streamable = inputRef != nil || (activeInputs == 0 && input == nil)
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
	backoffType := "exponential"
	if bt, ok := node.Config["retry_backoff"].(string); ok && bt != "" {
		backoffType = bt
	}
	// brokoli#138: retry_backoff used to be accepted and compiled but never
	// read here -- every node retried with a hardcoded exponential formula
	// regardless of what was configured. ComputeBackoff (engine/retry.go)
	// already implements exponential/linear/fixed; this just wires it in,
	// preserving today's max_retries/retry_delay semantics (opt-in via
	// explicit config, no per-node-type auto-retry defaults) and the
	// existing 60s cap exactly.
	retryCfg := RetryConfig{BackoffType: backoffType, BackoffBase: baseDelay, BackoffMax: 60 * time.Second}
	nodeTimeout := 30 * time.Minute
	if t, ok := node.Config["timeout"].(float64); ok && t > 0 {
		nodeTimeout = time.Duration(t) * time.Second
	}

	type nodeResult struct {
		result nodeExecutionResult
		err    error
	}

	r.emit(models.Event{Type: models.EventNodeStarted, RunID: r.run.ID, NodeID: node.ID})

	var output *common.DataSet
	var outputRef *artifact.DatasetRef
	var conditionDecision *bool
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			delay := ComputeBackoff(retryCfg, attempt)
			r.logWithTrace(node.ID, models.LogLevelWarning, "", attempt, nil,
				"Retry %d/%d in %v (%s backoff)", attempt, maxRetries, delay, backoffType)
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
				return nil, fmt.Errorf("cancelled during retry wait")
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
		idempotencyKey := nodeAttemptIdempotencyKey(r.run.ID, node.ID, attempt)

		// execAttemptStore is the optional store.ExecutionAttemptStore
		// capability (Tnsor-Labs/brokoli#90 M3) — nil on a store that
		// doesn't implement it (e.g. an older EE APIStore), in which case
		// this attempt's claim/lease/fencing bookkeeping below is simply
		// skipped and node execution behaves exactly as it always has.
		// execFencingGen is only meaningful while execAttemptStore != nil.
		var execAttemptStore store.ExecutionAttemptStore
		var execFencingGen int64
		if as, ok := r.store.(store.ExecutionAttemptStore); ok && !r.dryRun {
			execAttemptStore = as
		}

		crashAt(crashPointBeforeNodeAttemptCreate, node.ID)
		if !r.dryRun {
			// CreateNodeRun is the durable attempt record: no external
			// side effect (runNodeLogic below) is permitted to start until
			// it is persisted. Matches the already-correct run-level
			// pattern (CreateRun above checks and propagates); previously
			// this error was discarded entirely.
			//
			// When the store also supports ExecutionAttemptStore, the
			// node-level outbox/claim row is created in the SAME
			// transaction as the NodeRun — mirroring the pipeline-level
			// outbox pattern in Engine.RunPipelineAsync (see
			// models.ExecutionAttempt's doc comment: this is that pattern's
			// "natural next caller"). It starts Queued; ClaimAttempt right
			// after commit is this Runner claiming its own just-created
			// attempt, since there is no separate worker dequeue step yet.
			if execAttemptStore != nil {
				execAttempt := &models.ExecutionAttempt{
					RunID:          r.run.ID,
					NodeID:         node.ID,
					Attempt:        attempt,
					Status:         models.AttemptStatusQueued,
					IdempotencyKey: idempotencyKey,
				}
				if err := r.store.WithTx(func(tx *sql.Tx) error {
					if err := r.store.CreateNodeRunTx(tx, nr); err != nil {
						return err
					}
					return execAttemptStore.CreateExecutionAttemptTx(tx, execAttempt)
				}); err != nil {
					return nil, fmt.Errorf("persist node attempt for %s (attempt %d): %w", node.Name, attempt, err)
				}
				// Short lease (store.DefaultLeaseDuration/RenewInterval —
				// the same 15s/5s tradeoff already accepted for scheduler
				// leader election, store/postgres_leader.go) kept alive by
				// a renewal goroutine below for as long as this attempt
				// actually runs, rather than one long lease sized to
				// nodeTimeout: a genuinely crashed process stops renewing
				// and its lease is reclaimable within ~15s regardless of
				// how long the node's own timeout is, instead of leaving
				// reconcileExecutionAttempts (engine/recovery.go) deferring
				// to it for the whole nodeTimeout window.
				gen, ok, err := execAttemptStore.ClaimAttempt(r.run.ID, node.ID, "", attempt, r.instanceID, store.DefaultLeaseDuration)
				if err != nil {
					return nil, fmt.Errorf("claim execution attempt for %s (attempt %d): %w", node.Name, attempt, err)
				}
				if !ok {
					// A just-created Queued row must be claimable; failure
					// here means something else already claimed or settled
					// it, which should never happen for in-process dispatch
					// and indicates a genuine correctness violation worth
					// surfacing loudly rather than silently proceeding
					// without a valid lease.
					return nil, fmt.Errorf("claim execution attempt for %s (attempt %d): already claimed or terminal", node.Name, attempt)
				}
				execFencingGen = gen
			} else {
				if err := r.store.CreateNodeRun(nr); err != nil {
					return nil, fmt.Errorf("persist node attempt for %s (attempt %d): %w", node.Name, attempt, err)
				}
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

		if execAttemptStore != nil {
			// AckAttempt records that this Runner is about to actually
			// start runNodeLogic under the lease it holds — the claimed→
			// started transition models.AttemptStatus documents. Fencing-
			// checked against execFencingGen for the same reason ClaimAttempt
			// itself is: if this ever fails, some other claimant already
			// holds the lease, which must not be silently overridden.
			if err := execAttemptStore.AckAttempt(r.run.ID, node.ID, "", attempt, r.instanceID, execFencingGen); err != nil {
				attemptCancel()
				return nil, fmt.Errorf("ack execution attempt for %s (attempt %d): %w", node.Name, attempt, err)
			}
			// Keep the short lease alive for as long as runNodeLogic is
			// actually still executing, exactly like PostgresLeaderElector.Run
			// renews leadership (store/postgres_leader.go): renew on a tick
			// well below the lease duration so one missed tick doesn't cost
			// the lease, and stop the instant attemptCtx ends (success,
			// failure, or timeout — attemptCancel() below covers all three),
			// so a real process crash simply stops renewing and the lease
			// expires on its own.
			go r.renewAttemptLease(attemptCtx, execAttemptStore, node.ID, "", attempt, execFencingGen)
		}

		resultCh := make(chan nodeResult, 1)
		go func() {
			crashAt(crashPointBeforeNodeExecution, node.ID)
			var result nodeExecutionResult
			var e error
			if streamable {
				result, e = r.runNodeStreamed(node, inputRef, outputs)
			} else {
				result, e = r.runNodeLogic(node, input, allInputs, edgeInputsByFrom, attempt, idempotencyKey, attemptCtx)
			}
			resultCh <- nodeResult{result, e}
		}()

		var err error
		select {
		case result := <-resultCh:
			output, outputRef, conditionDecision, err = result.result.output, result.result.outputRef, result.result.condition, result.err
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
		if err == nil && r.pipe.IRVersion == models.ConditionalEdgesIRVersion && node.Type == models.NodeTypeCondition && conditionDecision == nil {
			err = fmt.Errorf("condition node completed without a routing decision")
		}

		if err == nil {
			// ── Success ──
			r.saveNodeProfile(node.ID, output, outputRef)
			rowCount := 0
			if output != nil {
				rowCount = len(output.Rows)
			} else if outputRef != nil {
				rowCount = int(outputRef.RowCount)
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
				attemptNum := attempt
				completedEvent := models.RunEvent{
					RunID:     r.run.ID,
					NodeID:    node.ID,
					Attempt:   &attemptNum,
					EventType: models.AttemptCompleted,
					Payload: models.RunEventPayload{
						Status:          models.RunStatusSuccess,
						NodeRunID:       nr.ID,
						RowCount:        nr.RowCount,
						DurationMs:      nr.DurationMs,
						RowsPerSec:      nr.RowsPerSec,
						ConditionResult: conditionDecision,
					},
				}
				if r.pipe.IRVersion == models.ConditionalEdgesIRVersion && node.Type == models.NodeTypeCondition {
					if err := r.store.WithTx(func(tx *sql.Tx) error {
						if err := r.store.UpdateNodeRunTx(tx, nr); err != nil {
							return err
						}
						return r.store.AppendEventTx(tx, &completedEvent)
					}); err != nil {
						attemptSpan.RecordError(err)
						attemptSpan.SetStatus(codes.Error, err.Error())
						attemptSpan.End()
						return nil, fmt.Errorf("persist successful condition %s with routing decision: %w", node.Name, err)
					}
					r.metrics.incrEventsAppended()
				} else {
					// If the success can't be durably recorded, the node is not
					// considered done — same contract as CreateNodeRun above.
					if err := r.store.UpdateNodeRun(nr); err != nil {
						attemptSpan.RecordError(err)
						attemptSpan.SetStatus(codes.Error, err.Error())
						attemptSpan.End()
						return nil, fmt.Errorf("persist successful node attempt for %s (attempt %d): %w", node.Name, attempt, err)
					}
					r.appendEvent(completedEvent)
				}
				if execAttemptStore != nil {
					// Settle the claim/lease record BEFORE
					// crashPointAfterNodeCompletionPersist, not after: the
					// NodeRun success above and the attempt's terminal state
					// must agree by the time that boundary is reached, or a
					// crash landing exactly there leaves a non-terminal,
					// still-live-leased attempt that startup recovery then
					// correctly (but here undesirably) defers to instead of
					// reconciling — see TestCrashRecoveryBaseline. Non-fatal:
					// the NodeRun row is still the authoritative success
					// record even if this best-effort call fails.
					if err := execAttemptStore.CompleteAttempt(r.run.ID, node.ID, "", attempt, execFencingGen); err != nil {
						r.log(node.ID, models.LogLevelWarning, "Failed to settle execution attempt %d as completed (node already succeeded; harmless — recovery reclaims it): %v", attempt, err)
					}
				}
			}
			crashAt(crashPointAfterNodeCompletionPersist, node.ID)

			if attempt > 0 {
				r.logWithTrace(node.ID, models.LogLevelInfo, spanID, attempt, nil,
					"Succeeded after %d retries", attempt)
			}

			// Store output — reference form first (ADR-019 Milestone 1):
			// the ref goes into nodeOutputs for downstream consumers
			// (materialized on demand for batch ones, streamed for
			// stream-capable ones), the preview keeps only the rows it
			// would have kept anyway, and the resume artifact is recorded
			// without materializing when the store supports it.
			if outputRef != nil && !r.dryRun {
				outputs.PutRef(node.ID, outputRef)
				preview, perr := previewFromRef(outputs, outputRef, 50)
				if perr != nil {
					attemptSpan.RecordError(perr)
					attemptSpan.SetStatus(codes.Error, perr.Error())
					attemptSpan.End()
					return nil, fmt.Errorf("persist node preview for %s (attempt %d): %w", node.Name, attempt, perr)
				}
				if err := r.store.SaveNodePreview(r.run.ID, node.ID, preview.Columns, preview.Rows); err != nil {
					attemptSpan.RecordError(err)
					attemptSpan.SetStatus(codes.Error, err.Error())
					attemptSpan.End()
					return nil, fmt.Errorf("persist node preview for %s (attempt %d): %w", node.Name, attempt, err)
				}
				if r.artifactStore != nil && !nonResumableNodeTypes[node.Type] {
					var aerr error
					if refWriter, ok := r.artifactStore.(RefArtifactWriter); ok {
						aerr = refWriter.WriteArtifactRef(r.run.ID, node.ID, "", outputRef)
					} else {
						// Store can't record refs: materialize once for the
						// artifact write — today's memory cost, only for
						// such stores, only at this boundary.
						ds, _, gerr := outputs.Get(node.ID)
						if gerr != nil {
							aerr = gerr
						} else {
							aerr = r.artifactStore.WriteArtifact(r.run.ID, node.ID, "", ds)
						}
					}
					if aerr != nil {
						r.log(node.ID, models.LogLevelWarning, "Failed to persist durable artifact for node %s (a future resume needing its output will fail loudly): %v", node.Name, aerr)
					}
				}
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
						return nil, fmt.Errorf("persist node preview for %s (attempt %d): %w", node.Name, attempt, err)
					}
					// Durable full-output artifact — distinct from
					// SaveNodePreview above, which truncates to 50 rows for
					// UI display and is not sufficient to correctly resume
					// a downstream node. See ArtifactStore and
					// docs/adr/010-run-artifact-and-resume-semantics.md.
					// Node types in nonResumableNodeTypes never get an
					// artifact even on success — see that var's doc comment.
					if r.artifactStore != nil && !nonResumableNodeTypes[node.Type] {
						if err := r.artifactStore.WriteArtifact(r.run.ID, node.ID, "", output); err != nil {
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
			} else if outputRef != nil && len(outputRef.Columns) > 0 {
				colInfo = fmt.Sprintf(", columns: [%s]", truncateList(outputRef.Columns, 8))
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
				return nil, fmt.Errorf("node %s (%s) failed: %v; persist failed node attempt (attempt %d): %w", node.Name, node.ID, err, attempt, persistErr)
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
			if execAttemptStore != nil {
				// Same non-fatal treatment as CompleteAttempt above: the
				// NodeRun row is already the durable record of this
				// attempt's failure; this settles the supplementary
				// claim/lease record to match, best-effort.
				if failErr := execAttemptStore.FailAttempt(r.run.ID, node.ID, "", attempt, execFencingGen, err.Error()); failErr != nil {
					r.log(node.ID, models.LogLevelWarning, "Failed to settle execution attempt %d as failed (node already marked failed; harmless — recovery reclaims it): %v", attempt, failErr)
				}
			}
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
		return nil, fmt.Errorf("node %s (%s) failed: %w", node.Name, node.ID, lastErr)
	}
	return conditionDecision, nil
}

// saveNodeProfile makes runtime schema and profile evidence available to
// lineage consumers. Profiling is intentionally bounded to a sample so a
// large successful node cannot turn observability into a second full scan.
func (r *Runner) saveNodeProfile(nodeID string, output *common.DataSet, outputRef *artifact.DatasetRef) {
	profiles, ok := r.store.(store.NodeProfileStore)
	if !ok || r.dryRun {
		return
	}
	var profile *DataProfile
	if output != nil {
		sample := *output
		if len(sample.Rows) > 10000 {
			sample.Rows = sample.Rows[:10000]
		}
		profile = ProfileDataSet(&sample)
		profile.RowCount = len(output.Rows)
	} else if outputRef != nil {
		profile = &DataProfile{RowCount: int(outputRef.RowCount), ColumnCount: len(outputRef.Columns)}
		for _, column := range outputRef.Columns {
			profile.Columns = append(profile.Columns, ColumnProfile{Name: column, Type: "unknown"})
		}
	}
	if profile == nil {
		return
	}
	schema := ExtractSchema(profile)
	profileJSON, profileErr := json.Marshal(profile)
	schemaJSON, schemaErr := json.Marshal(schema)
	if profileErr != nil || schemaErr != nil {
		return
	}
	if err := profiles.SaveNodeProfile(r.run.ID, nodeID, string(profileJSON), string(schemaJSON), "[]"); err != nil {
		r.logWithTrace(nodeID, models.LogLevelDebug, "", 0, nil, "lineage profile unavailable: %v", err)
	}
}

// renewAttemptLease keeps a node attempt's short execution-attempt lease
// alive for as long as it is genuinely still executing (Tnsor-Labs/brokoli#90
// M3), renewing on store.DefaultRenewInterval — well below
// store.DefaultLeaseDuration, so one missed tick under load doesn't cost
// the lease — until ctx is done (the attempt finished or timed out, see
// attemptCtx/attemptCancel in executeNode). A renewal failure means the
// fencing generation was already reclaimed by someone else (only possible
// today via startup recovery deciding this process looked dead); it is
// logged and this goroutine simply stops — the attempt's own
// Complete/FailAttempt call after runNodeLogic returns will then correctly
// fail its own fencing check and get logged the same non-fatal way, rather
// than this goroutine trying to fail the run out-of-band.
func (r *Runner) renewAttemptLease(ctx context.Context, attemptStore store.ExecutionAttemptStore, nodeID, instanceKey string, attempt int, fencingGen int64) {
	ticker := time.NewTicker(store.DefaultRenewInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ok, err := attemptStore.RenewLease(r.run.ID, nodeID, instanceKey, attempt, r.instanceID, fencingGen, store.DefaultLeaseDuration)
			if err != nil || !ok {
				r.log(nodeID, models.LogLevelWarning, "Failed to renew execution attempt lease for attempt %d (ok=%v): %v — a startup recovery pass may have reclaimed it", attempt, ok, err)
				return
			}
		}
	}
}

func (r *Runner) persistSkippedNode(node models.Node, readyAt time.Time) error {
	r.log(node.ID, models.LogLevelInfo, "Skipping node %s: all incoming edges are inactive", node.Name)
	return r.persistSkippedOutcome(node, readyAt, false, nil, "")
}

func (r *Runner) persistSkippedOutcome(node models.Node, readyAt time.Time, reused bool, conditionResult *bool, artifactRunID string) error {
	if r.dryRun {
		if r.dryRunResults == nil {
			r.dryRunResults = make(map[string]*DryRunNodeResult)
		}
		r.dryRunResults[node.ID] = &DryRunNodeResult{NodeID: node.ID, Name: node.Name, Status: string(models.RunStatusSkipped)}
		return nil
	}

	nr := &models.NodeRun{
		ID:      common.NewID(),
		RunID:   r.run.ID,
		NodeID:  node.ID,
		Status:  models.RunStatusSkipped,
		Attempt: 0,
		ReadyAt: &readyAt,
		TraceID: r.traceID,
	}
	attempt := 0
	event := models.RunEvent{
		RunID:     r.run.ID,
		NodeID:    node.ID,
		Attempt:   &attempt,
		EventType: models.AttemptSkipped,
		Payload: models.RunEventPayload{
			Status:          models.RunStatusSkipped,
			NodeRunID:       nr.ID,
			ReadyAt:         nr.ReadyAt,
			TraceID:         nr.TraceID,
			ConditionResult: conditionResult,
			Reused:          reused,
			ArtifactRunID:   artifactRunID,
		},
	}
	if err := r.store.WithTx(func(tx *sql.Tx) error {
		if err := r.store.CreateNodeRunTx(tx, nr); err != nil {
			return err
		}
		return r.store.AppendEventTx(tx, &event)
	}); err != nil {
		return fmt.Errorf("persist skipped node %s: %w", node.Name, err)
	}
	r.metrics.incrEventsAppended()
	r.emit(models.Event{Type: models.EventNodeCompleted, RunID: r.run.ID, NodeID: node.ID, Status: models.RunStatusSkipped})
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

	sourceRunID := r.artifactSourceRunIDs[node.ID]
	if sourceRunID == "" {
		sourceRunID = r.resumedFromRunID
	}
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

	ds, readErr := r.artifactStore.ReadArtifact(sourceRunID, node.ID, "")
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

func outputExecutionResult(output *common.DataSet, err error) (nodeExecutionResult, error) {
	return nodeExecutionResult{output: output}, err
}

func (r *Runner) runNodeLogic(node models.Node, input *common.DataSet, allInputs []*common.DataSet, edgeInputsByFrom map[string]*common.DataSet, attempt int, idempotencyKey string, ctx context.Context) (nodeExecutionResult, error) {
	// Check if the org's plan allows this node type. Deliberately checked
	// before the executor loop below, not after: a NodeExecutor can claim
	// any node type string (a plugin-registered type, an enterprise K8s-
	// dispatched type, etc.), and this gate is meant to apply uniformly to
	// every node type regardless of which mechanism ultimately executes
	// it — a gate that only fired for the built-in switch further down
	// would silently never apply to executor-dispatched types at all.
	if extensions.NodeTypeGateFunc != nil {
		if msg := extensions.NodeTypeGateFunc(r.pipe.OrgID, string(node.Type)); msg != "" {
			return nodeExecutionResult{}, fmt.Errorf("%s", msg)
		}
	}

	// Branch selection is control-plane behavior owned by the Go engine.
	// External executors return data only and cannot replace this decision.
	if node.Type == models.NodeTypeCondition {
		output, selected, err := r.runCondition(node, input)
		if err != nil {
			return nodeExecutionResult{}, err
		}
		if r.pipe.IRVersion != models.ConditionalEdgesIRVersion && !selected {
			output = &common.DataSet{Columns: []string{}, Rows: []common.DataRow{}}
		}
		return nodeExecutionResult{output: output, condition: &selected}, nil
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
				return nodeExecutionResult{}, err
			}
			for _, logLine := range result.Logs {
				if logLine != "" {
					r.log(node.ID, models.LogLevelInfo, "[%s] %s", exec.Name(), logLine)
				}
			}
			if result.OutputData != nil {
				if ds, ok := result.OutputData.(*common.DataSet); ok {
					return nodeExecutionResult{output: ds}, nil
				}
			}
			return nodeExecutionResult{}, nil
		}
	}
	if !IsBuiltInNodeType(node.Type) {
		return nodeExecutionResult{}, fmt.Errorf("unsupported node type %q", node.Type)
	}

	switch node.Type {
	case models.NodeTypeSourceFile:
		return outputExecutionResult(r.runSourceFile(node))
	case models.NodeTypeSourceAPI:
		return outputExecutionResult(r.runSourceAPI(node))
	case models.NodeTypeSourceDB:
		return outputExecutionResult(r.runSourceDB(node))
	case models.NodeTypeTransform:
		return outputExecutionResult(r.runTransform(node, input))
	case models.NodeTypeQualityCheck:
		return outputExecutionResult(r.runQualityCheck(node, input))
	case models.NodeTypeCode:
		// Dynamic node expansion (#31): a `code` node carrying an
		// `expansion` config block (brokoli-sdk's _TaskWrapper.expand())
		// fans out into one execution per item instead of running once —
		// see engine/expansion.go's top-of-file doc comment for the full
		// architectural resolution.
		if nodeHasExpansion(node) {
			return outputExecutionResult(r.runCodeExpansion(node, edgeInputsByFrom, attempt))
		}
		return outputExecutionResult(r.runCode(node, input))
	case models.NodeTypeJoin:
		return outputExecutionResult(r.runJoin(node, allInputs))
	case models.NodeTypeSQLGenerate:
		return outputExecutionResult(r.runSQLGenerate(node, input))
	case models.NodeTypeSinkFile:
		return outputExecutionResult(r.runSinkFile(node, input))
	case models.NodeTypeSinkDB:
		return outputExecutionResult(r.runSinkDB(node, input))
	case models.NodeTypeSinkAPI:
		return outputExecutionResult(r.runSinkAPI(node, input))
	case models.NodeTypeMigrate:
		return outputExecutionResult(r.runMigrate(node))
	case models.NodeTypeDBT:
		return outputExecutionResult(r.runDBT(node))
	case models.NodeTypeNotify:
		return outputExecutionResult(r.runNotify(node, input))
	case models.NodeTypeUnion:
		return outputExecutionResult(r.runUnion(node, allInputs))
	case models.NodeTypeDatasetMap:
		return outputExecutionResult(r.runDatasetMap(node, input))
	case models.NodeTypeDatasetFilter:
		return outputExecutionResult(r.runDatasetFilter(node, input))
	default:
		return nodeExecutionResult{}, fmt.Errorf("built-in node type %q has no runtime handler", node.Type)
	}
}

// cancelIntentPollInterval is how often the watcher below re-reads the
// run's durable cancel flag while nodes execute. A var so tests can
// shrink it; 5s keeps a cancelled run's worst-case convergence in
// single-digit seconds at the cost of one primary-key read per interval
// per in-flight run.
var cancelIntentPollInterval = 5 * time.Second

// startCancelIntentWatcher polls the run's durable cancel_requested flag
// WHILE nodes execute, cancelling the run context the moment it appears.
// Returns a stop func for the caller to defer.
//
// This is the mid-node half of the durable-intent design. The wave-
// boundary check catches a lost cancel between nodes, but a single-node
// pipeline mid-execution has no boundary — found live when a Redis
// restart killed every pod's relay subscription (see brokoli-ee's relay
// resilience fix): cancels returned 200, intent was durably recorded,
// and a 45-second single-node run still completed because nothing looked
// at the flag until the run was already over. With the watcher, ANY run
// converges within cancelIntentPollInterval of the intent landing, no
// matter what the relay transport is doing and no matter the pipeline's
// shape — the relay becomes a latency optimization, not a correctness
// dependency.
func (r *Runner) startCancelIntentWatcher() func() {
	if r.run == nil || r.store == nil {
		return func() {}
	}
	runID := r.run.ID
	done := make(chan struct{})
	// Read the interval before spawning: the goroutine can outlive its
	// Execute call by a scheduling beat, and tests override the package
	// var between sequential runs — an async read would race that write.
	interval := cancelIntentPollInterval
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-r.ctx.Done():
				return
			case <-ticker.C:
				if stored, err := r.store.GetRun(runID); err == nil && stored.CancelRequested {
					r.Cancel()
					return
				}
			}
		}
	}()
	return func() { close(done) }
}

// reclaimTransientBlobs deletes the run's blob scratch namespace when the
// run is terminal and the artifact store declares the namespace transient
// (TransientBlobJanitor). Best-effort: a failure here costs disk, never
// correctness, so it logs and moves on.
func (r *Runner) reclaimTransientBlobs() {
	if r.run == nil || r.artifactStore == nil {
		return
	}
	if !isTerminalRunStatus(r.run.Status) {
		return
	}
	janitor, ok := r.artifactStore.(TransientBlobJanitor)
	if !ok {
		return
	}
	if err := janitor.DeleteTransientBlobs(r.run.ID); err != nil {
		common.SLog().Warn("transient blob reclaim failed",
			common.RunAttr(r.run.ID), "error", err)
	}
}

// finalizeCancelled persists the run as cancelled and emits the cancellation
// event, hook, and log line. It returns the "pipeline cancelled" error the
// caller should propagate as Execute's error.
func (r *Runner) finalizeCancelled() error {
	finishTime := time.Now().UTC()
	r.run.Status = models.RunStatusCancelled
	r.run.FinishedAt = &finishTime
	if err := r.store.UpdateRun(r.run); err != nil {
		return fmt.Errorf("persist cancelled run: %w", err)
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
	return fmt.Errorf("pipeline cancelled")
}

func (r *Runner) failRun(err error) error {
	// Cancellation wins over failure, no matter which exit path noticed the
	// node error first. When CancelRun fires while a node is in flight, the
	// node fails with "pipeline cancelled" and the wave loop exits through
	// here — without this guard the run would be persisted as FAILED (plus
	// DLQ entry, failure alert, and critical notification) whenever this
	// goroutine won the UpdateRun race against CancelRun's own cancelled
	// write. r.ctx is only ever cancelled by Runner.Cancel (it has no
	// deadline: parentCtx is a span context over context.Background()), so
	// ctx.Err() != nil here means exactly "the user cancelled this run".
	if r.ctx.Err() != nil {
		return r.finalizeCancelled()
	}
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
// savePhysicalPlan computes and persists the run's physical plan, best
// effort. Skips silently if the store doesn't implement the optional
// PhysicalPlanStore capability, if planning fails (the executable
// validator already rejects unplannable graphs at save time), or if the
// write fails — none of which should abort a run.
func (r *Runner) savePhysicalPlan() {
	pp, ok := r.store.(store.PhysicalPlanStore)
	if !ok {
		return
	}
	plan, err := PlanPipeline(r.pipe)
	if err != nil {
		return
	}
	planJSON, err := json.Marshal(plan)
	if err != nil {
		return
	}
	if err := pp.SaveRunPlan(r.run.ID, string(planJSON)); err != nil {
		r.log("", models.LogLevelWarning, "could not persist physical plan: %v", err)
	}
}

// savePhysicalInstances persists the run's physical instances, best
// effort. Runs at Execute exit (deferred), by which point node_runs and
// any expansion_instances are written, so the projection is complete.
// Skips silently if the store lacks the optional capability or the run
// row never got created.
func (r *Runner) savePhysicalInstances() {
	pi, ok := r.store.(store.PhysicalInstanceStore)
	if !ok || r.run == nil {
		return
	}
	instances, err := ProjectRunInstances(r.store, r.run.ID)
	if err != nil || len(instances) == 0 {
		return
	}
	if err := pi.SavePhysicalInstances(r.run.ID, instances); err != nil {
		r.log("", models.LogLevelWarning, "could not persist physical instances: %v", err)
	}
}

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
