package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// Parking before dispatch: for a deployment that hands whole runs to
// another process which cannot park them (Engine.WaitInPlace), the waits
// with nothing upstream can still park here, before the run is handed
// over. Nothing has run yet, so there are no earlier outputs a woken run
// would need restored; the run holds no slot anywhere while it waits.
// The watcher wakes it as usual and WakeHandoff hands it on.

// ParkBeforeDispatch checks, on this process, the wait nodes of pipe that
// have nothing upstream, for a run that has not been handed to whatever
// executes it. If every one is met, the run is left untouched and parked
// is false: dispatch it. Otherwise the run parks on the first unmet one,
// exactly as a running run would -- the parked wait is stored, the run
// becomes waiting -- and parked is true; the watcher wakes it later
// through WakeHandoff.
//
// file_exists waits are not checked here: the files they look for are
// where the run executes, not necessarily here. Neither are waits with
// something upstream. Both are left to the process that runs the
// pipeline.
//
// Config is resolved the way the runner resolves it, with params merged
// over the pipeline's defaults. A check that fails, such as a malformed
// URL or a pipeline wait for a pipeline that is not found, fails the run,
// as the wait's first check does when a runner makes it, and err says
// why. params are the run's parameters; run.OrgID must be set, since a
// pipeline wait is answered only within it.
func (e *Engine) ParkBeforeDispatch(run *models.Run, pipe *models.Pipeline, params map[string]string) (parked bool, err error) {
	waits := dispatchableWaits(pipe)
	if len(waits) == 0 {
		return false, nil
	}
	now := time.Now().UTC()
	vc := runVariableContext(pipe, run, params, e.VarStore, now)
	for _, node := range waits {
		node.Config = resolveNodeConfig(vc, node)
		cond, poll, timeout, err := parseWaitCondition(node)
		if err == nil && cond.Type == "file_exists" {
			continue
		}
		var met bool
		if err == nil {
			met, err = waitConditionMet(context.Background(), e.store, run, cond)
		}
		if err != nil {
			_, failErr := e.failAcceptedRun(run, fmt.Errorf("node %s (%s) failed: %w", node.Name, node.ID, err))
			e.emitDispatchEvent(pipe, models.Event{Type: models.EventRunFailed, RunID: run.ID, Status: models.RunStatusFailed, Error: run.Error})
			return false, failErr
		}
		if met {
			continue
		}
		park := &WaitParkError{NodeID: node.ID, Condition: cond, Poll: poll, Deadline: now.Add(timeout)}
		if err := e.parkBeforeDispatch(run, pipe, park); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

// dispatchableWaits returns pipe's wait nodes with no incoming edge, in
// pipeline order. file_exists is filtered after config resolution, since
// a condition can only be read from resolved config.
func dispatchableWaits(pipe *models.Pipeline) []models.Node {
	hasUpstream := make(map[string]bool, len(pipe.Edges))
	for _, edge := range pipe.Edges {
		hasUpstream[edge.To] = true
	}
	var waits []models.Node
	for _, node := range pipe.Nodes {
		if node.Type == models.NodeTypeWait && !hasUpstream[node.ID] {
			waits = append(waits, node)
		}
	}
	return waits
}

// runVariableContext is the variable context a runner builds for run:
// pipeline default params under the run's, the run's data interval,
// stored variables scoped to the pipeline's workspace.
func runVariableContext(pipe *models.Pipeline, run *models.Run, params map[string]string, vars VariableStore, now time.Time) *VariableContext {
	merged := make(map[string]string)
	for k, v := range pipe.Params {
		merged[k] = v
	}
	for k, v := range params {
		merged[k] = v
	}
	vc := NewVariableContext(merged, run.ID, now)
	vc.IntervalStart = run.DataIntervalStart
	vc.IntervalEnd = run.DataIntervalEnd
	vc.Vars = vars
	vc.WorkspaceID = pipe.WorkspaceID
	return vc
}

// parkBeforeDispatch is parkRun for a run no runner holds: the parked
// wait first, then the run row, for the same crash-ordering reason.
func (e *Engine) parkBeforeDispatch(run *models.Run, pipe *models.Pipeline, p *WaitParkError) error {
	condJSON, err := json.Marshal(p.Condition)
	if err != nil {
		return fmt.Errorf("park run: marshal condition: %w", err)
	}
	now := time.Now().UTC()
	if err := e.store.CreateParkedWait(&models.ParkedWait{
		RunID:        run.ID,
		PipelineID:   pipe.ID,
		NodeID:       p.NodeID,
		Condition:    string(condJSON),
		PollInterval: p.Poll.Milliseconds(),
		NextPollAt:   now.Add(p.Poll),
		ExpiresAt:    p.Deadline,
		CreatedAt:    now,
	}); err != nil {
		_, failErr := e.failAcceptedRun(run, fmt.Errorf("park run at wait node %s: %w", p.NodeID, err))
		return failErr
	}
	run.Status = models.RunStatusWaiting
	run.Error = ""
	if err := e.store.UpdateRun(run); err != nil {
		return fmt.Errorf("persist waiting run: %w", err)
	}
	e.appendEvent(&models.RunEvent{
		RunID:     run.ID,
		NodeID:    p.NodeID,
		EventType: models.RunEventParked,
		Payload: models.RunEventPayload{
			Status: models.RunStatusWaiting,
			Error:  "waiting on " + p.Condition.Type,
		},
	})
	e.emitDispatchEvent(pipe, models.Event{Type: models.EventRunWaiting, RunID: run.ID, Status: models.RunStatusWaiting})
	common.SLog().Info("run parked before dispatch",
		common.RunAttr(run.ID), common.PipelineAttr(pipe.ID),
		"node", p.NodeID, "condition", p.Condition.Type,
		"poll", p.Poll.String(), "deadline", p.Deadline.Format(time.RFC3339))
	return nil
}

// emitDispatchEvent stamps an event the way Runner.emit does -- org and
// pipeline name included, for tenant isolation and for consumers that
// cannot resolve a deleted pipeline's name -- and drops it rather than
// block when nobody is reading, as Runner.emit does.
func (e *Engine) emitDispatchEvent(pipe *models.Pipeline, ev models.Event) {
	ev.Timestamp = time.Now().UTC()
	ev.PipelineID = pipe.ID
	ev.PipelineName = pipe.Name
	ev.OrgID = pipe.OrgID
	select {
	case e.eventCh <- ev:
	default:
	}
}
