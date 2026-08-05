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
)

// Run represents a single execution of a pipeline.
type Run struct {
	ID         string            `json:"id"`
	PipelineID string            `json:"pipeline_id"`
	Status     RunStatus         `json:"status"`
	Error      string            `json:"error,omitempty"`  // top-level error (from first failed node)
	Params     map[string]string `json:"params,omitempty"` // runtime parameter overrides
	StartedAt  *time.Time        `json:"started_at"`
	FinishedAt *time.Time        `json:"finished_at"`
	TraceID    string            `json:"trace_id,omitempty"` // unique correlation ID for distributed tracing
	NodeRuns   []NodeRun         `json:"node_runs"`

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

	// ResumedFromRunID is the ID of the run this run was resumed from, set
	// by Engine.ResumeRun. Empty for a run that was not created via resume.
	// This is the lineage pointer the issue asks for — it also identifies
	// which run's artifact store entries a skipped node's durable output
	// should be restored from.
	ResumedFromRunID string `json:"resumed_from_run_id,omitempty"`

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
