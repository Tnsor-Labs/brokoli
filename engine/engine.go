package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
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

// ErrInvalidQueuedRun identifies a malformed or orphaned queue delivery that
// cannot become valid through retry.
var ErrInvalidQueuedRun = errors.New("invalid queued run")

// Engine manages pipeline execution and event broadcasting.
type Engine struct {
	store         store.Store
	eventCh       chan models.Event
	mu            sync.RWMutex
	active        map[string]*Runner
	maxConcurrent int
	runSem        chan struct{}
	VarStore      VariableStore                   // for resolving ${var.key}
	ConnResolver  *ConnectionResolver             // for resolving conn_id → URI
	Executors     []extensions.NodeExecutor       // enterprise: K8s, Docker, etc.
	Notifier      extensions.NotificationProvider // enterprise: Slack, PagerDuty, etc.
	JobQueue      extensions.JobQueue             // nil = run in-process (default)

	// InstanceJobQueue (ADR-017, worker protocol v2 — proposed) opts a
	// node's dynamic-expansion instances into remote dispatch: instead of
	// executing an item in-process, the Runner enqueues a WorkOrder-bearing
	// extensions.RunJob and waits for a remote worker to complete it (see
	// that ADR's 2026-08-12 wait-and-fetch design). Deliberately a separate
	// field from JobQueue, not a reuse of it — a deployment already running
	// distributed whole-pipeline dispatch via JobQueue must not silently
	// start attempting instance-level remote dispatch the moment this
	// ships. Nil by default (the case everywhere today): every expansion
	// instance executes locally exactly as before, unconditionally.
	InstanceJobQueue extensions.JobQueue

	// CancelRelay broadcasts cancellation requests for runs executing in
	// OTHER engine instances (see extensions.RunCancelRelay). Nil by
	// default: a single-process deployment cancels every run locally and
	// needs no transport. When set, the process must also subscribe its
	// transport to deliver received run IDs to CancelRelayedRun.
	CancelRelay extensions.RunCancelRelay

	// ArtifactStore persists durable node-output artifacts so ResumeRun can
	// restore a skipped node's real prior output (Tnsor-Labs/brokoli#8).
	// Defaults to a LocalDiskArtifactStore rooted at BROKOLI_ARTIFACT_DIR
	// (or ./brokoli-artifacts); assign a different implementation the same
	// way VarStore/ConnResolver are overridden after NewEngine.
	ArtifactStore ArtifactStore
	// SpillThresholdBytes is the estimated encoded size at or above which a
	// node's output is written to ArtifactStore instead of being held in
	// memory for the rest of the run (Tnsor-Labs/brokoli#38). Defaults to
	// DefaultSpillThresholdBytes, overridable via BROKOLI_SPILL_THRESHOLD_BYTES
	// or by assigning after NewEngine. A negative value disables spilling
	// and restores the previous always-in-memory behaviour exactly.
	SpillThresholdBytes int64
	// StreamThresholdBytes is the encoded size at or above which ADR-019's
	// reference-passing paths engage — a code node's output file becomes a
	// blob reference instead of materializing, which is what lets the next
	// stream-capable node process it in bounded memory. Deliberately a
	// SEPARATE, much lower knob than SpillThresholdBytes: spilling parks an
	// already-materialized output (the memory was already paid, disk I/O
	// only buys back the steady state), while streaming prevents the
	// memory from ever being paid — measured live, this workload's NDJSON
	// encodes ~5-6x smaller than its map-row in-memory form, so a dataset
	// far below the spill threshold's 64 MiB is still worth streaming.
	// Defaults to DefaultStreamThresholdBytes, overridable via
	// BROKOLI_STREAM_THRESHOLD_BYTES or by assigning after NewEngine. A
	// negative value disables reference-passing entirely (every node takes
	// its pre-ADR-019 batch path).
	StreamThresholdBytes int64
	// PaginationCheckpointStore persists mid-pagination progress for
	// source_api nodes so a retry or resume can continue a large paginated
	// fetch instead of restarting it (Tnsor-Labs/brokoli#41 M2). Defaults to
	// a LocalDiskPaginationCheckpointStore rooted at
	// BROKOLI_PAGINATION_CHECKPOINT_DIR (or ./brokoli-pagination-checkpoints);
	// overridden the same way ArtifactStore is.
	PaginationCheckpointStore PaginationCheckpointStore

	// InstanceID identifies this engine process as a claimant when it wires
	// node execution through store.ExecutionAttemptStore's claim/lease/
	// fencing contract (Tnsor-Labs/brokoli#90 M3). It is not a leader or
	// worker-pool identity — just a stable value to pass as ClaimAttempt's
	// claimedBy for every attempt this process's Runners claim, so a lease
	// left behind by a dead process is visibly attributable on inspection.
	// Generated once per NewEngine call; never persisted or read back.
	InstanceID string

	// shutdown is closed by Close. Background goroutines the engine starts
	// after this point are refused, and run dispatch returns
	// ErrEngineClosed. bg counts every goroutine whose lifetime the engine
	// owns — run executors and trigger-mode fan-out — so Close can wait
	// for them instead of leaving them writing into stores and directories
	// that are being torn down (Tnsor-Labs/brokoli#94).
	shutdown  chan struct{}
	closeOnce sync.Once
	bg        sync.WaitGroup

	RunsTotal     int64
	RunsSucceeded int64
	RunsFailed    int64

	// Startup-recovery counters (Tnsor-Labs/brokoli#9), incremented by
	// RecoverNonTerminalRuns via atomic.AddInt64 exactly like RunsTotal/
	// RunsSucceeded/RunsFailed above, and surfaced by
	// api.PrometheusHandler the same way those already are — recovery does
	// not get its own metrics type, it extends this engine-level counter
	// set, which is what the Prometheus handler already reads directly.
	//
	// RunsRecovered counts runs recovery determined a definite outcome for
	// (success or failed) from the event log, whether or not it also had to
	// reclaim an expired attempt lease along the way; RunsRecoveryFailed
	// counts only the subset with no recoverable path at all, forced to
	// models.RunStatusFailed for lack of any way to tell what the live
	// process would have persisted; AttemptsReclaimed counts
	// execution_attempts rows whose expired lease was settled via
	// FailAttempt.
	RunsRecovered      int64
	RunsRecoveryFailed int64
	AttemptsReclaimed  int64

	// The counters below fill the remaining metrics gaps named by
	// Tnsor-Labs/brokoli#11: event append/replay (Tnsor-Labs/brokoli#6) and
	// node-attempt/queue-depth (Tnsor-Labs/brokoli#7). They extend the same
	// atomic.AddInt64-updated-int64-field pattern already used above by
	// RunsTotal/RunsSucceeded/RunsFailed and the recovery counters — see
	// api.PrometheusHandler, which reads all of these directly at scrape
	// time exactly like it already does for RunsRecovered etc.
	//
	// EventsAppended/EventsAppendFailed count calls to AppendEvent from
	// Engine.appendEvent and Runner.appendEvent (the run/node lifecycle
	// event log write path). EventsReplayed counts individual events
	// consumed by ProjectRun's replay/projection logic (currently only
	// invoked by startup recovery — see recoverRun in recovery.go).
	EventsAppended     int64
	EventsAppendFailed int64
	EventsReplayed     int64

	// AttemptsStarted/AttemptsSucceeded/AttemptsFailed/AttemptsRetried
	// count node-level execution attempts — the CreateNodeRun/UpdateNodeRun
	// lifecycle in Runner.executeNode — mirroring RunsTotal/RunsSucceeded/
	// RunsFailed above but at node-attempt granularity. Updated via the
	// *runnerMetrics pointers handed to each Runner (see runnerMetrics
	// below); nil-safe, so a Runner constructed without one (e.g. DryRun)
	// simply skips these increments.
	AttemptsStarted   int64
	AttemptsSucceeded int64
	AttemptsFailed    int64
	AttemptsRetried   int64

	// RunsQueueWaiting is a gauge: the number of RunPipeline/
	// RunPipelineAsync/ExecuteQueuedRun callers currently blocked acquiring
	// a concurrency slot from runSem — i.e. dispatch backlog depth, not
	// currently exposed anywhere (brokoli_active_runs only reports runs
	// that already acquired a slot and are executing). Incremented
	// immediately before, decremented immediately after, each `e.runSem <-
	// struct{}{}` send.
	RunsQueueWaiting int64
}

