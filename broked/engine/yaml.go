package engine

import (
	"fmt"
	"time"

	"github.com/hc12r/brokolisql-go/pkg/common"
	"github.com/hc12r/broked/models"
	"gopkg.in/yaml.v3"
)

// PipelineYAML is the YAML-serializable pipeline format.
type PipelineYAML struct {
	Name        string     `yaml:"name"`
	Description string     `yaml:"description,omitempty"`
	Schedule    string     `yaml:"schedule,omitempty"`
	Enabled     *bool      `yaml:"enabled,omitempty"`
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

// ImportPipelineYAML parses YAML bytes into a Pipeline model.
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
		Enabled:     true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if py.Enabled != nil {
		p.Enabled = *py.Enabled
	}

	for _, ny := range py.Nodes {
		node := models.Node{
			ID:     ny.ID,
			Type:   models.NodeType(ny.Type),
			Name:   ny.Name,
			Config: ny.Config,
		}
		if ny.Position != nil {
			node.Position = models.Position{X: ny.Position.X, Y: ny.Position.Y}
		}
		if node.Config == nil {
			node.Config = make(map[string]interface{})
		}
		p.Nodes = append(p.Nodes, node)
	}

	for _, ey := range py.Edges {
		p.Edges = append(p.Edges, models.Edge{From: ey.From, To: ey.To})
	}

	return p, nil
}

// ExportPipelineYAML converts a Pipeline model to YAML bytes.
func ExportPipelineYAML(p *models.Pipeline) ([]byte, error) {
	py := PipelineYAML{
		Name:        p.Name,
		Description: p.Description,
		Schedule:    p.Schedule,
	}

	if !p.Enabled {
		f := false
		py.Enabled = &f
	}

	for _, n := range p.Nodes {
		ny := NodeYAML{
			ID:     n.ID,
			Type:   string(n.Type),
			Name:   n.Name,
			Config: n.Config,
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
