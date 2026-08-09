package engine

import (
	"strconv"

	"github.com/Tnsor-Labs/brokoli/models"
)

// ProjectRun deterministically folds an ordered run-event stream into the
// equivalent of the runs/node_runs row shape that the rest of the codebase
// reads via store.Store.GetRun. It is the reference "replay reproduces
// state" implementation required by issue #6 — given nothing but the
// immutable event log for a run, this function must reconstruct exactly
// what production code currently derives by mutating runs/node_runs in
// place via UpdateRun/UpdateNodeRun.
//
// events must be supplied in ascending id (append) order, which is what
// store.Store.ListEventsByRun already returns. ProjectRun does not itself
// query the store — it is pure, so it can be unit tested against a captured
// event slice without a database.
func ProjectRun(runID string, events []models.RunEvent) *models.Run {
	run := &models.Run{ID: runID}
	nodeRuns := make(map[string]*models.NodeRun)
	var order []string // preserves first-seen order of (node_id, attempt) pairs

	getOrCreateNodeRun := func(e models.RunEvent) *models.NodeRun {
		key := nodeRunKey(e.NodeID, e.Attempt)
		nr, ok := nodeRuns[key]
		if !ok {
			attempt := 0
			if e.Attempt != nil {
				attempt = *e.Attempt
			}
			nr = &models.NodeRun{
				ID:      e.Payload.NodeRunID,
				RunID:   runID,
				NodeID:  e.NodeID,
				Attempt: attempt,
			}
			nodeRuns[key] = nr
			order = append(order, key)
		}
		if e.Payload.NodeRunID != "" {
			nr.ID = e.Payload.NodeRunID
		}
		return nr
	}

	for _, e := range events {
		p := e.Payload
		switch e.EventType {
		case models.RunEventCreated:
			run.PipelineID = p.PipelineID
			run.Status = p.Status
			run.StartedAt = p.StartedAt
			run.FinishedAt = p.FinishedAt
			run.TraceID = p.TraceID
			run.Error = p.Error
			run.PipelineVersion = p.PipelineVersion
			run.ResumedFromRunID = p.ResumedFromRunID
			if p.Params != nil {
				run.Params = p.Params
			}

		case models.RunEventQueued:
			// Informational only — does not change runs.status (still pending).

		case models.RunEventClaimed:
			run.Status = models.RunStatusRunning
			if p.StartedAt != nil {
				run.StartedAt = p.StartedAt
			}
			if p.TraceID != "" {
				run.TraceID = p.TraceID
			}
			if p.Params != nil {
				run.Params = p.Params
			}

		case models.RunEventCancelled, models.RunEventTerminal, models.RunEventRecoveryFailed:
			// RunEventRecoveryFailed (Tnsor-Labs/brokoli#9) carries its own
			// terminal transition — unlike RunEventRecoveryStarted/Completed
			// below, it is not purely informational: startup recovery uses
			// it as the terminal event itself when a run has no recoverable
			// path (as opposed to pairing RunEventRecoveryCompleted with a
			// normal RunEventTerminal, which is what happens when recovery
			// CAN determine the run's true outcome from its event log).
			run.Status = p.Status
			run.FinishedAt = p.FinishedAt
			run.Error = p.Error

		case models.RunEventRecoveryStarted, models.RunEventRecoveryCompleted:
			// Informational only — the audit trail of a startup recovery
			// pass. Any state change is carried by the RunEventTerminal (or
			// RunEventRecoveryFailed, above) event recovery pairs these
			// with, not by these events themselves.

		case models.AttemptStarted:
			if e.NodeID == "" {
				continue
			}
			nr := getOrCreateNodeRun(e)
			nr.Status = models.RunStatusRunning
			nr.StartedAt = p.StartedAt
			nr.ReadyAt = p.ReadyAt
			nr.QueueMs = p.QueueMs
			nr.TraceID = p.TraceID
			nr.SpanID = p.SpanID

		case models.AttemptCompleted:
			if e.NodeID == "" {
				continue
			}
			nr := getOrCreateNodeRun(e)
			nr.Status = models.RunStatusSuccess
			nr.RowCount = p.RowCount
			nr.DurationMs = p.DurationMs
			nr.RowsPerSec = p.RowsPerSec

		case models.AttemptFailed:
			if e.NodeID == "" {
				continue
			}
			nr := getOrCreateNodeRun(e)
			nr.Status = models.RunStatusFailed
			nr.DurationMs = p.DurationMs
			nr.Error = p.Error

		case models.AttemptSkipped:
			if e.NodeID == "" {
				continue
			}
			nr := getOrCreateNodeRun(e)
			nr.Status = models.RunStatusSkipped
			nr.ReadyAt = p.ReadyAt
			nr.TraceID = p.TraceID

		case models.RetryScheduled:
			// Informational only — the next attempt's AttemptStarted event
			// creates its own node_runs-equivalent row.
		}
	}

	for _, key := range order {
		run.NodeRuns = append(run.NodeRuns, *nodeRuns[key])
	}
	return run
}

func nodeRunKey(nodeID string, attempt *int) string {
	a := 0
	if attempt != nil {
		a = *attempt
	}
	// A NUL separator keeps this collision-free against node IDs that
	// happen to contain digits right before the attempt number.
	return nodeID + "\x00" + strconv.Itoa(a)
}