// runnerMetrics carries pointers to the Engine-owned counters a Runner
// needs to update directly (Tnsor-Labs/brokoli#11's event-append and
// node-attempt metrics) without holding a reference to the whole Engine.
// Assigned at every NewRunner call site exactly like runner.artifactStore =
// e.ArtifactStore already is; nil-safe throughout (see incrMetric in
// runner.go) so a Runner built without one — DryRun, or a test constructing
// a Runner directly via NewRunner — simply skips these increments, the same
// way a nil artifactStore already skips artifact writes.
type runnerMetrics struct {
	attemptsStarted    *int64
	attemptsSucceeded  *int64
	attemptsFailed     *int64
	attemptsRetried    *int64
	eventsAppended     *int64
	eventsAppendFailed *int64
}

func (e *Engine) newRunnerMetrics() *runnerMetrics {
	return &runnerMetrics{
		attemptsStarted:    &e.AttemptsStarted,
		attemptsSucceeded:  &e.AttemptsSucceeded,
		attemptsFailed:     &e.AttemptsFailed,
		attemptsRetried:    &e.AttemptsRetried,
		eventsAppended:     &e.EventsAppended,
		eventsAppendFailed: &e.EventsAppendFailed,
	}
}

// The incr* methods below are safe to call on a nil *runnerMetrics receiver
// (a Runner built without one, e.g. DryRun, or a test constructing Runner
// directly) — each checks m != nil before touching any field, so call sites
// in runner.go never need their own nil check.
func (m *runnerMetrics) incrAttemptsStarted() {
	if m != nil {
		atomic.AddInt64(m.attemptsStarted, 1)
	}
}
func (m *runnerMetrics) incrAttemptsSucceeded() {
	if m != nil {
		atomic.AddInt64(m.attemptsSucceeded, 1)
	}
}
func (m *runnerMetrics) incrAttemptsFailed() {
	if m != nil {
		atomic.AddInt64(m.attemptsFailed, 1)
	}
}
func (m *runnerMetrics) incrAttemptsRetried() {
	if m != nil {
		atomic.AddInt64(m.attemptsRetried, 1)
	}
}
func (m *runnerMetrics) incrEventsAppended() {
	if m != nil {
		atomic.AddInt64(m.eventsAppended, 1)
	}
}
func (m *runnerMetrics) incrEventsAppendFailed() {
	if m != nil {
		atomic.AddInt64(m.eventsAppendFailed, 1)
	}
}

// NewEngine creates a new pipeline execution engine.
func NewEngine(s store.Store) *Engine {
	maxC := 4
	if v := os.Getenv("BROKOLI_MAX_CONCURRENT_RUNS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxC = n
		}
	}
	eventBuf := 512
	if v := os.Getenv("BROKOLI_EVENT_BUFFER"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			eventBuf = n
		}
	}
	artifactDir := os.Getenv("BROKOLI_ARTIFACT_DIR")
	if artifactDir == "" {
		artifactDir = "./brokoli-artifacts"
	}
	// Spill threshold: the estimated encoded size at or above which a node's
	// output is written to the artifact store instead of being held in
	// memory for the rest of the run (Tnsor-Labs/brokoli#38). Unset uses
	// DefaultSpillThresholdBytes; a negative value disables spilling, which
	// restores the previous always-in-memory behaviour exactly.
	spillThreshold := DefaultSpillThresholdBytes
	if v := os.Getenv("BROKOLI_SPILL_THRESHOLD_BYTES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			spillThreshold = n
		}
	}
	streamThreshold := DefaultStreamThresholdBytes
	if v := os.Getenv("BROKOLI_STREAM_THRESHOLD_BYTES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			streamThreshold = n
		}
	}

	checkpointDir := os.Getenv("BROKOLI_PAGINATION_CHECKPOINT_DIR")
	if checkpointDir == "" {
		checkpointDir = "./brokoli-pagination-checkpoints"
	}
	return &Engine{
		store:                     s,
		shutdown:                  make(chan struct{}),
		eventCh:                   make(chan models.Event, eventBuf),
		active:                    make(map[string]*Runner),
		maxConcurrent:             maxC,
		runSem:                    make(chan struct{}, maxC),
		ArtifactStore:             NewLocalDiskArtifactStore(artifactDir),
		SpillThresholdBytes:       spillThreshold,
		StreamThresholdBytes:      streamThreshold,
		PaginationCheckpointStore: NewLocalDiskPaginationCheckpointStore(checkpointDir),
		InstanceID:                common.NewID(),
	}
}

// ErrEngineClosed is returned by run dispatch after Close has been called.
var ErrEngineClosed = errors.New("engine: closed")

