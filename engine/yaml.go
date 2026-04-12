package engine

import (
	"fmt"
	"strings"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"gopkg.in/yaml.v3"
)

const yamlFormatVersion = "1"

// PipelineYAML is the YAML-serializable pipeline format.
type PipelineYAML struct {
	Version     string     `yaml:"version,omitempty"`
	Name        string     `yaml:"name"`
	Description string     `yaml:"description,omitempty"`
	Schedule    string     `yaml:"schedule,omitempty"`
	Enabled     *bool      `yaml:"enabled,omitempty"`
	Tags        []string   `yaml:"tags,omitempty"`
	Nodes       []NodeYAML `yaml:"nodes"`
	Edges       []EdgeYAML `yaml:"edges"`
}

// NodeYAML is the YAML format for a pipeline node.
type NodeYAML struct {
	ID       string                 `yaml:"id"`
	Type     string                 `yaml:"type"`
	Name     string                 `yaml:"name"`
	Config   map[string]interface{} `yaml:"config,omitempty"`
	Position *PositionYAML          `yaml:"position,omitempty"`
}

// PositionYAML is the optional canvas position.
type PositionYAML struct {
	X float64 `yaml:"x"`
	Y float64 `yaml:"y"`
}

// EdgeYAML is the YAML format for an edge.
type EdgeYAML struct {
	From string `yaml:"from"`
	To   string `yaml:"to"`
}

// internalConfigKeys are implementation-detail keys that should be stripped
// from export so the YAML stays human-readable and forward-compatible.
var internalConfigKeys = map[string]bool{
	"_schema_hint": true,
}

