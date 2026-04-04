package models

import (
	"fmt"
	"strings"
	"time"
)

// Pipeline represents a data processing pipeline with its nodes and edges.
type Pipeline struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Nodes       []Node            `json:"nodes"`
	Edges       []Edge            `json:"edges"`
	Schedule    string            `json:"schedule"`    // cron expression, empty for manual-only
	WebhookURL  string            `json:"webhook_url"` // URL for event notifications
	Params      map[string]string `json:"params"`      // default parameter values
	Tags        []string          `json:"tags"`        // labels for filtering/grouping
	Hooks       map[string]Hook   `json:"hooks,omitempty"` // on_start, on_success, on_failure, on_node_failure
	ScheduleTimezone string          `json:"schedule_timezone,omitempty"` // e.g. "America/New_York", defaults to UTC
	SLADeadline  string            `json:"sla_deadline,omitempty"`  // "HH:MM" — must complete by this time daily
	SLATimezone  string            `json:"sla_timezone,omitempty"`  // e.g. "America/New_York", defaults to UTC
	DependsOn    []string          `json:"depends_on,omitempty"`    // pipeline IDs that must succeed before this runs
	WebhookToken string            `json:"webhook_token,omitempty"` // token for triggering via webhook
	Enabled      bool              `json:"enabled"`
	PipelineID   string            `json:"pipeline_id"`             // stable slug for git-sync matching
	Source       string            `json:"source"`                  // "ui" or "git"
	WorkspaceID  string            `json:"workspace_id,omitempty"`  // workspace isolation
	OrgID        string            `json:"org_id,omitempty"`        // tenant isolation
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

const (
	PipelineSourceUI  = "ui"
	PipelineSourceGit = "git"
)

// NodeType defines the kind of operation a node performs.
type NodeType string

const (
	NodeTypeSourceFile   NodeType = "source_file"
	NodeTypeSourceAPI    NodeType = "source_api"
	NodeTypeSourceDB     NodeType = "source_db"
	NodeTypeTransform    NodeType = "transform"
	NodeTypeQualityCheck NodeType = "quality_check"
	NodeTypeSQLGenerate  NodeType = "sql_generate"
	NodeTypeCode         NodeType = "code"
	NodeTypeJoin         NodeType = "join"
	NodeTypeSinkFile     NodeType = "sink_file"
	NodeTypeSinkDB       NodeType = "sink_db"
	NodeTypeSinkAPI      NodeType = "sink_api"
	NodeTypeMigrate      NodeType = "migrate"
	NodeTypeCondition    NodeType = "condition"     // if/else branching
)

// Position represents a node's position on the visual canvas.
type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Node represents a single processing step in a pipeline.
type Node struct {
	ID       string                 `json:"id"`
	Type     NodeType               `json:"type"`
	Name     string                 `json:"name"`
	Config   map[string]interface{} `json:"config"`
	Position Position               `json:"position"`
}

// Hook defines a lifecycle callback (webhook, slack, email).
type Hook struct {
	Type    string            `json:"type"`    // webhook, slack, email
	URL     string            `json:"url"`     // webhook URL or Slack webhook
	Enabled bool              `json:"enabled"`
	Extra   map[string]string `json:"extra,omitempty"` // additional config
}

// Validate checks a pipeline for structural errors before persisting.
func (p *Pipeline) Validate() error {
	if p.Name == "" {
		return fmt.Errorf("pipeline name is required")
	}
	if strings.ContainsAny(p.Name, "<>\"'&") {
		return fmt.Errorf("pipeline name contains invalid characters")
	}
	if len(p.Name) > 255 {
		return fmt.Errorf("pipeline name too long (max 255 characters)")
	}
	if len(p.Description) > 2000 {
		return fmt.Errorf("description too long (max 2000 characters)")
	}
	if len(p.Nodes) > 500 {
		return fmt.Errorf("too many nodes (max 500)")
	}
	if p.ScheduleTimezone != "" {
		if _, err := time.LoadLocation(p.ScheduleTimezone); err != nil {
			return fmt.Errorf("invalid schedule timezone %q: %w", p.ScheduleTimezone, err)
		}
	}
	if p.SLATimezone != "" {
		if _, err := time.LoadLocation(p.SLATimezone); err != nil {
			return fmt.Errorf("invalid timezone %q: %w", p.SLATimezone, err)
		}
	}
	if p.SLADeadline != "" {
		parts := strings.SplitN(p.SLADeadline, ":", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid SLA deadline format, expected HH:MM")
		}
	}
	// Check for duplicate node IDs
	seen := make(map[string]bool)
	for _, n := range p.Nodes {
		if seen[n.ID] {
			return fmt.Errorf("duplicate node ID: %s", n.ID)
		}
		seen[n.ID] = true
	}
	// Validate edges reference existing nodes
	for _, e := range p.Edges {
		if !seen[e.From] {
			return fmt.Errorf("edge references unknown source node: %s", e.From)
		}
		if !seen[e.To] {
			return fmt.Errorf("edge references unknown target node: %s", e.To)
		}
	}
	return nil
}

// Edge represents a directed connection between two nodes.
type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
}