// closing reports whether Close has been called.
func (e *Engine) closing() bool {
	select {
	case <-e.shutdown:
		return true
	default:
		return false
	}
}

// goBG runs fn on a goroutine whose lifetime the engine owns. After Close
// it is a silent no-op: the work it carries — trigger-mode fan-out — is
// best-effort dispatch, and refusing it at shutdown is the point.
func (e *Engine) goBG(fn func()) {
	if e.closing() {
		return
	}
	e.bg.Add(1)
	go func() {
		defer e.bg.Done()
		fn()
	}()
}

// Close stops the engine: new run dispatch is refused with ErrEngineClosed,
// no new background work is started, and Close waits — bounded by ctx —
// for every goroutine the engine owns to finish. In-flight runs are allowed
// to complete rather than being killed; a caller that wants a hard ceiling
// expresses it through ctx.
//
// Before this existed, goroutines spawned after a run finished (trigger-mode
// dependency fan-out in particular) simply outlived everything: they wrote
// into stores that had been closed and directories that were being removed,
// which surfaced as "database is closed" logs in production shutdowns and
// as "TempDir RemoveAll: directory not empty" flakes in CI
// (Tnsor-Labs/brokoli#94).
//
// Close is idempotent and safe to call from multiple goroutines.
func (e *Engine) Close(ctx context.Context) error {
	e.closeOnce.Do(func() { close(e.shutdown) })
	done := make(chan struct{})
	go func() {
		e.bg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("engine: shutdown wait: %w", ctx.Err())
	}
}

// SetMaxConcurrentRuns updates the concurrency limit.
func (e *Engine) SetMaxConcurrentRuns(n int) {
	if n < 1 {
		n = 1
	}
	e.mu.Lock()
	e.maxConcurrent = n
	e.runSem = make(chan struct{}, n)
	e.mu.Unlock()
}

// GetQueueInfo returns current active and queued run counts.
func (e *Engine) GetQueueInfo() (active int, maxConcurrent int) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.active), e.maxConcurrent
}

// Events returns the channel for real-time event streaming.
func (e *Engine) Events() <-chan models.Event {
	return e.eventCh
}

// CancelRun stops a running pipeline.
func (e *Engine) CancelRun(runID string) error {
	// Case 1: the run executes in this process — cancel its Runner directly.
	if e.cancelLocalRun(runID) {
		return nil
	}

	run, err := e.store.GetRun(runID)
	if err != nil {
		return fmt.Errorf("run %s not found or already completed", runID)
	}
	if isTerminalRunStatus(run.Status) {
		return fmt.Errorf("run %s not found or already completed", runID)
	}

	// Case 2: the run is still pending — queued but claimed by nobody.
	// Cancel it with a compare-and-swap against the pending status, the
	// mirror image of ClaimPendingRun's conditional claim: exactly one of
	// the two transitions wins, so a worker can never execute a run this
	// path cancelled. Losing the swap means a claim landed in between —
	// fall through to the running-elsewhere case.
	if run.Status == models.RunStatusPending {
		if canceller, ok := e.store.(store.PendingRunCanceller); ok {
			now := time.Now().UTC()
			cancelled, cErr := canceller.CancelPendingRun(runID, now)
			if cErr != nil {
				return fmt.Errorf("cancel pending run %s: %w", runID, cErr)
			}
			if cancelled {
				e.appendEvent(&models.RunEvent{
					RunID:     runID,
					EventType: models.RunEventCancelled,
					Payload: models.RunEventPayload{
						Status:     models.RunStatusCancelled,
						FinishedAt: &now,
						Error:      "cancelled by user",
					},
				})
				e.emitCancelledEvent(runID, run.PipelineID, "")
				return nil
			}
		}
	}

	// Case 3: the run executes in another engine instance. Broadcast when
	// a relay is configured; the owning instance's CancelRelayedRun does
	// the actual cancel (and, post-#203, finalizes the run as cancelled on
	// every exit path). Without a relay this remains an error — better a
	// loud "could not cancel" than silently doing nothing.
	if e.CancelRelay != nil {
		if bErr := e.CancelRelay.BroadcastCancel(runID); bErr != nil {
			return fmt.Errorf("broadcast cancel for run %s: %w", runID, bErr)
		}
		return nil
	}
	return fmt.Errorf("run %s not found or already completed", runID)
}

// CancelRelayedRun delivers a relayed cancellation request (see
// extensions.RunCancelRelay): it cancels runID if that run is executing in
// this process and quietly ignores it otherwise — every instance receives
// every broadcast, and only the owner acts.
func (e *Engine) CancelRelayedRun(runID string) {
	e.cancelLocalRun(runID)
}

// cancelLocalRun cancels runID if its Runner lives in this process's
// active map, returning whether it did. The status write here converges
// with the Runner's own finalizeCancelled write — both record cancelled,
// whichever lands last.
func (e *Engine) cancelLocalRun(runID string) bool {
	e.mu.RLock()
	runner, ok := e.active[runID]
	e.mu.RUnlock()
	if !ok {
		return false
	}
	runner.Cancel()
	// Update run status
	run, err := e.store.GetRun(runID)
	if err == nil && run.Status == models.RunStatusRunning {
		now := time.Now()
		run.Status = models.RunStatusCancelled
		run.FinishedAt = &now
		e.store.UpdateRun(run)
		e.appendEvent(&models.RunEvent{
			RunID:     runID,
			EventType: models.RunEventCancelled,
			Payload: models.RunEventPayload{
				Status:     models.RunStatusCancelled,
				FinishedAt: run.FinishedAt,
				Error:      "cancelled by user",
			},
		})
	}
	e.emitCancelledEvent(runID, runner.pipe.ID, runner.pipe.Name)
	return true
}

// emitCancelledEvent pushes the user-facing cancellation event. An empty
// name is resolved best-effort from the store — the ID is always present,
// and consumers already tolerate an empty name (deleted pipelines).
func (e *Engine) emitCancelledEvent(runID, pipelineID, name string) {
	if name == "" {
		if pipe, err := e.store.GetPipeline(pipelineID); err == nil && pipe != nil {
			name = pipe.Name
		}
	}
	e.eventCh <- models.Event{
		Type:         models.EventRunFailed,
		RunID:        runID,
		PipelineID:   pipelineID,
		PipelineName: name,
		Status:       models.RunStatusCancelled,
		Error:        "cancelled by user",
	}
}

