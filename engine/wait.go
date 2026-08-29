package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
)

// Deferrable waits (#399): a wait node that has not started yet is the
// natural unit of deferral -- nothing has to suspend user code mid-flight,
// which was the hardest part of the prior art's design. When the condition
// is not met NOW, the run parks: its row goes to status "waiting", a
// ParkedWait lands in the store (restart-durable), and the run's
// concurrency slot frees. One leader-gated watcher polls every parked
// condition and wakes the run -- the SAME run, via the acceptedRun path --
// when its condition fires. Timeouts are part of the condition: "wait at
// most 6h, then fail naming what was waited for".

// waitPollDefault and waitTimeoutDefault are the condition's defaults.
const (
	waitPollDefault    = 30 * time.Second
	waitTimeoutDefault = 6 * time.Hour
)

// waitCondition is a wait node's declarative condition.
type waitCondition struct {
	// Type: file_exists | http | interval_elapsed | pipeline.
	Type string `json:"condition"`

	// file_exists: a local path or glob; met when it matches anything.
	Path string `json:"path,omitempty"`

	// http: met when GET returns ExpectStatus (default 200). The request
	// goes through the netguard-guarded client (ADR-022) -- a wait node
	// must not become an SSRF probe.
	URL          string `json:"url,omitempty"`
	ExpectStatus int    `json:"expect_status,omitempty"`

	// interval_elapsed: met when now >= the run's data_interval_end plus
	// Offset (ADR-028 pairing: "wait until the slice is really over").
	Offset string `json:"offset,omitempty"`

	// pipeline: met when the named pipeline's latest run succeeded --
	// today's dependency-gate shape, expressible as a wait.
	PipelineID string `json:"pipeline_id,omitempty"`

	PollInterval string `json:"poll_interval,omitempty"`
	Timeout      string `json:"timeout,omitempty"`
}

// WaitParkError is how a wait node whose condition is not yet met leaves
// the runner: not a failure -- the signal to park. It carries everything
// the ParkedWait row needs.
type WaitParkError struct {
	NodeID    string
	Condition waitCondition
	Poll      time.Duration
	Deadline  time.Time
}

func (e *WaitParkError) Error() string {
	return fmt.Sprintf("wait node %s: condition %q not yet met; parking", e.NodeID, e.Condition.Type)
}

// parseWaitCondition reads a wait node's (variable-resolved) config.
func parseWaitCondition(node models.Node) (waitCondition, time.Duration, time.Duration, error) {
	raw, err := json.Marshal(node.Config)
	if err != nil {
		return waitCondition{}, 0, 0, err
	}
	var c waitCondition
	if err := json.Unmarshal(raw, &c); err != nil {
		return waitCondition{}, 0, 0, fmt.Errorf("wait config: %w", err)
	}
	poll, timeout := waitPollDefault, waitTimeoutDefault
	if c.PollInterval != "" {
		d, err := time.ParseDuration(c.PollInterval)
		if err != nil || d <= 0 {
			return c, 0, 0, fmt.Errorf("wait poll_interval %q is not a positive duration", c.PollInterval)
		}
		poll = d
	}
	if c.Timeout != "" {
		d, err := time.ParseDuration(c.Timeout)
		if err != nil || d <= 0 {
			return c, 0, 0, fmt.Errorf("wait timeout %q is not a positive duration", c.Timeout)
		}
		timeout = d
	}
	switch c.Type {
	case "file_exists":
		if c.Path == "" {
			return c, 0, 0, fmt.Errorf("wait condition file_exists needs a path")
		}
	case "http":
		if c.URL == "" {
			return c, 0, 0, fmt.Errorf("wait condition http needs a url")
		}
	case "interval_elapsed":
		if c.Offset != "" {
			if _, err := time.ParseDuration(c.Offset); err != nil {
				return c, 0, 0, fmt.Errorf("wait offset %q is not a duration", c.Offset)
			}
		}
	case "pipeline":
		if c.PipelineID == "" {
			return c, 0, 0, fmt.Errorf("wait condition pipeline needs a pipeline_id")
		}
	case "":
		return c, 0, 0, fmt.Errorf("wait node needs a condition (file_exists, http, interval_elapsed, pipeline)")
	default:
		return c, 0, 0, fmt.Errorf("unknown wait condition %q (known: file_exists, http, interval_elapsed, pipeline)", c.Type)
	}
	return c, poll, timeout, nil
}

// waitHTTPClient builds the guarded client wait conditions probe with --
// netguard.Outbound() honors the deployment's policy env, exactly like
// the notify handler's client. A var so tests can substitute a client
// that reaches their loopback fixtures, which netguard rightly blocks.
var waitHTTPClient = func() *http.Client {
	return netguard.Outbound().Client(15 * time.Second)
}

