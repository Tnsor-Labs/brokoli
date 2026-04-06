package engine

import (
	"testing"
)

func TestYAMLRoundTrip(t *testing.T) {
	yaml := `
name: test-pipeline
description: A test
schedule: "0 2 * * *"
nodes:
  - id: n1
    type: source_file
    name: Load
    config:
      path: /data/test.csv
    position:
      x: 40
      y: 100
  - id: n2
    type: sql_generate
    name: Generate SQL
    config:
      dialect: postgres
      table: users
    position:
      x: 300
      y: 100
edges:
  - from: n1
    to: n2
`

	// Import
	p, err := ImportPipelineYAML([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "test-pipeline" {
		t.Errorf("Name = %q, want %q", p.Name, "test-pipeline")
	}
	if p.Schedule != "0 2 * * *" {
		t.Errorf("Schedule = %q, want %q", p.Schedule, "0 2 * * *")
	}
	if len(p.Nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(p.Nodes))
	}
	if len(p.Edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(p.Edges))
	}
	if p.Nodes[0].Position.X != 40 {
		t.Errorf("Node[0].Position.X = %v, want 40", p.Nodes[0].Position.X)
	}
	if string(p.Nodes[1].Type) != "sql_generate" {
		t.Errorf("Node[1].Type = %q, want %q", p.Nodes[1].Type, "sql_generate")
	}

	// Export
	exported, err := ExportPipelineYAML(p)
	if err != nil {
		t.Fatal(err)
	}

	// Re-import exported
	p2, err := ImportPipelineYAML(exported)
	if err != nil {
		t.Fatal(err)
	}
	if p2.Name != p.Name {
		t.Errorf("round-trip Name = %q, want %q", p2.Name, p.Name)
	}
	if len(p2.Nodes) != len(p.Nodes) {
		t.Errorf("round-trip nodes count = %d, want %d", len(p2.Nodes), len(p.Nodes))
	}
	if len(p2.Edges) != len(p.Edges) {
		t.Errorf("round-trip edges count = %d, want %d", len(p2.Edges), len(p.Edges))
	}
}

func TestYAMLImport_MinimalValid(t *testing.T) {
	yaml := `
name: minimal
nodes:
  - id: n1
    type: source_file
    name: Load
edges: []
`
	p, err := ImportPipelineYAML([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	if !p.Enabled {
		t.Error("should default to enabled")
	}
	if p.ID == "" {
		t.Error("should generate an ID")
	}
}

func TestYAMLImport_MissingName(t *testing.T) {
	yaml := `
nodes:
  - id: n1
    type: source_file
    name: Load
`
	_, err := ImportPipelineYAML([]byte(yaml))
	if err == nil {
		t.Error("should error on missing name")
	}
}

func TestYAMLImport_NoNodes(t *testing.T) {
	yaml := `
name: empty
nodes: []
`
	_, err := ImportPipelineYAML([]byte(yaml))
	if err == nil {
		t.Error("should error on empty nodes")
	}
}

func TestYAMLImport_InvalidYAML(t *testing.T) {
	_, err := ImportPipelineYAML([]byte(`{{{invalid`))
	if err == nil {
		t.Error("should error on invalid YAML")
	}
}

func TestYAMLExport_DisabledPipeline(t *testing.T) {
	yaml := `
name: disabled-test
enabled: false
nodes:
  - id: n1
    type: source_file
    name: Load
edges: []
`
	p, err := ImportPipelineYAML([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	if p.Enabled {
		t.Error("should be disabled")
	}

	exported, err := ExportPipelineYAML(p)
	if err != nil {
		t.Fatal(err)
	}

	p2, err := ImportPipelineYAML(exported)
	if err != nil {
		t.Fatal(err)
	}
	if p2.Enabled {
		t.Error("round-trip should preserve disabled state")
	}
}