// resolveRunPipelineVersion returns the store.PipelineVersion number that
// represents pipe's current live definition, auto-saving a snapshot if none
// exists yet — e.g. a pipeline that was created but never edited since (the
// UI's Update handler is what normally calls SavePipelineVersion), or one
// seeded outside that path (CLI, git-sync, a pre-existing database from
// before this field existed). This guarantees every run created after this
// point can record a concrete PipelineVersion that GetPipelineVersion can
// resolve later, even after further edits — the core fix for
// Tnsor-Labs/brokoli#8: a run (and especially a resumed run) executing the
// live pipeline definition instead of a pinned snapshot of what it started
// with. ListPipelineVersions orders by version DESC, so the first result is
// always the current maximum regardless of how many versions exist.
func resolveRunPipelineVersion(s store.Store, pipe *models.Pipeline) (int, error) {
	versions, err := s.ListPipelineVersions(pipe.ID)
	if err != nil {
		return 0, fmt.Errorf("list pipeline versions: %w", err)
	}
	if len(versions) > 0 {
		return versions[0].Version, nil
	}
	snapshot, err := json.Marshal(pipe)
	if err != nil {
		return 0, fmt.Errorf("snapshot pipeline: %w", err)
	}
	v, err := s.SavePipelineVersion(pipe.ID, string(snapshot), "auto-snapshot at first run")
	if err != nil {
		return 0, fmt.Errorf("save initial pipeline version: %w", err)
	}
	return v, nil
}

// resolvePipelineForRun returns the pipeline definition a run should
// execute against. When version is a real, previously recorded
// PipelineVersion, this returns the exact snapshot pinned at run-creation
// time — immune to edits made to the live pipeline afterward, which is what
// makes ResumeRun safe to call after the pipeline has since changed.
//
// version <= 0 means no snapshot was ever recorded for this run — a run
// created before models.Run.PipelineVersion existed. Falling back to the
// live pipeline reproduces the previous (pre-#8) behavior for those legacy
// rows instead of failing them outright.
func (e *Engine) resolvePipelineForRun(pipelineID string, version int) (*models.Pipeline, error) {
	if version <= 0 {
		log.Printf("run for pipeline %s has no recorded pipeline_version; resolving the live pipeline definition instead (legacy run predating Tnsor-Labs/brokoli#8)", pipelineID)
		return e.store.GetPipeline(pipelineID)
	}
	snapshot, err := e.store.GetPipelineVersion(pipelineID, version)
	if err != nil {
		return nil, fmt.Errorf("get pipeline version %d for %s: %w", version, pipelineID, err)
	}
	var pipe models.Pipeline
	if err := json.Unmarshal([]byte(snapshot), &pipe); err != nil {
		return nil, fmt.Errorf("decode pipeline version %d snapshot for %s: %w", version, pipelineID, err)
	}
	return &pipe, nil
}

// RunPipeline triggers execution of a pipeline by ID with optional params.
func (e *Engine) RunPipeline(pipelineID string, params ...map[string]string) (*models.Run, error) {
	if e.closing() {
		return nil, ErrEngineClosed
	}
	pipe, err := e.store.GetPipeline(pipelineID)
	if err != nil {
		return nil, fmt.Errorf("get pipeline: %w", err)
	}

	// Validate before running
	if ve := ValidatePipeline(pipe, e.Executors...); ve.HasErrors() {
		return nil, ve
	}

	pipelineVersion, err := resolveRunPipelineVersion(e.store, pipe)
	if err != nil {
		return nil, fmt.Errorf("resolve pipeline version: %w", err)
	}

	// Check cross-pipeline dependencies; persist a blocked run if unsatisfied.
	if ok, _, reason := CheckDependencies(e.store, pipe, time.Now().UTC()); !ok {
		now := time.Now().UTC()
		blocked := &models.Run{
			ID:              common.NewID(),
			PipelineID:      pipe.ID,
			Status:          models.RunStatusBlocked,
			Error:           reason,
			StartedAt:       &now,
			FinishedAt:      &now,
			PipelineVersion: pipelineVersion,
			OrgID:           pipe.OrgID,
		}
		if len(params) > 0 && params[0] != nil {
			blocked.Params = params[0]
		}
		if err := e.store.CreateRun(blocked); err != nil {
			return nil, fmt.Errorf("create blocked run: %w", err)
		}
		e.appendEvent(&models.RunEvent{
			RunID:     blocked.ID,
			EventType: models.RunEventCreated,
			Payload: models.RunEventPayload{
				Status:          models.RunStatusBlocked,
				PipelineID:      blocked.PipelineID,
				StartedAt:       blocked.StartedAt,
				FinishedAt:      blocked.FinishedAt,
				Error:           blocked.Error,
				Params:          blocked.Params,
				PipelineVersion: blocked.PipelineVersion,
			},
		})
		return blocked, nil
	}

	runner := NewRunner(e.store, e.eventCh, pipe, e.VarStore, e.ConnResolver, e.Executors, e.Notifier, e.InstanceID, e.InstanceJobQueue)
	runner.orgID = pipe.OrgID
	runner.pipelineVersion = pipelineVersion
	runner.artifactStore = e.ArtifactStore
	runner.spillThreshold = e.SpillThresholdBytes
	runner.streamThreshold = e.StreamThresholdBytes
	runner.checkpointStore = e.PaginationCheckpointStore
	runner.metrics = e.newRunnerMetrics()
	if len(params) > 0 && params[0] != nil {
		runner.params = params[0]
	}

	// Acquire concurrency slot (blocks if at max)
	atomic.AddInt64(&e.RunsQueueWaiting, 1)
	e.runSem <- struct{}{}
	atomic.AddInt64(&e.RunsQueueWaiting, -1)

	atomic.AddInt64(&e.RunsTotal, 1)

	// Pre-generate run ID so we can register the runner for cancellation
	runID := common.NewID()
	runner.preRunID = runID
	e.mu.Lock()
	e.active[runID] = runner
	e.mu.Unlock()

	resultCh := make(chan runResult, 1)
	e.bg.Add(1)
	go func() {
		defer e.bg.Done()
		defer func() { <-e.runSem }()
		defer func() {
			e.mu.Lock()
			delete(e.active, runID)
			e.mu.Unlock()
		}()
		run, err := runner.Execute()
		if err != nil {
			atomic.AddInt64(&e.RunsFailed, 1)
		} else {
			atomic.AddInt64(&e.RunsSucceeded, 1)
		}
		// Signal the caller first so the RunPipeline return latency does not include
		// downstream trigger-mode fan-out. Fire dependents asynchronously — they'll
		// re-enter RunPipeline through their own goroutines.
		resultCh <- runResult{run: run, err: err}
		if run != nil {
			e.goBG(func() { e.fireTriggerModeDependents(run) })
		}
	}()

	// Wait briefly for the run to be created so we can return its ID
	result := <-resultCh
	return result.run, result.err
}

