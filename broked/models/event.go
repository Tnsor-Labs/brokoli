package models

import "time"

// EventType identifies the kind of real-time event broadcast via WebSocket.
type EventType string

const (
	EventRunStarted    EventType = "run.started"
	EventRunCompleted  EventType = "run.completed"
	EventRunFailed     EventType = "run.failed"
	EventNodeStarted   EventType = "node.started"
	EventNodeCompleted EventType = "node.completed"
	EventNodeFailed    EventType = "node.failed"
	EventLog           EventType = "log"
)

// Event is a real-time message sent to WebSocket clients.
type Event struct {
	Type       EventType   `json:"type"`
	RunID      string      `json:"run_id"`
	PipelineID string      `json:"pipeline_id,omitempty"`
	NodeID     string      `json:"node_id,omitempty"`
	Status     RunStatus   `json:"status,omitempty"`
	RowCount   int         `json:"row_count,omitempty"`
	DurationMs int64       `json:"duration_ms,omitempty"`
	Error      string      `json:"error,omitempty"`
	Level      LogLevel    `json:"level,omitempty"`
	Message    string      `json:"message,omitempty"`
	Timestamp  time.Time   `json:"timestamp"`
}
