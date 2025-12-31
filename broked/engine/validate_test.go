package engine

import (
	"testing"

	"github.com/hc12r/broked/models"
)

func validPipeline() *models.Pipeline {
	return &models.Pipeline{
		Name: "test",
		Nodes: []models.Node{
			{ID: "n1", Type: models.NodeTypeSourceFile, Name: "Load", Config: map[string]interface{}{"path": "/data/test.csv"}},
			{ID: "n2", Type: models.NodeTypeSinkFile, Name: "Output", Config: map[string]interface{}{"path": "/out.sql"}},
		},
		Edges: []models.Edge{{From: "n1", To: "n2"}},
	}
}

func TestValidate_ValidPipeline(t *testing.T) {
	ve := ValidatePipeline(validPipeline())
	if ve.HasErrors() {
		t.Errorf("expected no errors, got: %v", ve.Errors)
	}
}

func TestValidate_EmptyName(t *testing.T) {
	p := validPipeline()
	p.Name = ""
	ve := ValidatePipeline(p)
	if !ve.HasErrors() {
		t.Error("expected error for empty name")
	}
}

func TestValidate_NoNodes(t *testing.T) {
	p := &models.Pipeline{Name: "test", Nodes: []models.Node{}}
	ve := ValidatePipeline(p)
	if !ve.HasErrors() {
		t.Error("expected error for no nodes")
	}
}

func TestValidate_DuplicateNodeID(t *testing.T) {
	p := validPipeline()
	p.Nodes[1].ID = "n1" // duplicate
	ve := ValidatePipeline(p)
	if !ve.HasErrors() {
		t.Error("expected error for duplicate ID")
	}
}

func TestValidate_InvalidEdgeRef(t *testing.T) {
	p := validPipeline()
	p.Edges = append(p.Edges, models.Edge{From: "n1", To: "nonexistent"})
	ve := ValidatePipeline(p)
	if !ve.HasErrors() {
		t.Error("expected error for invalid edge ref")
	}
}

func TestValidate_Cycle(t *testing.T) {
	p := &models.Pipeline{
		Name: "test",
		Nodes: []models.Node{
			{ID: "a", Type: models.NodeTypeSourceFile, Name: "A", Config: map[string]interface{}{"path": "/a"}},
			{ID: "b", Type: models.NodeTypeTransform, Name: "B", Config: map[string]interface{}{}},
		},
		Edges: []models.Edge{{From: "a", To: "b"}, {From: "b", To: "a"}},
	}
	ve := ValidatePipeline(p)
	if !ve.HasErrors() {
		t.Error("expected error for cycle")
	}
}

func TestValidate_NoSource(t *testing.T) {
	p := &models.Pipeline{
		Name: "test",
		Nodes: []models.Node{
			{ID: "n1", Type: models.NodeTypeSinkFile, Name: "Out", Config: map[string]interface{}{"path": "/out"}},
		},
	}
	ve := ValidatePipeline(p)
	if !ve.HasErrors() {
		t.Error("expected error for no source node")
	}
}

func TestValidate_DisconnectedNode(t *testing.T) {
	p := validPipeline()
	p.Nodes = append(p.Nodes, models.Node{
		ID: "n3", Type: models.NodeTypeTransform, Name: "Orphan", Config: map[string]interface{}{},
	})
	ve := ValidatePipeline(p)
	if !ve.HasErrors() {
		t.Error("expected error for disconnected node")
	}
}

func TestValidate_MissingRequiredConfig(t *testing.T) {
	p := &models.Pipeline{
		Name: "test",
		Nodes: []models.Node{
			{ID: "n1", Type: models.NodeTypeSourceFile, Name: "Load", Config: map[string]interface{}{}}, // missing path
		},
	}
	ve := ValidatePipeline(p)
	if !ve.HasErrors() {
		t.Error("expected error for missing path config")
	}
}