// fireTriggerModeDependents scans for pipelines that list the finished run's pipeline
// as a trigger-mode dependency and fires them when upstream reaches a terminal state.
//
// Tenant isolation: only pipelines in the same org as the finished run are considered.
// This is the last line of defense if save-time validation is bypassed. Traversal uses
// the lightweight dep adjacency query so we never load nodes/edges JSON blobs.
func (e *Engine) fireTriggerModeDependents(finished *models.Run) {
	if e.closing() {
		return
	}
	if finished == nil || finished.PipelineID == "" {
		return
	}
	// Only fire on terminal states.
	switch finished.Status {
	case models.RunStatusSuccess, models.RunStatusFailed, models.RunStatusCancelled:
	default:
		return
	}
	upstream, err := e.store.GetPipeline(finished.PipelineID)
	if err != nil || upstream == nil {
		log.Printf("trigger-mode: cannot resolve upstream %s: %v", finished.PipelineID, err)
		return
	}
	summaries, err := e.store.ListPipelineDepsByOrg(upstream.OrgID)
	if err != nil {
		log.Printf("trigger-mode: list deps for org %s: %v", upstream.OrgID, err)
		return
	}
	for i := range summaries {
		sum := &summaries[i]
		if !hasTriggerOn(sum, finished.PipelineID) {
			continue
		}
		pid := sum.ID
		e.goBG(func() {
			if _, err := e.RunPipeline(pid); err != nil {
				// A refusal at shutdown is the mechanism working, not a
				// failed fire worth a log line per dependent.
				if errors.Is(err, ErrEngineClosed) {
					return
				}
				log.Printf("trigger-mode: fire failed for %s: %v", pid, err)
			}
		})
	}
}

// hasTriggerOn reports whether any rule on the summary is a trigger-mode dep on upstreamID.
func hasTriggerOn(sum *models.PipelineDepSummary, upstreamID string) bool {
	for _, rule := range sum.EffectiveDependencies() {
		if rule.PipelineID == upstreamID && rule.Mode == models.DepModeTrigger {
			return true
		}
	}
	return false
}

// RunPipelineAsync triggers execution and returns the run ID immediately.
// The pipeline runs in a background goroutine. Use WebSocket events or polling to track status.
// If a JobQueue is configured, the run is enqueued for distributed execution instead.
func (e *Engine) RunPipelineAsync(pipelineID string, params ...map[string]string) (string, error) {
	if e.closing() {
		return "", ErrEngineClosed
	}
	pipe, err := e.store.GetPipeline(pipelineID)
	if err != nil {
		return "", fmt.Errorf("get pipeline: %w", err)
	}

	// Validate before running
	if ve := ValidatePipeline(pipe, e.Executors...); ve.HasErrors() {
		return "", ve
	}

	pipelineVersion, err := resolveRunPipelineVersion(e.store, pipe)
	if err != nil {
		return "", fmt.Errorf("resolve pipeline version: %w", err)
	}

	// Check cross-pipeline dependencies; persist a blocked run if unsatisfied.
	if ok, _, reason := CheckDependencies(e.store, pipe, time.Now().UTC()); !ok {
		now := time.Now().UTC()
		blocked := &models.Run{
			ID:              common.NewID(),
			PipelineID:      pipe.ID,
			Status:          models.RunStatusBlocked,
			Error:           reason,
			StartedAt:       &now,
			FinishedAt:      &now,
			PipelineVersion: pipelineVersion,
			OrgID:           pipe.OrgID,
		}
		if len(params) > 0 && params[0] != nil {
			blocked.Params = params[0]
		}
		if err := e.store.CreateRun(blocked); err != nil {
			return "", fmt.Errorf("create blocked run: %w", err)
		}
		e.appendEvent(&models.RunEvent{
			RunID:     blocked.ID,
			EventType: models.RunEventCreated,
			Payload: models.RunEventPayload{
				Status:          models.RunStatusBlocked,
				PipelineID:      blocked.PipelineID,
				StartedAt:       blocked.StartedAt,
				FinishedAt:      blocked.FinishedAt,
				Error:           blocked.Error,
				Params:          blocked.Params,
				PipelineVersion: blocked.PipelineVersion,
			},
		})
		return blocked.ID, nil
	}

	runID := common.NewID()

	// If job queue is available, enqueue for distributed execution
	if e.JobQueue != nil {
		accepted := &models.Run{
			ID:              runID,
			PipelineID:      pipelineID,
			Status:          models.RunStatusPending,
			PipelineVersion: pipelineVersion,
			OrgID:           pipe.OrgID,
		}
		if len(params) > 0 && params[0] != nil {
			accepted.Params = params[0]
		}
		createdEvent := &models.RunEvent{
			RunID:     accepted.ID,
			EventType: models.RunEventCreated,
			Payload: models.RunEventPayload{
				Status:          models.RunStatusPending,
				PipelineID:      accepted.PipelineID,
				Params:          accepted.Params,
				PipelineVersion: accepted.PipelineVersion,
			},
		}
		// The durable dispatch-intent/outbox record (Tnsor-Labs/brokoli#7).
		// NodeID/Attempt stay at their zero values — this is a pipeline-level
		// outbox row, not a per-node one; see models.ExecutionAttempt's doc
		// comment. IdempotencyKey is the run ID itself: already a globally
		// unique, stable identity for this dispatch.
		outboxAttempt := &models.ExecutionAttempt{
			RunID:          accepted.ID,
			Status:         models.AttemptStatusQueued,
			IdempotencyKey: accepted.ID,
		}

		// Outbox pattern: the pending-run row, its run.created event, and its
		// durable execution-attempt record commit in one transaction.
		// Previously CreateRun and JobQueue.Enqueue below were two
		// independent, non-transactional calls — a crash between them left a
		// `pending` run in the store with nothing ever enqueued to execute
		// it, and no durable trace that dispatch was ever intended. Wrapping
		// the DB-side writes in one transaction closes that specific gap:
		// JobQueue.Enqueue can still fail (handled below) or the process can
		// still die before or during that call, but by the time this
		// transaction commits, a durable execution_attempts row already
		// exists recording the dispatch intent, making the run reconcilable
		// on restart (Tnsor-Labs/brokoli#9) instead of silently orphaned.
		if err := e.store.WithTx(func(tx *sql.Tx) error {
			if err := e.store.CreateRunTx(tx, accepted); err != nil {
				return fmt.Errorf("create pending run: %w", err)
			}
			if err := e.store.AppendEventTx(tx, createdEvent); err != nil {
				return fmt.Errorf("append run created event: %w", err)
			}
			if attemptStore, ok := e.store.(store.ExecutionAttemptStore); ok {
				if err := attemptStore.CreateExecutionAttemptTx(tx, outboxAttempt); err != nil {
					return fmt.Errorf("create execution attempt outbox record: %w", err)
				}
			}
			return nil
		}); err != nil {
			return "", err
		}

		job := extensions.RunJob{
			ID:             runID,
			PipelineID:     pipelineID,
			RunID:          runID,
			OrgID:          pipe.OrgID,
			EnqueuedAt:     time.Now().UTC(),
			IdempotencyKey: runID,
		}
		if len(params) > 0 && params[0] != nil {
			job.Params = params[0]
		}
		if err := e.JobQueue.Enqueue(job); err != nil {
			now := time.Now().UTC()
			accepted.Status = models.RunStatusFailed
			accepted.Error = "queue publication failed: " + err.Error()
			accepted.FinishedAt = &now
			if updateErr := e.store.UpdateRun(accepted); updateErr != nil {
				return "", fmt.Errorf("enqueue job: %v; mark run failed: %w", err, updateErr)
			}
			e.appendEvent(&models.RunEvent{
				RunID:     accepted.ID,
				EventType: models.RunEventTerminal,
				Payload: models.RunEventPayload{
					Status:     models.RunStatusFailed,
					FinishedAt: accepted.FinishedAt,
					Error:      accepted.Error,
				},
			})
			return "", fmt.Errorf("enqueue job: %w", err)
		}
		e.appendEvent(&models.RunEvent{
			RunID:     accepted.ID,
			EventType: models.RunEventQueued,
			Payload: models.RunEventPayload{
				Status: models.RunStatusPending,
			},
		})
		return runID, nil
	}

	// Default: run in-process (current behavior)
	runner := NewRunner(e.store, e.eventCh, pipe, e.VarStore, e.ConnResolver, e.Executors, e.Notifier, e.InstanceID, e.InstanceJobQueue)
	runner.orgID = pipe.OrgID
	runner.pipelineVersion = pipelineVersion
	runner.artifactStore = e.ArtifactStore
	runner.spillThreshold = e.SpillThresholdBytes
	runner.streamThreshold = e.StreamThresholdBytes
	runner.checkpointStore = e.PaginationCheckpointStore
	runner.metrics = e.newRunnerMetrics()
	if len(params) > 0 && params[0] != nil {
		runner.params = params[0]
	}

	runner.preRunID = runID
	e.mu.Lock()
	e.active[runID] = runner
	e.mu.Unlock()

	// Acquire concurrency slot
	atomic.AddInt64(&e.RunsQueueWaiting, 1)
	e.runSem <- struct{}{}
	atomic.AddInt64(&e.RunsQueueWaiting, -1)
	atomic.AddInt64(&e.RunsTotal, 1)

	e.bg.Add(1)
	go func() {
		defer e.bg.Done()
		defer func() { <-e.runSem }()
		defer func() {
			e.mu.Lock()
			delete(e.active, runID)
			e.mu.Unlock()
		}()
		_, err := runner.Execute()
		if err != nil {
			atomic.AddInt64(&e.RunsFailed, 1)
		} else {
			atomic.AddInt64(&e.RunsSucceeded, 1)
		}
	}()

	return runID, nil
}

