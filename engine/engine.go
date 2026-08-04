package engine

import (
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
	"github.com/Tnsor-Labs/brokoli/store"
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
	// ArtifactStore persists durable node-output artifacts so ResumeRun can
	// restore a skipped node's real prior output (Tnsor-Labs/brokoli#8).
	// Defaults to a LocalDiskArtifactStore rooted at BROKOLI_ARTIFACT_DIR
	// (or ./brokoli-artifacts); assign a different implementation the same
	// way VarStore/ConnResolver are overridden after NewEngine.
	ArtifactStore ArtifactStore
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
	return &Engine{
		store:         s,
		eventCh:       make(chan models.Event, eventBuf),
		active:        make(map[string]*Runner),
		maxConcurrent: maxC,
		runSem:        make(chan struct{}, maxC),
		ArtifactStore: NewLocalDiskArtifactStore(artifactDir),
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
	e.mu.RLock()
	runner, ok := e.active[runID]
	e.mu.RUnlock()
	if !ok {
		return fmt.Errorf("run %s not found or already completed", runID)
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
	e.eventCh <- models.Event{
		Type:       models.EventRunFailed,
		RunID:      runID,
		PipelineID: runner.pipe.ID,
		Status:     models.RunStatusCancelled,
		Error:      "cancelled by user",
	}
	return nil
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
	pipe, err := e.store.GetPipeline(pipelineID)
	if err != nil {
		return nil, fmt.Errorf("get pipeline: %w", err)
	}

	// Validate before running
	if ve := ValidatePipeline(pipe); ve.HasErrors() {
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

	runner := NewRunner(e.store, e.eventCh, pipe, e.VarStore, e.ConnResolver, e.Executors, e.Notifier)
	runner.orgID = pipe.OrgID
	runner.pipelineVersion = pipelineVersion
	runner.artifactStore = e.ArtifactStore
	if len(params) > 0 && params[0] != nil {
		runner.params = params[0]
	}

	// Acquire concurrency slot (blocks if at max)
	e.runSem <- struct{}{}

	atomic.AddInt64(&e.RunsTotal, 1)

	// Pre-generate run ID so we can register the runner for cancellation
	runID := common.NewID()
	runner.preRunID = runID
	e.mu.Lock()
	e.active[runID] = runner
	e.mu.Unlock()

	resultCh := make(chan runResult, 1)
	go func() {
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
			go e.fireTriggerModeDependents(run)
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
		go func() {
			if _, err := e.RunPipeline(pid); err != nil {
				log.Printf("trigger-mode: fire failed for %s: %v", pid, err)
			}
		}()
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
	pipe, err := e.store.GetPipeline(pipelineID)
	if err != nil {
		return "", fmt.Errorf("get pipeline: %w", err)
	}

	// Validate before running
	if ve := ValidatePipeline(pipe); ve.HasErrors() {
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
	runner := NewRunner(e.store, e.eventCh, pipe, e.VarStore, e.ConnResolver, e.Executors, e.Notifier)
	runner.orgID = pipe.OrgID
	runner.pipelineVersion = pipelineVersion
	runner.artifactStore = e.ArtifactStore
	if len(params) > 0 && params[0] != nil {
		runner.params = params[0]
	}

	runner.preRunID = runID
	e.mu.Lock()
	e.active[runID] = runner
	e.mu.Unlock()

	// Acquire concurrency slot
	e.runSem <- struct{}{}
	atomic.AddInt64(&e.RunsTotal, 1)

	go func() {
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
	if ve := ValidatePipeline(pipe); ve.HasErrors() {
		return e.failAcceptedRun(accepted, ve)
	}

	claimer, ok := e.store.(store.PendingRunClaimer)
	if !ok {
		return nil, fmt.Errorf("store does not support atomic queued-run claims")
	}
	startedAt := time.Now().UTC()
	traceID := common.NewID()
	claimed, err := claimer.ClaimPendingRun(runID, pipelineID, startedAt, traceID)
	if err != nil {
		return nil, fmt.Errorf("claim pending run: %w", err)
	}
	if !claimed {
		existing, getErr := e.store.GetRun(runID)
		if getErr != nil {
			return nil, fmt.Errorf("get concurrently claimed run: %w", getErr)
		}
		if isTerminalRunStatus(existing.Status) {
			return existing, nil
		}
		return nil, fmt.Errorf("run %s was concurrently claimed", runID)
	}

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
	runner := NewRunner(e.store, e.eventCh, pipe, e.VarStore, e.ConnResolver, e.Executors, e.Notifier)
	runner.orgID = pipe.OrgID
	runner.params = params
	runner.acceptedRun = accepted
	runner.artifactStore = e.ArtifactStore

	e.runSem <- struct{}{}
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
	go e.fireTriggerModeDependents(persisted)
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
		log.Printf("append run event %s for run %s: %v", ev.EventType, ev.RunID, err)
	}
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

	runner := NewRunner(e.store, e.eventCh, p, e.VarStore, e.ConnResolver, e.Executors, e.Notifier)
	runner.dryRun = true
	runner.dryRunMaxRows = maxRows

	_, err := runner.Execute()
	// Even if it fails partway, return what we got
	_ = err

	return runner.dryRunResults, nil
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

	// Find which nodes succeeded — they can be skipped
	succeeded := make(map[string]bool)
	for _, nr := range oldRun.NodeRuns {
		if nr.Status == models.RunStatusSuccess {
			succeeded[nr.NodeID] = true
		}
	}

	runner := NewRunner(e.store, e.eventCh, pipe, e.VarStore, e.ConnResolver, e.Executors, e.Notifier)
	runner.orgID = pipe.OrgID
	runner.skipNodes = succeeded
	runner.artifactStore = e.ArtifactStore
	runner.resumedFromRunID = oldRun.ID
	runner.pipelineVersion = newRunVersion

	run, err := runner.Execute()
	return run, err
}

type runResult struct {
	run *models.Run
	err error
}
