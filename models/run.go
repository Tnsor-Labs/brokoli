package models

import "time"

// RunStatus represents the lifecycle state of a pipeline run.
type RunStatus string

const (
	RunStatusPending   RunStatus = "pending"
	RunStatusRunning   RunStatus = "running"
	RunStatusSuccess   RunStatus = "success"
	RunStatusFailed    RunStatus = "failed"
	RunStatusCancelled RunStatus = "cancelled"
	RunStatusBlocked   RunStatus = "blocked" // dependencies not satisfied, trigger skipped
	// RunStatusWaiting: parked on a wait node (#399, deferrable waits).
	// Non-terminal, holds NO run slot; the leader's wait watcher owns
	// waking it (store parked_waits), recovery deliberately does not.
	RunStatusWaiting RunStatus = "waiting"
	// RunStatusSkipped is a node-only terminal state for work made inactive
	// by conditional routing. A pipeline Run itself is never skipped.
	RunStatusSkipped RunStatus = "skipped"
)

// RunTriggerScheduled marks a run created by the scheduler for a cron
// tick (or its catch-up pass). The empty string remains "everything
// else" -- manual, API, webhook -- undistinguished, as historically.
const RunTriggerScheduled = "scheduled"

// RunTriggerBackfill marks a run created by a backfill (ADR-028 phase 3).
// Deliberately outside the scheduled-dispatch unique index: re-running an
// attempted interval is what a backfill is for, and history appends.
const RunTriggerBackfill = "backfill"

// Run represents a single execution of a pipeline.
type Run struct {
	ID         string            `json:"id"`
	PipelineID string            `json:"pipeline_id"`
	Status     RunStatus         `json:"status"`
	Error      string            `json:"error,omitempty"`  // top-level error (from first failed node)
	Params     map[string]string `json:"params,omitempty"` // legacy untyped runtime parameter overrides (ADR-032 section 3)
	// Parameters is the resolved, typed run-parameter snapshot (ADR-032
	// section 3 rule 5): the immutable object the control plane produces
	// by validating submitted values against Pipeline.Parameters and
	// applying declared defaults, at the point this run is created.
	// Distinct from Params above and never silently merged with it. Empty
	// when the pipeline declares no typed parameters -- not every run has
	// one, and absence here is honest, not an omission.
	Parameters map[string]interface{} `json:"parameters,omitempty"`
	StartedAt  *time.Time             `json:"started_at"`
	FinishedAt *time.Time             `json:"finished_at"`
	TraceID    string                 `json:"trace_id,omitempty"` // unique correlation ID for distributed tracing
	NodeRuns   []NodeRun              `json:"node_runs"`

	// PipelineVersion pins this run to the exact store.PipelineVersion
	// snapshot (store.Store.GetPipelineVersion) of the pipeline definition
	// it executes against, resolved once at run-creation time from the
	// pipeline's current version. This is what makes a run — and especially
	// a resumed run — immune to edits made to the live pipeline afterward
	// (Tnsor-Labs/brokoli#8): engine.Engine no longer re-fetches the live,
	// mutable pipeline row when resolving what DAG to execute or resume.
	// Zero means no version was recorded (a run created before this field
	// existed) — callers fall back to the live pipeline definition, the
	// previous behavior, rather than treating 0 as a real version number.
	PipelineVersion int `json:"pipeline_version,omitempty"`

	// Trigger records what created this run: "scheduled" for the
	// scheduler's cron ticks, "" for everything else today (manual, API,
	// webhook -- indistinguishable historically, and left so rather than
	// guessed). ADR-028's backfill adds its own value later. The partial
	// unique index guarding scheduled dispatch keys on this, which is why
	// it is a recorded fact rather than an inference from other fields.
	Trigger string `json:"trigger,omitempty"`

	// DataIntervalStart/End are the half-open data interval [start, end)
	// this run is responsible for (ADR-028), stamped by whatever created
	// the run. Nil for every run that predates the field and for every
	// manual run: absence means "no interval", never "empty interval".
	DataIntervalStart *time.Time `json:"data_interval_start,omitempty"`
	DataIntervalEnd   *time.Time `json:"data_interval_end,omitempty"`

	// ResumedFromRunID is the ID of the run this run was resumed from, set
	// by Engine.ResumeRun. Empty for a run that was not created via resume.
	// This is the lineage pointer the issue asks for — it also identifies
	// which run's artifact store entries a skipped node's durable output
	// should be restored from.
	ResumedFromRunID string `json:"resumed_from_run_id,omitempty"`

	// CancelRequested records a durable cancellation intent
	// (store.RunCancelRequester), set by Engine.CancelRun before it acts.
	// It exists because the acting half of a cancel can be lost — the
	// relay broadcast is fire-and-forget, and a process can die between
	// receiving a cancel and finalizing the run — while this flag cannot.
	// The Runner re-checks it at every wave boundary and recovery honors
	// it when closing out a run with no recoverable path, so a cancel
	// whose delivery was lost still converges to cancelled instead of the
	// run silently completing or being recovery-failed. Never cleared:
	// terminal runs simply stop consulting it.
	CancelRequested bool `json:"cancel_requested,omitempty"`

	// OrgID is the owning pipeline's org at the moment this run was
	// created (copied from Pipeline.OrgID, not re-resolved later — a
	// pipeline moved to a different org afterward doesn't retroactively
	// change which org an already-created run belongs to). Every
	// engine.Engine run-creation path already resolves this into
	// Runner.orgID for WebSocket/SODP tenant isolation
	// (Tnsor-Labs/brokoli#50) — this field is what makes it durable on the
	// row itself, which store.Store's org-scoped run queries
	// (PurgeRunsOlderThanByOrg, GetRunCalendarByOrg,
	// ListRunIDsOlderThanByOrg) depend on to actually match anything
	// beyond the schema default.
	OrgID string `json:"org_id,omitempty"`
}