// ExecuteQueuedRun executes a previously accepted run using its durable ID.
// Only the delivery that atomically transitions pending to running executes;
// duplicate deliveries return the existing run without repeating side effects.
func (e *Engine) ExecuteQueuedRun(runID, pipelineID string, params map[string]string) (*models.Run, error) {
	if e.closing() {
		return nil, ErrEngineClosed
	}
	accepted, err := e.store.GetRun(runID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: accepted run %s does not exist", ErrInvalidQueuedRun, runID)
		}
		return nil, fmt.Errorf("get accepted run: %w", err)
	}
	if accepted.PipelineID != pipelineID {
		return nil, fmt.Errorf("%w: run %s belongs to pipeline %s, not %s", ErrInvalidQueuedRun, runID, accepted.PipelineID, pipelineID)
	}
	if isTerminalRunStatus(accepted.Status) {
		return accepted, nil
	}
	if accepted.Status != models.RunStatusPending {
		return nil, fmt.Errorf("run %s is already %s", runID, accepted.Status)
	}

	pipe, err := e.store.GetPipeline(pipelineID)
	if err != nil {
		return e.failAcceptedRun(accepted, fmt.Errorf("get pipeline: %w", err))
	}
	if ve := ValidatePipeline(pipe, e.Executors...); ve.HasErrors() {
		return e.failAcceptedRun(accepted, ve)
	}

	claimer, ok := e.store.(store.PendingRunClaimer)
	if !ok {
		return nil, fmt.Errorf("store does not support atomic queued-run claims")
	}
	startedAt := time.Now().UTC()
	traceID := common.NewID()

	// claimCtx carries the OpenTelemetry trace context for this run from
	// the claim attempt onward (Tnsor-Labs/brokoli#11): handed to the
	// Runner below as parentCtx so the run's execution span is a child of
	// (i.e. the same trace as) this claim span, rather than starting an
	// unrelated new trace once execution begins. claimSpan itself ends
	// here — only its trace/span-context metadata is propagated forward,
	// which remains valid for creating child spans even after End().
	claimCtx, claimSpan := tracing.Tracer().Start(context.Background(), "brokoli.run.claim",
		trace.WithAttributes(
			attribute.String("run_id", runID),
			attribute.String("pipeline_id", pipelineID),
		))
	claimed, err := claimer.ClaimPendingRun(runID, pipelineID, startedAt, traceID)
	if err != nil {
		claimSpan.RecordError(err)
		claimSpan.SetStatus(codes.Error, err.Error())
		claimSpan.End()
		return nil, fmt.Errorf("claim pending run: %w", err)
	}
	claimSpan.SetAttributes(attribute.Bool("claimed", claimed))
	if !claimed {
		claimSpan.End()
		existing, getErr := e.store.GetRun(runID)
		if getErr != nil {
			return nil, fmt.Errorf("get concurrently claimed run: %w", getErr)
		}
		if isTerminalRunStatus(existing.Status) {
			return existing, nil
		}
		return nil, fmt.Errorf("run %s was concurrently claimed", runID)
	}
	claimSpan.SetStatus(codes.Ok, "")
	claimSpan.End()
	common.SLog().Info("run claimed", common.RunAttr(runID), common.PipelineAttr(pipelineID), common.TraceAttr(traceID))

	accepted.Status = models.RunStatusRunning
	accepted.StartedAt = &startedAt
	accepted.TraceID = traceID
	accepted.Params = params
	e.appendEvent(&models.RunEvent{
		RunID:     runID,
		EventType: models.RunEventClaimed,
		Payload: models.RunEventPayload{
			Status:    models.RunStatusRunning,
			StartedAt: &startedAt,
			TraceID:   traceID,
			Params:    params,
		},
	})
	runner := NewRunner(e.store, e.eventCh, pipe, e.VarStore, e.ConnResolver, e.Executors, e.Notifier, e.InstanceID, e.InstanceJobQueue)
	runner.orgID = pipe.OrgID
	runner.params = params
	runner.acceptedRun = accepted
	runner.artifactStore = e.ArtifactStore
	runner.spillThreshold = e.SpillThresholdBytes
	runner.streamThreshold = e.StreamThresholdBytes
	runner.checkpointStore = e.PaginationCheckpointStore
	runner.metrics = e.newRunnerMetrics()
	runner.parentCtx = claimCtx

	atomic.AddInt64(&e.RunsQueueWaiting, 1)
	e.runSem <- struct{}{}
	atomic.AddInt64(&e.RunsQueueWaiting, -1)
	defer func() { <-e.runSem }()
	atomic.AddInt64(&e.RunsTotal, 1)

	e.mu.Lock()
	e.active[runID] = runner
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.active, runID)
		e.mu.Unlock()
	}()

	_, executeErr := runner.Execute()
	if executeErr != nil {
		atomic.AddInt64(&e.RunsFailed, 1)
	} else {
		atomic.AddInt64(&e.RunsSucceeded, 1)
	}
	persisted, err := e.store.GetRun(runID)
	if err != nil {
		return nil, fmt.Errorf("verify terminal run state: %w", err)
	}
	if !isTerminalRunStatus(persisted.Status) {
		if executeErr != nil {
			return nil, fmt.Errorf("execute run: %v; durable status is %s", executeErr, persisted.Status)
		}
		return nil, fmt.Errorf("run execution returned without durable terminal state: %s", persisted.Status)
	}
	e.goBG(func() { e.fireTriggerModeDependents(persisted) })
	return persisted, executeErr
}