func TestValidate_SelfLoop(t *testing.T) {
	p := validPipeline()
	p.Edges = append(p.Edges, models.Edge{From: "n1", To: "n1"})
	ve := ValidatePipeline(p)
	if !ve.HasErrors() {
		t.Error("expected error for self-loop")
	}
}

// ── ValidateNodes (per-node) tests ──

func TestValidateNodes_AllValid(t *testing.T) {
	nodes := []models.Node{
		{ID: "n1", Type: models.NodeTypeSourceFile, Name: "In", Config: map[string]interface{}{"path": "/data.csv"}},
		{ID: "n2", Type: models.NodeTypeSinkFile, Name: "Out", Config: map[string]interface{}{"path": "/out.csv"}},
	}
	results := ValidateNodes(nodes)
	if len(results) != 0 {
		t.Errorf("expected no issues, got %d", len(results))
	}
}

func TestValidateNodes_MissingSourcePath(t *testing.T) {
	nodes := []models.Node{
		{ID: "n1", Type: models.NodeTypeSourceFile, Name: "In", Config: map[string]interface{}{}},
	}
	results := ValidateNodes(nodes)
	if len(results) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(results))
	}
	if results[0].NodeID != "n1" {
		t.Errorf("expected issue on n1, got %s", results[0].NodeID)
	}
	if len(results[0].Errors) != 1 {
		t.Errorf("expected 1 error, got %d", len(results[0].Errors))
	}
}

func TestValidateNodes_MissingCodeScript(t *testing.T) {
	nodes := []models.Node{
		{ID: "c1", Type: models.NodeTypeCode, Name: "Code", Config: map[string]interface{}{}},
	}
	results := ValidateNodes(nodes)
	if len(results) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(results))
	}
	if len(results[0].Errors) == 0 {
		t.Error("expected error for missing script")
	}
}

func TestValidateNodes_SourceDBMissingBoth(t *testing.T) {
	nodes := []models.Node{
		{ID: "db1", Type: models.NodeTypeSourceDB, Name: "DB", Config: map[string]interface{}{}},
	}
	results := ValidateNodes(nodes)
	if len(results) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(results))
	}
	if len(results[0].Errors) != 2 {
		t.Errorf("expected 2 errors (uri + query), got %d", len(results[0].Errors))
	}
}

func TestValidateNodes_TransformWarning(t *testing.T) {
	nodes := []models.Node{
		{ID: "t1", Type: models.NodeTypeTransform, Name: "Transform", Config: map[string]interface{}{}},
	}
	results := ValidateNodes(nodes)
	if len(results) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(results))
	}
	if len(results[0].Warnings) == 0 {
		t.Error("expected warning for empty transform rules")
	}
	if len(results[0].Errors) != 0 {
		t.Error("transform with no rules should be warning, not error")
	}
}

func TestValidateNodes_SQLGenerateMissingTable(t *testing.T) {
	nodes := []models.Node{
		{ID: "g1", Type: models.NodeTypeSQLGenerate, Name: "SQL", Config: map[string]interface{}{}},
	}
	results := ValidateNodes(nodes)
	if len(results) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(results))
	}
	if len(results[0].Errors) == 0 {
		t.Error("expected error for missing table in sql_generate")
	}
}

func TestValidateNodes_MultipleIssues(t *testing.T) {
	nodes := []models.Node{
		{ID: "s1", Type: models.NodeTypeSourceFile, Name: "Good", Config: map[string]interface{}{"path": "/ok.csv"}},
		{ID: "s2", Type: models.NodeTypeSourceAPI, Name: "Bad API", Config: map[string]interface{}{}},
		{ID: "c1", Type: models.NodeTypeCode, Name: "Bad Code", Config: map[string]interface{}{}},
	}
	results := ValidateNodes(nodes)
	if len(results) != 2 {
		t.Errorf("expected 2 nodes with issues, got %d", len(results))
	}
}
