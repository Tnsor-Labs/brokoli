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

	// AttemptSkipped records a synthetic node_runs row for a node that did
	// not execute because every incoming edge resolved inactive.
	AttemptSkipped RunEventType = "attempt.skipped"

	// RetryScheduled marks the moment a retry is decided and its backoff
	// delay computed, before the next attempt's node_runs row is created.
	// This makes durable what was previously only an ephemeral
	// WebSocket-only "retrying" status literal (engine/runner.go). Equivalent
	// to the issue's canonical "retry scheduled" type.
	RetryScheduled RunEventType = "retry.scheduled"

	// RunEventRecoveryStarted marks the moment startup recovery
	// (Tnsor-Labs/brokoli#9) begins examining a run it found left in a
	// non-terminal status by a prior process death. Informational only —
	// does not change runs.status. Appended exactly once per recovery pass
	// that looks at a given run; a run that is still non-terminal because
	// recovery deferred to a possibly-live owner (see
	// store.ExecutionAttemptStore's lease contract) will show a
	// RunEventRecoveryStarted with no matching Completed/Failed until a
	// later boot resolves it — that gap is itself the audit trail of the
	// deferral.
	RunEventRecoveryStarted RunEventType = "run.recovery_started"

	// RunEventRecoveryCompleted marks startup recovery successfully
	// determining a non-terminal run's true outcome from its event log and
	// reconciling the runs/node_runs snapshot to match — either because the
	// snapshot had simply gone stale (fixed by re-syncing it) or because the
	// run's node-attempt history showed a definite success or failure that
	// the live process never got to persist before it died (fixed by
	// appending the RunEventTerminal that process would have appended).
	// Informational only; the state change itself is carried by the
	// RunEventTerminal event this is paired with, not by this event's own
	// payload.
	RunEventRecoveryCompleted RunEventType = "run.recovery_completed"

	// RunEventRecoveryFailed marks startup recovery concluding a
	// non-terminal run has no recoverable path — its node-attempt history
	// is incomplete or ambiguous (e.g. a node's last recorded attempt was
	// still "running" when the process died, or no node ever started) so
	// there is no way to tell what the live process would have persisted.
	// Unlike RunEventRecoveryCompleted, this event itself carries the
	// terminal state transition (Payload.Status is always
	// RunStatusFailed) — recovery marks the run failed with a specific
	// reason (Payload.Error) rather than leaving it stuck non-terminal
	// forever.
	RunEventRecoveryFailed RunEventType = "run.recovery_failed"

	// RunEventRecoveryRequeued marks startup recovery putting an
	// interrupted run back on the job queue rather than failing it
	// (Tnsor-Labs/brokoli#289).
	//
	// Recovery's default for an interrupted run is to fail it, because it
	// cannot tell whether a node's side effect completed. This event is
	// the narrower case where it can: every node with a durable record was
	// of a type that provably cannot write outside Brokoli, so re-running
	// from the beginning has nothing to duplicate. Worker eviction and
	// node drains make this the ordinary shape of an interruption.
	//
	// Like RunEventRecoveryFailed, this event carries its own state
	// transition — Payload.Status is always RunStatusPending — because the
	// run is going back to where it was before a worker claimed it. The
	// event log therefore records every automatic re-queue, which is both
	// the audit trail and what bounds them: recovery counts these to stop
	// a run cycling forever.
	RunEventRecoveryRequeued RunEventType = "run.recovery_requeued"
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
	// ConditionResult is the durable routing decision produced by a
	// successful IR 2.1 condition attempt. Nil for every other attempt.
	ConditionResult *bool `json:"condition_result,omitempty"`
	// Reused distinguishes a resume-restored node from a routing-inactive
	// skipped node. Only reused skips are eligible for another resume.
	Reused bool `json:"reused,omitempty"`
	// ArtifactRunID identifies the run namespace containing a reused node's
	// carried output. Empty when the node has no downstream artifact.
	ArtifactRunID string `json:"artifact_run_id,omitempty"`
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