func (e *Engine) failAcceptedRun(run *models.Run, cause error) (*models.Run, error) {
	now := time.Now().UTC()
	run.Status = models.RunStatusFailed
	run.Error = cause.Error()
	run.FinishedAt = &now
	if err := e.store.UpdateRun(run); err != nil {
		return nil, fmt.Errorf("%v; persist failed run: %w", cause, err)
	}
	e.appendEvent(&models.RunEvent{
		RunID:     run.ID,
		EventType: models.RunEventTerminal,
		Payload: models.RunEventPayload{
			Status:     models.RunStatusFailed,
			FinishedAt: run.FinishedAt,
			Error:      run.Error,
		},
	})
	return run, cause
}

// appendEvent persists a run/node-attempt lifecycle event. Failures are
// logged, not propagated — the events table is a dual-written, advisory
// projection source alongside runs/node_runs for this PR; making event
// persistence transactionally required is tracked as follow-up work
// (Tnsor-Labs/brokoli#7).
func (e *Engine) appendEvent(ev *models.RunEvent) {
	if err := e.store.AppendEvent(ev); err != nil {
		atomic.AddInt64(&e.EventsAppendFailed, 1)
		common.SLog().Warn("append run event failed",
			common.RunAttr(ev.RunID), common.NodeAttr(ev.NodeID),
			"event_type", ev.EventType, "error", err)
		return
	}
	atomic.AddInt64(&e.EventsAppended, 1)
}

func isTerminalRunStatus(status models.RunStatus) bool {
	switch status {
	case models.RunStatusSuccess, models.RunStatusFailed, models.RunStatusCancelled, models.RunStatusBlocked:
		return true
	default:
		return false
	}
}

// DryRun executes a pipeline with only the first N rows and returns node previews.
// Does not persist a real run — useful for editor preview.
func (e *Engine) DryRun(p *models.Pipeline, maxRows int) (map[string]*DryRunNodeResult, error) {
	if maxRows <= 0 {
		maxRows = 10
	}

	runner := NewRunner(e.store, e.eventCh, p, e.VarStore, e.ConnResolver, e.Executors, e.Notifier, e.InstanceID, e.InstanceJobQueue)
	runner.dryRun = true
	runner.dryRunMaxRows = maxRows

	_, err := runner.Execute()
	// Return partial previews alongside the execution error.
	return runner.dryRunResults, err
}

// DryRunNodeResult contains the preview data for one node.
type DryRunNodeResult struct {
	NodeID  string                   `json:"node_id"`
	Name    string                   `json:"name"`
	Status  string                   `json:"status"`
	Columns []string                 `json:"columns"`
	Rows    []map[string]interface{} `json:"rows"`
	Error   string                   `json:"error,omitempty"`
}

// Backfill triggers multiple runs for a date range.
func (e *Engine) Backfill(pipelineID, startDate, endDate string) ([]string, error) {
	start, err := time.Parse("2006-01-02", startDate)
	if err != nil {
		return nil, fmt.Errorf("invalid start_date: %w", err)
	}
	end, err := time.Parse("2006-01-02", endDate)
	if err != nil {
		return nil, fmt.Errorf("invalid end_date: %w", err)
	}
	if end.Before(start) {
		return nil, fmt.Errorf("end_date must be after start_date")
	}

	var runIDs []string
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		params := map[string]string{"date": d.Format("2006-01-02")}
		run, err := e.RunPipeline(pipelineID, params)
		if err != nil {
			return runIDs, fmt.Errorf("backfill %s failed: %w", d.Format("2006-01-02"), err)
		}
		runIDs = append(runIDs, run.ID)
	}
	return runIDs, nil
}