// waitConditionMet evaluates one condition once. run supplies the
// interval for interval_elapsed; st answers pipeline conditions.
func waitConditionMet(ctx context.Context, st waitStore, run *models.Run, c waitCondition) (bool, error) {
	switch c.Type {
	case "file_exists":
		matches, err := filepath.Glob(c.Path)
		if err != nil {
			return false, fmt.Errorf("wait glob %q: %w", c.Path, err)
		}
		return len(matches) > 0, nil
	case "http":
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil)
		if err != nil {
			return false, err
		}
		resp, err := waitHTTPClient().Do(req)
		if err != nil {
			// Unreachable is "not yet", not an error: the whole point of
			// waiting on an endpoint is that it is not answering yet.
			return false, nil
		}
		defer resp.Body.Close()
		want := c.ExpectStatus
		if want == 0 {
			want = http.StatusOK
		}
		return resp.StatusCode == want, nil
	case "interval_elapsed":
		if run == nil || run.DataIntervalEnd == nil {
			return false, fmt.Errorf("wait interval_elapsed needs a run with a data interval (ADR-028); this run has none")
		}
		var off time.Duration
		if c.Offset != "" {
			off, _ = time.ParseDuration(c.Offset)
		}
		return !time.Now().UTC().Before(run.DataIntervalEnd.Add(off)), nil
	case "pipeline":
		runs, err := st.ListRunsByPipeline(c.PipelineID, 1)
		if err != nil {
			return false, fmt.Errorf("wait pipeline %q: %w", c.PipelineID, err)
		}
		return len(runs) > 0 && runs[0].Status == models.RunStatusSuccess, nil
	default:
		return false, fmt.Errorf("unknown wait condition %q", c.Type)
	}
}

// waitStore is the sliver of store.Store wait evaluation needs.
type waitStore interface {
	ListRunsByPipeline(pipelineID string, limit int) ([]models.Run, error)
}

// runWait is the wait node's handler: evaluate once; met passes the input
// through untouched (a wait is a gate, not a transform), unmet parks.
func (r *Runner) runWait(node models.Node, input *common.DataSet) (*common.DataSet, error) {
	cond, poll, timeout, err := parseWaitCondition(node)
	if err != nil {
		return nil, err
	}
	met, err := waitConditionMet(r.ctx, r.store, r.run, cond)
	if err != nil {
		return nil, err
	}
	if met {
		return input, nil
	}
	return nil, &WaitParkError{
		NodeID:    node.ID,
		Condition: cond,
		Poll:      poll,
		Deadline:  time.Now().UTC().Add(timeout),
	}
}

// parkRun finalizes a run as parked (#399): the ParkedWait row FIRST --
// a crash between the two writes leaves a running run recovery owns plus
// a stale park the watcher deletes on its status check -- then the run
// flips to waiting. No failure hooks, no failure alerts: parked is not
// failed. Execute returns nil after this; the run's slot frees when the
// runner's goroutine does.
func (r *Runner) parkRun(p *WaitParkError) error {
	condJSON, err := json.Marshal(p.Condition)
	if err != nil {
		return r.failRun(fmt.Errorf("park run: marshal condition: %w", err))
	}
	now := time.Now().UTC()
	if err := r.store.CreateParkedWait(&models.ParkedWait{
		RunID:        r.run.ID,
		PipelineID:   r.pipe.ID,
		NodeID:       p.NodeID,
		Condition:    string(condJSON),
		PollInterval: p.Poll.Milliseconds(),
		NextPollAt:   now.Add(p.Poll),
		ExpiresAt:    p.Deadline,
		CreatedAt:    now,
	}); err != nil {
		// A park that cannot be persisted must not strand the run in
		// waiting with nothing to wake it: fail loudly instead.
		return r.failRun(fmt.Errorf("park run at wait node %s: %w", p.NodeID, err))
	}
	r.run.Status = models.RunStatusWaiting
	r.run.Error = ""
	if err := r.store.UpdateRun(r.run); err != nil {
		return fmt.Errorf("persist waiting run: %w", err)
	}
	r.appendEvent(models.RunEvent{
		RunID:     r.run.ID,
		NodeID:    p.NodeID,
		EventType: models.RunEventParked,
		Payload: models.RunEventPayload{
			Status: models.RunStatusWaiting,
			Error:  "waiting on " + p.Condition.Type,
		},
	})
	r.emit(models.Event{Type: models.EventRunWaiting, RunID: r.run.ID, PipelineID: r.pipe.ID, Status: models.RunStatusWaiting})
	common.SLog().Info("run parked on wait node",
		common.RunAttr(r.run.ID), common.PipelineAttr(r.pipe.ID),
		"node", p.NodeID, "condition", p.Condition.Type,
		"poll", p.Poll.String(), "deadline", p.Deadline.Format(time.RFC3339))
	return nil
}

