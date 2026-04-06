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
	NodeRuns   []NodeRun         `json:"node_runs"`
}

// PopulateError sets the Error field from the first failed NodeRun.
func (r *Run) PopulateError() {
	if r.Error != "" || r.Status != RunStatusFailed {
		return
	}
	for _, nr := range r.NodeRuns {
		if nr.Error != "" {
			r.Error = nr.Error
			return
		}
	}
}

// NodeRun represents the execution of a single node within a pipeline run.
type NodeRun struct {
	ID         string     `json:"id"`
	RunID      string     `json:"run_id"`
	NodeID     string     `json:"node_id"`
	Status     RunStatus  `json:"status"`
	RowCount   int        `json:"row_count"`
	StartedAt  *time.Time `json:"started_at"`
	DurationMs int64      `json:"duration_ms"`
	Error      string     `json:"error,omitempty"`
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
	RunID     string    `json:"run_id"`
	NodeID    string    `json:"node_id"`
	Level     LogLevel  `json:"level"`
	Message   string    `json:"message"`
	Timestamp time.Time `json:"timestamp"`
}