// ResumeRun re-runs a failed run from the first failed node.
//
// Two correctness properties matter here, both fixed by Tnsor-Labs/brokoli#8:
//
//  1. It resumes the DAG the original run actually executed, not whatever
//     the live pipeline looks like now — resolved via
//     resolvePipelineForRun(oldRun.PipelineID, oldRun.PipelineVersion)
//     instead of e.store.GetPipeline, which always returns the current,
//     possibly-since-edited row.
//  2. Skipped nodes (those that already succeeded) get their REAL prior
//     output restored from the durable artifact store — see
//     Runner.restoreSkippedNodeOutput — instead of the previous behavior,
//     where a skipped node's entry in the in-memory outputs map was simply
//     never populated and its downstream consumer silently received an
//     empty dataset.
//
// The new run records its lineage back to oldRun via
// models.Run.ResumedFromRunID, and pins itself to the SAME PipelineVersion
// oldRun used — a resume-of-a-resume stays pinned to the original DAG
// snapshot, not whatever version happened to be live if oldRun itself had
// no recorded version.
func (e *Engine) ResumeRun(runID string) (*models.Run, error) {
	if e.closing() {
		return nil, ErrEngineClosed
	}
	oldRun, err := e.store.GetRun(runID)
	if err != nil {
		return nil, fmt.Errorf("get run: %w", err)
	}
	if oldRun.Status != models.RunStatusFailed {
		return nil, fmt.Errorf("can only resume failed runs (current: %s)", oldRun.Status)
	}

	pipe, err := e.resolvePipelineForRun(oldRun.PipelineID, oldRun.PipelineVersion)
	if err != nil {
		return nil, fmt.Errorf("resolve pipeline definition for resume: %w", err)
	}

	// Prefer the exact version oldRun was pinned to. For a legacy run with
	// no recorded version (resolvePipelineForRun fell back to the live
	// pipeline above), pin the NEW resumed run to a freshly resolved
	// version instead of propagating the same "unknown" state forward —
	// this progressively closes the gap so a resume-of-this-resume is
	// pinned even though oldRun itself wasn't.
	newRunVersion := oldRun.PipelineVersion
	if newRunVersion <= 0 {
		if v, verr := resolveRunPipelineVersion(e.store, pipe); verr == nil {
			newRunVersion = v
		}
	}

	// Find which nodes succeeded — they can be skipped. IR 2.1 condition
	// decisions are restored from durable completion events so resume cannot
	// select a different branch after variables or run metadata change.
	succeeded := make(map[string]bool)
	conditionResults := make(map[string]bool)
	artifactSourceRunIDs := make(map[string]string)
	conditionNodes := make(map[string]bool)
	unresolvedNodes := make(map[string]bool)
	for _, node := range pipe.Nodes {
		unresolvedNodes[node.ID] = true
		if node.Type == models.NodeTypeCondition {
			conditionNodes[node.ID] = true
		}
	}
	lineageRun := oldRun
	seenRuns := make(map[string]bool)
	for lineageRun != nil && len(unresolvedNodes) > 0 {
		if seenRuns[lineageRun.ID] {
			return nil, fmt.Errorf("cannot resume: cycle in run lineage at %s", lineageRun.ID)
		}
		seenRuns[lineageRun.ID] = true
		latest := latestNodeOutcome(lineageRun.NodeRuns)
		events, eventErr := e.store.ListEventsByRun(lineageRun.ID)
		if eventErr != nil {
			return nil, fmt.Errorf("load reusable node outcomes from run %s: %w", lineageRun.ID, eventErr)
		}
		type reuseEvent struct {
			reused        bool
			condition     *bool
			artifactRunID string
		}
		reuseEvents := make(map[string]reuseEvent)
		for _, event := range events {
			if event.Attempt == nil {
				continue
			}
			nr, ok := latest[event.NodeID]
			if !ok || nr.Attempt != *event.Attempt {
				continue
			}
			metadata := reuseEvents[event.NodeID]
			if event.EventType == models.AttemptSkipped && event.Payload.Reused {
				metadata.reused = true
				metadata.artifactRunID = event.Payload.ArtifactRunID
			}
			if (event.EventType == models.AttemptCompleted || event.EventType == models.AttemptSkipped) && event.Payload.ConditionResult != nil {
				selected := *event.Payload.ConditionResult
				metadata.condition = &selected
			}
			reuseEvents[event.NodeID] = metadata
		}

		for nodeID := range unresolvedNodes {
			nr, ok := latest[nodeID]
			if !ok {
				continue // carrying may have failed; inspect the ancestor
			}
			metadata := reuseEvents[nodeID]
			reusable := nr.Status == models.RunStatusSuccess || (nr.Status == models.RunStatusSkipped && metadata.reused)
			if reusable {
				if conditionNodes[nodeID] && pipe.IRVersion == models.ConditionalEdgesIRVersion {
					if metadata.condition == nil {
						return nil, fmt.Errorf("cannot resume: condition node %s has no durable routing decision", nodeID)
					}
					conditionResults[nodeID] = *metadata.condition
				}
				succeeded[nodeID] = true
				if metadata.reused {
					artifactSourceRunIDs[nodeID] = metadata.artifactRunID
				} else {
					artifactSourceRunIDs[nodeID] = lineageRun.ID
				}
			}
			// Any recorded non-reusable outcome belongs to this run and must
			// execute again; do not search past it for an older success.
			delete(unresolvedNodes, nodeID)
		}

		if lineageRun.ResumedFromRunID == "" {
			break
		}
		ancestor, ancestorErr := e.store.GetRun(lineageRun.ResumedFromRunID)
		if ancestorErr != nil {
			return nil, fmt.Errorf("load ancestor run %s: %w", lineageRun.ResumedFromRunID, ancestorErr)
		}
		lineageRun = ancestor
	}

	runner := NewRunner(e.store, e.eventCh, pipe, e.VarStore, e.ConnResolver, e.Executors, e.Notifier, e.InstanceID, e.InstanceJobQueue)
	runner.orgID = pipe.OrgID
	runner.skipNodes = succeeded
	runner.conditionResults = conditionResults
	runner.artifactSourceRunIDs = artifactSourceRunIDs
	runner.params = oldRun.Params
	runner.artifactStore = e.ArtifactStore
	runner.spillThreshold = e.SpillThresholdBytes
	runner.streamThreshold = e.StreamThresholdBytes
	runner.checkpointStore = e.PaginationCheckpointStore
	runner.resumedFromRunID = oldRun.ID
	runner.pipelineVersion = newRunVersion
	runner.metrics = e.newRunnerMetrics()

	run, err := runner.Execute()
	return run, err
}

type runResult struct {
	run *models.Run
	err error
}
