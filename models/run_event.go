package models

import "time"

// RunEventType identifies the kind of immutable fact recorded in the run
// event log (store.Store.AppendEvent / ListEventsByRun). Unlike EventType
// (models/event.go), which is an ephemeral, in-memory-only message broadcast
// over WebSocket, a RunEvent is durably persisted and never mutated or
// deleted — it is the append-only source material that engine.ProjectRun
// folds into the equivalent of a runs/node_runs row.
//
// Event types are grouped into two scopes, distinguished by whether NodeID
// is set on the RunEvent:
//
//   - Run-level events (NodeID empty) describe the lifecycle of the run row
//     itself and are coherent with models.RunStatus: pending is implied by
//     RunEventCreated with no StartedAt, running by RunEventCreated with a
//     StartedAt or by RunEventClaimed, and the terminal statuses (success,
//     failed, cancelled, blocked) are reached via RunEventCancelled or
//     RunEventTerminal.
//   - Node-attempt-level events (NodeID + Attempt set) describe a single
//     models.NodeRun — one per (node, attempt) pair, mirroring how
//     node_runs already has one row per retry attempt.
type RunEventType string

const (
	// RunEventCreated marks the moment a runs row was inserted — covers the
	// direct in-process start path (status=running), the queued/pending
	// path (status=pending), and the blocked path (status=blocked, already
	// terminal). Equivalent to the issue's canonical "run created" type.
	RunEventCreated RunEventType = "run.created"

	// RunEventQueued marks a pending run being handed off to a JobQueue for
	// distributed execution, awaiting a worker to claim it. Equivalent to
	// the issue's canonical "attempt queued" type, applied at run scope.
	RunEventQueued RunEventType = "run.queued"

	// RunEventClaimed marks the atomic pending->running transition performed
	// by store.PendingRunClaimer.ClaimPendingRun. Equivalent to the issue's
	// canonical "attempt claimed" type.
	RunEventClaimed RunEventType = "run.claimed"

	// RunEventCancelled marks a run being cancelled (by CancelRun or because
	// the runner observed ctx.Err() before finishing). Terminal: also sets
	// the run's final status/finished_at/error, same as RunEventTerminal.
	RunEventCancelled RunEventType = "run.cancelled"

	// RunEventTerminal marks a run reaching a terminal outcome that is not a
	// cancellation: success, failed, or (for the direct-create path) it is
	// folded into RunEventCreated instead since blocked runs never
	// transition. Equivalent to the issue's canonical "terminal run
	// outcome" type.
	RunEventTerminal RunEventType = "run.terminal"

	// AttemptStarted marks a node_runs row being created for one execution
	// attempt of one node (Attempt=0 for the first try, 1+ for retries).
	// Equivalent to the issue's canonical "attempt started" type.
	AttemptStarted RunEventType = "attempt.started"

	// AttemptCompleted marks a node attempt finishing successfully.
	// Equivalent to the issue's canonical "attempt completed" type.
	AttemptCompleted RunEventType = "attempt.completed"

	// AttemptFailed marks a node attempt finishing with an error (whether or
	// not it will be retried). Equivalent to the issue's canonical "attempt
	// failed" type.
	AttemptFailed RunEventType = "attempt.failed"

	// RetryScheduled marks the moment a retry is decided and its backoff
	// delay computed, before the next attempt's node_runs row is created.
	// This makes durable what was previously only an ephemeral
	// WebSocket-only "retrying" status literal (engine/runner.go). Equivalent
	// to the issue's canonical "retry scheduled" type.
	RetryScheduled RunEventType = "retry.scheduled"
)

// RunEvent is a single immutable, append-only fact about a run or a node
// attempt within a run. Rows are ordered by ID (monotonically increasing,
// assigned by the store on insert) — that order is the authoritative replay
// order for engine.ProjectRun.
//
// NodeID and Attempt are both empty/nil for run-level events and both set
// for node-attempt-level events; the pair addresses a single models.NodeRun
// the same way (NodeID, Attempt) already does.
type RunEvent struct {
	ID            int64           `json:"id"`
	RunID         string          `json:"run_id"`
	NodeID        string          `json:"node_id,omitempty"`
	Attempt       *int            `json:"attempt,omitempty"`
	EventType     RunEventType    `json:"event_type"`
	Payload       RunEventPayload `json:"payload"`
	CreatedAt     time.Time       `json:"created_at"`
	SchemaVersion int             `json:"schema_version"`
}

// RunEventPayload carries the fields needed to project run/node_run state
// from the event stream. Not every field is populated by every event type —
// e.g. RetryScheduled only needs BackoffMs/Error, while RunEventTerminal
// needs Status/FinishedAt/Error. Fields mirror models.Run / models.NodeRun
// so engine.ProjectRun is a near-direct field copy.
type RunEventPayload struct {
	Status     RunStatus `json:"status,omitempty"`
	PipelineID string    `json:"pipeline_id,omitempty"`
	// NodeRunID is the models.NodeRun.ID this event pertains to. Only set on
	// node-attempt-level events; it lets projection reconstruct the exact
	// same row identity the store generated, rather than a new one.
	NodeRunID  string     `json:"node_run_id,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	ReadyAt    *time.Time `json:"ready_at,omitempty"`
	Error      string     `json:"error,omitempty"`
	RowCount   int        `json:"row_count,omitempty"`
	DurationMs int64      `json:"duration_ms,omitempty"`
	QueueMs    int64      `json:"queue_ms,omitempty"`
	RowsPerSec float64    `json:"rows_per_sec,omitempty"`
	TraceID    string     `json:"trace_id,omitempty"`
	SpanID     string     `json:"span_id,omitempty"`
	// BackoffMs is the computed retry delay (RetryScheduled only).
	BackoffMs int64             `json:"backoff_ms,omitempty"`
	Params    map[string]string `json:"params,omitempty"`
	// PipelineVersion and ResumedFromRunID mirror models.Run's fields of the
	// same name (Tnsor-Labs/brokoli#8) — set on RunEventCreated only, so a
	// full event-log replay (engine.ProjectRun) reconstructs the same
	// pinned-version/lineage identity the live runs row has, not just the
	// fields #6 originally covered.
	PipelineVersion  int    `json:"pipeline_version,omitempty"`
	ResumedFromRunID string `json:"resumed_from_run_id,omitempty"`
}
