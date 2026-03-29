package engine

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hc12r/broked/models"
	"github.com/hc12r/broked/store"
)

// Engine manages pipeline execution and event broadcasting.
type Engine struct {
	store         store.Store
	eventCh       chan models.Event
	mu            sync.RWMutex
	active        map[string]*Runner
	maxConcurrent int
	runSem        chan struct{}
	VarStore      VariableStore       // for resolving ${var.key}
	ConnResolver  *ConnectionResolver // for resolving conn_id → URI
	RunsTotal     int64
	RunsSucceeded int64
	RunsFailed    int64
}

// NewEngine creates a new pipeline execution engine.
func NewEngine(s store.Store) *Engine {
	maxC := 4 // default max concurrent runs
	return &Engine{
		store:         s,
		eventCh:       make(chan models.Event, 256),
		active:        make(map[string]*Runner),
		maxConcurrent: maxC,
		runSem:        make(chan struct{}, maxC),
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

	runner := NewRunner(e.store, e.eventCh, pipe, e.VarStore, e.ConnResolver)
	if len(params) > 0 && params[0] != nil {
		runner.params = params[0]
	}

	// Acquire concurrency slot (blocks if at max)
	e.runSem <- struct{}{}

	atomic.AddInt64(&e.RunsTotal, 1)

	resultCh := make(chan runResult, 1)
	go func() {
		defer func() { <-e.runSem }()
		run, err := runner.Execute()
		if err != nil {
			atomic.AddInt64(&e.RunsFailed, 1)
		} else {
			atomic.AddInt64(&e.RunsSucceeded, 1)
		}
		resultCh <- runResult{run: run, err: err}
		if run != nil {
			e.mu.Lock()
			delete(e.active, run.ID)
			e.mu.Unlock()
		}
	}()

	// Wait briefly for the run to be created so we can return its ID
	result := <-resultCh
	return result.run, result.err
}

// RunPipelineAsync triggers execution and returns the run ID immediately.
func (e *Engine) RunPipelineAsync(pipelineID string) (string, error) {
	pipe, err := e.store.GetPipeline(pipelineID)
	if err != nil {
		return "", fmt.Errorf("get pipeline: %w", err)
	}

	runner := NewRunner(e.store, e.eventCh, pipe, e.VarStore, e.ConnResolver)

	// We need the run ID before it starts, so we pre-create
	// Actually, let the runner create it and report back via channel
	runIDCh := make(chan string, 1)
	errCh := make(chan error, 1)

	go func() {
		run, err := runner.Execute()
		if run != nil {
			runIDCh <- run.ID
			e.mu.Lock()
			delete(e.active, run.ID)
			e.mu.Unlock()
		}
		if err != nil {
			errCh <- err
		}
	}()

	// Wait for the run ID or an error
	select {
	case id := <-runIDCh:
		return id, nil
	case err := <-errCh:
		return "", err
	}
}

// DryRun executes a pipeline with only the first N rows and returns node previews.
// Does not persist a real run — useful for editor preview.
func (e *Engine) DryRun(p *models.Pipeline, maxRows int) (map[string]*DryRunNodeResult, error) {
	if maxRows <= 0 {
		maxRows = 10
	}

	runner := NewRunner(e.store, e.eventCh, p, e.VarStore, e.ConnResolver)
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
func (e *Engine) ResumeRun(runID string) (*models.Run, error) {
	oldRun, err := e.store.GetRun(runID)
	if err != nil {
		return nil, fmt.Errorf("get run: %w", err)
	}
	if oldRun.Status != models.RunStatusFailed {
		return nil, fmt.Errorf("can only resume failed runs (current: %s)", oldRun.Status)
	}

	pipe, err := e.store.GetPipeline(oldRun.PipelineID)
	if err != nil {
		return nil, fmt.Errorf("get pipeline: %w", err)
	}

	// Find which nodes succeeded — they can be skipped
	succeeded := make(map[string]bool)
	for _, nr := range oldRun.NodeRuns {
		if nr.Status == models.RunStatusSuccess {
			succeeded[nr.NodeID] = true
		}
	}

	runner := NewRunner(e.store, e.eventCh, pipe, e.VarStore, e.ConnResolver)
	runner.skipNodes = succeeded

	run, err := runner.Execute()
	return run, err
}

type runResult struct {
	run *models.Run
	err error
}