// WakeParkedRun continues a parked run -- the SAME run row, not a resume
// lineage child. The claim is the conditional waiting->running flip
// (store.ClaimWaitingRun), so two watchers cannot both wake it; the
// caller is expected to have already deleted the ParkedWait. The run
// takes a fresh concurrency slot -- while parked it held none, which is
// the entire point of a deferrable wait.
func (e *Engine) WakeParkedRun(runID string) (*models.Run, error) {
	if e.closing() {
		return nil, ErrEngineClosed
	}
	run, err := e.store.GetRun(runID)
	if err != nil {
		return nil, fmt.Errorf("get parked run: %w", err)
	}
	claimed, err := e.store.ClaimWaitingRun(runID)
	if err != nil {
		return nil, fmt.Errorf("claim parked run: %w", err)
	}
	if !claimed {
		// Someone else woke it, or it left waiting some other way; both
		// are the no-op case, not an error.
		return run, nil
	}
	run.Status = models.RunStatusRunning
	run.Error = ""
	e.appendEvent(&models.RunEvent{
		RunID:     runID,
		EventType: models.RunEventWoken,
		Payload:   models.RunEventPayload{Status: models.RunStatusRunning},
	})

	pipe, err := e.resolvePipelineForRun(run.PipelineID, run.PipelineVersion)
	if err != nil {
		return nil, fmt.Errorf("resolve pipeline for wake: %w", err)
	}
	full, err := e.store.GetRun(runID) // with NodeRuns, for the outcome scan
	if err != nil {
		return nil, err
	}
	full.Status = models.RunStatusRunning
	succeeded, conditionResults, artifactSourceRunIDs, err := e.reusableOutcomes(pipe, full)
	if err != nil {
		return nil, fmt.Errorf("wake: %w", err)
	}

	runner := NewRunner(e.store, e.eventCh, pipe, e.VarStore, e.ConnResolver, e.Executors, e.Notifier, e.InstanceID, e.InstanceJobQueue)
	runner.orgID = pipe.OrgID
	runner.params = full.Params
	runner.acceptedRun = full
	runner.skipNodes = succeeded
	runner.conditionResults = conditionResults
	runner.artifactSourceRunIDs = artifactSourceRunIDs
	runner.artifactStore = e.ArtifactStore
	runner.spillThreshold = e.SpillThresholdBytes
	runner.streamThreshold = e.StreamThresholdBytes
	runner.checkpointStore = e.PaginationCheckpointStore
	runner.pipelineVersion = full.PipelineVersion
	runner.intervalStart = full.DataIntervalStart
	runner.intervalEnd = full.DataIntervalEnd
	runner.metrics = e.newRunnerMetrics()

	e.mu.Lock()
	e.active[runID] = runner
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.active, runID)
		e.mu.Unlock()
	}()

	e.runSem <- struct{}{}
	defer func() { <-e.runSem }()

	common.SLog().Info("parked run woken", common.RunAttr(runID), common.PipelineAttr(pipe.ID))
	return runner.Execute()
}

// TimeoutParkedRun fails a parked run whose wait expired, naming the
// condition -- timeouts are part of the condition, not an afterthought.
// Conditional on status waiting, so it cannot clobber a run that woke in
// the same instant.
func (e *Engine) TimeoutParkedRun(w models.ParkedWait, now time.Time) {
	var cond waitCondition
	condName := "unparseable condition"
	if err := json.Unmarshal([]byte(w.Condition), &cond); err == nil && cond.Type != "" {
		condName = "condition " + cond.Type
	}
	msg := fmt.Sprintf("wait node %s timed out: %s not met by %s",
		w.NodeID, condName, w.ExpiresAt.Format(time.RFC3339))
	failed, err := e.store.FailWaitingRun(w.RunID, now, msg)
	if err != nil {
		common.SLog().Error("timeout parked run", common.RunAttr(w.RunID), "error", err)
		return
	}
	_, _ = e.store.DeleteParkedWait(w.RunID)
	if !failed {
		return // it woke first; the park row was stale
	}
	e.appendEvent(&models.RunEvent{
		RunID:     w.RunID,
		NodeID:    w.NodeID,
		EventType: models.RunEventTerminal,
		Payload:   models.RunEventPayload{Status: models.RunStatusFailed, FinishedAt: &now, Error: msg},
	})
	e.eventCh <- models.Event{Type: models.EventRunFailed, RunID: w.RunID, PipelineID: w.PipelineID, Status: models.RunStatusFailed, Error: msg}
	common.SLog().Warn("parked run timed out", common.RunAttr(w.RunID), "node", w.NodeID, "error", msg)
}