// ImportPipelineYAML parses YAML bytes into a Pipeline model.
// Validates edge endpoints, node types, and auto-generates layout positions when missing.
func ImportPipelineYAML(data []byte) (*models.Pipeline, error) {
	var py PipelineYAML
	if err := yaml.Unmarshal(data, &py); err != nil {
		return nil, fmt.Errorf("parse YAML: %w", err)
	}

	if py.Name == "" {
		return nil, fmt.Errorf("pipeline name is required")
	}
	if len(py.Nodes) == 0 {
		return nil, fmt.Errorf("pipeline must have at least one node")
	}

	now := time.Now()
	p := &models.Pipeline{
		ID:          common.NewID(),
		Name:        py.Name,
		Description: py.Description,
		Schedule:    py.Schedule,
		Tags:        py.Tags,
		Enabled:     true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if py.Enabled != nil {
		p.Enabled = *py.Enabled
	}

	nodeIDs := make(map[string]bool, len(py.Nodes))
	needsLayout := false

	for _, ny := range py.Nodes {
		if ny.ID == "" {
			return nil, fmt.Errorf("node missing id")
		}
		if nodeIDs[ny.ID] {
			return nil, fmt.Errorf("duplicate node id: %s", ny.ID)
		}
		if ny.Type == "" {
			return nil, fmt.Errorf("node %q missing type", ny.ID)
		}
		if !isKnownNodeType(ny.Type) {
			return nil, fmt.Errorf("node %q has unknown type %q", ny.ID, ny.Type)
		}
		nodeIDs[ny.ID] = true

		node := models.Node{
			ID:     ny.ID,
			Type:   models.NodeType(ny.Type),
			Name:   ny.Name,
			Config: ny.Config,
		}
		if ny.Position != nil {
			node.Position = models.Position{X: ny.Position.X, Y: ny.Position.Y}
		} else {
			needsLayout = true
		}
		if node.Config == nil {
			node.Config = make(map[string]interface{})
		}

		if err := validateNodeConfigImport(node); err != nil {
			return nil, fmt.Errorf("node %q (%s): %w", ny.ID, ny.Name, err)
		}

		p.Nodes = append(p.Nodes, node)
	}

	for _, ey := range py.Edges {
		if !nodeIDs[ey.From] {
			return nil, fmt.Errorf("edge references unknown source node: %s", ey.From)
		}
		if !nodeIDs[ey.To] {
			return nil, fmt.Errorf("edge references unknown target node: %s", ey.To)
		}
		if ey.From == ey.To {
			return nil, fmt.Errorf("self-referencing edge: %s", ey.From)
		}
		p.Edges = append(p.Edges, models.Edge{From: ey.From, To: ey.To})
	}

	if needsLayout {
		autoLayoutNodes(p)
	}

	return p, nil
}

// ExportPipelineYAML converts a Pipeline model to YAML bytes.
// Strips internal config keys and adds a format version.
func ExportPipelineYAML(p *models.Pipeline) ([]byte, error) {
	py := PipelineYAML{
		Version:     yamlFormatVersion,
		Name:        p.Name,
		Description: p.Description,
		Schedule:    p.Schedule,
		Tags:        p.Tags,
	}

	if !p.Enabled {
		f := false
		py.Enabled = &f
	}

	for _, n := range p.Nodes {
		cleanConfig := stripInternalKeys(n.Config)
		ny := NodeYAML{
			ID:     n.ID,
			Type:   string(n.Type),
			Name:   n.Name,
			Config: cleanConfig,
		}
		if n.Position.X != 0 || n.Position.Y != 0 {
			ny.Position = &PositionYAML{X: n.Position.X, Y: n.Position.Y}
		}
		py.Nodes = append(py.Nodes, ny)
	}

	for _, e := range p.Edges {
		py.Edges = append(py.Edges, EdgeYAML{From: e.From, To: e.To})
	}

	data, err := yaml.Marshal(&py)
	if err != nil {
		return nil, fmt.Errorf("marshal YAML: %w", err)
	}
	return data, nil
}

// stripInternalKeys returns a shallow copy of the config with internal
// implementation-detail keys removed so the YAML export stays clean.
func stripInternalKeys(cfg map[string]interface{}) map[string]interface{} {
	if len(cfg) == 0 {
		return cfg
	}
	out := make(map[string]interface{}, len(cfg))
	for k, v := range cfg {
		if internalConfigKeys[k] {
			continue
		}
		out[k] = v
	}
	return out
}

// isKnownNodeType reports whether the given type string is a recognized node type.
func isKnownNodeType(t string) bool {
	switch models.NodeType(t) {
	case models.NodeTypeSourceFile, models.NodeTypeSourceAPI, models.NodeTypeSourceDB,
		models.NodeTypeTransform, models.NodeTypeQualityCheck, models.NodeTypeSQLGenerate,
		models.NodeTypeCode, models.NodeTypeJoin, models.NodeTypeSinkFile,
		models.NodeTypeSinkDB, models.NodeTypeSinkAPI, models.NodeTypeMigrate,
		models.NodeTypeCondition, models.NodeTypeDBT, models.NodeTypeNotify:
		return true
	}
	return false
}

// validateNodeConfigImport checks required config fields per node type.
// This is a best-effort check at import time; the engine does a deeper validation at runtime.
func validateNodeConfigImport(n models.Node) error {
	cfg := n.Config
	switch n.Type {
	case models.NodeTypeSourceAPI:
		if _, ok := cfg["url"]; !ok {
			return fmt.Errorf("source_api requires 'url' in config")
		}
	case models.NodeTypeSourceFile:
		if _, ok := cfg["path"]; !ok {
			return fmt.Errorf("source_file requires 'path' in config")
		}
	case models.NodeTypeSourceDB:
		if _, ok := cfg["query"]; !ok {
			return fmt.Errorf("source_db requires 'query' in config")
		}
	case models.NodeTypeSinkFile:
		if _, ok := cfg["path"]; !ok {
			return fmt.Errorf("sink_file requires 'path' in config")
		}
	case models.NodeTypeSinkDB:
		if _, ok := cfg["table"]; !ok {
			return fmt.Errorf("sink_db requires 'table' in config")
		}
	case models.NodeTypeJoin:
		if _, ok := cfg["left_key"]; !ok {
			return fmt.Errorf("join requires 'left_key' in config")
		}
		if _, ok := cfg["right_key"]; !ok {
			return fmt.Errorf("join requires 'right_key' in config")
		}
	case models.NodeTypeCondition:
		if _, ok := cfg["expression"]; !ok {
			return fmt.Errorf("condition requires 'expression' in config")
		}
	}
	return nil
}

// autoLayoutNodes assigns positions to nodes that are missing them, using a
// simple topological-level layout (layer by longest path from roots, stack
// within layers). This is server-side only — the UI has its own richer layout.
func autoLayoutNodes(p *models.Pipeline) {
	nodeIndex := make(map[string]int, len(p.Nodes))
	for i, n := range p.Nodes {
		nodeIndex[n.ID] = i
	}

	// Build incoming edge count for topological level assignment.
	incoming := make(map[string][]string, len(p.Nodes))
	for _, n := range p.Nodes {
		incoming[n.ID] = nil
	}
	for _, e := range p.Edges {
		incoming[e.To] = append(incoming[e.To], e.From)
	}

	// Longest-path level assignment.
	level := make(map[string]int, len(p.Nodes))
	visiting := make(map[string]bool)
	var assignLevel func(id string) int
	assignLevel = func(id string) int {
		if l, ok := level[id]; ok {
			return l
		}
		if visiting[id] {
			return 0
		}
		visiting[id] = true
		parents := incoming[id]
		if len(parents) == 0 {
			level[id] = 0
			return 0
		}
		maxParent := 0
		for _, pid := range parents {
			if l := assignLevel(pid); l > maxParent {
				maxParent = l
			}
		}
		level[id] = maxParent + 1
		return maxParent + 1
	}
	for _, n := range p.Nodes {
		assignLevel(n.ID)
	}

	// Group by level, assign positions.
	byLevel := make(map[int][]string)
	for _, n := range p.Nodes {
		byLevel[level[n.ID]] = append(byLevel[level[n.ID]], n.ID)
	}

	const (
		colWidth  = 280.0
		rowHeight = 100.0
		startX    = 50.0
		startY    = 50.0
	)

	for lv, ids := range byLevel {
		for row, id := range ids {
			idx := nodeIndex[id]
			if p.Nodes[idx].Position.X == 0 && p.Nodes[idx].Position.Y == 0 {
				p.Nodes[idx].Position = models.Position{
					X: startX + float64(lv)*colWidth,
					Y: startY + float64(row)*rowHeight,
				}
			}
		}
	}
}

// sanitizeConfigValue ensures config values don't contain YAML injection payloads.
// Rejects multiline YAML directives and anchors in string values.
func sanitizeConfigValue(v interface{}) interface{} {
	s, ok := v.(string)
	if !ok {
		return v
	}
	if strings.Contains(s, "!!") || strings.HasPrefix(s, "*") || strings.HasPrefix(s, "&") {
		return strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(s, "!!", ""), "&", ""), "*", "")
	}
	return v
}