// PopulateError sets the Error field from the final failed NodeRun.
// With per-attempt NodeRuns, only considers the highest attempt per node.
func (r *Run) PopulateError() {
	if r.Error != "" || r.Status != RunStatusFailed {
		return
	}
	// Find the max attempt per node
	maxAttempt := make(map[string]int)
	for _, nr := range r.NodeRuns {
		if nr.Attempt > maxAttempt[nr.NodeID] {
			maxAttempt[nr.NodeID] = nr.Attempt
		}
	}
	for _, nr := range r.NodeRuns {
		if nr.Error != "" && nr.Attempt == maxAttempt[nr.NodeID] {
			r.Error = nr.Error
			return
		}
	}
}

// NodeRun represents the execution of a single node within a pipeline run.
// With retries, there is one NodeRun per attempt (Attempt=0 for first try).
type NodeRun struct {
	ID         string     `json:"id"`
	RunID      string     `json:"run_id"`
	NodeID     string     `json:"node_id"`
	Status     RunStatus  `json:"status"`
	RowCount   int        `json:"row_count"`
	StartedAt  *time.Time `json:"started_at"`
	DurationMs int64      `json:"duration_ms"`
	Error      string     `json:"error,omitempty"`
	Attempt    int        `json:"attempt"`            // 0=first try, 1=first retry, etc.
	ReadyAt    *time.Time `json:"ready_at,omitempty"` // when all deps finished (for queue wait calc)
	QueueMs    int64      `json:"queue_ms"`           // ms between ready and started
	RowsPerSec float64    `json:"rows_per_sec"`       // throughput: rows / (duration_ms / 1000)
	TraceID    string     `json:"trace_id,omitempty"` // correlation ID (same as run)
	SpanID     string     `json:"span_id,omitempty"`  // unique per attempt
}

// LogLevel represents the severity of a log entry.
type LogLevel string

const (
	LogLevelDebug   LogLevel = "debug"
	LogLevelInfo    LogLevel = "info"
	LogLevelWarning LogLevel = "warning"
	LogLevelError   LogLevel = "error"
)

// LogEntry represents a single log line from a pipeline run.
type LogEntry struct {
	RunID     string            `json:"run_id"`
	NodeID    string            `json:"node_id"`
	Level     LogLevel          `json:"level"`
	Message   string            `json:"message"`
	Timestamp time.Time         `json:"timestamp"`
	TraceID   string            `json:"trace_id,omitempty"` // correlation ID (same as run)
	SpanID    string            `json:"span_id,omitempty"`  // unique per node attempt
	Attempt   int               `json:"attempt,omitempty"`  // retry attempt number
	Metadata  map[string]string `json:"metadata,omitempty"` // structured key-value pairs
}

// ParkedWait is one deferred wait (#399): a run parked at a wait node,
// owned by the store so it survives instance restarts. The condition is
// the node's config, serialized; the watcher re-parses it on every poll
// so a config format change cannot leave stale in-memory parses running.
type ParkedWait struct {
	RunID        string    `json:"run_id"`
	PipelineID   string    `json:"pipeline_id"`
	NodeID       string    `json:"node_id"`
	Condition    string    `json:"condition"` // the wait node's config, JSON
	PollInterval int64     `json:"poll_interval_ms"`
	NextPollAt   time.Time `json:"next_poll_at"`
	ExpiresAt    time.Time `json:"expires_at"`
	CreatedAt    time.Time `json:"created_at"`
}
