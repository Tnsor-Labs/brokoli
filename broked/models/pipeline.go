package models

import "time"

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
	Enabled     bool              `json:"enabled"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

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

// Edge represents a directed connection between two nodes.
type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
}
