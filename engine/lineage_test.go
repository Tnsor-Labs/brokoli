package engine

import (
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
)

func TestLineage_WalksDAGEdges(t *testing.T) {
	p := models.Pipeline{
		ID:   "p1",
		Name: "Test Pipeline",
		Nodes: []models.Node{
			{ID: "s1", Type: models.NodeTypeSourceFile, Name: "Input", Config: map[string]interface{}{"path": "/data/in.csv"}},
			{ID: "t1", Type: models.NodeTypeTransform, Name: "Transform", Config: map[string]interface{}{}},
			{ID: "o1", Type: models.NodeTypeSinkFile, Name: "Output", Config: map[string]interface{}{"path": "/data/out.csv"}},
		},
		Edges: []models.Edge{
			{From: "s1", To: "t1"},
			{From: "t1", To: "o1"},
		},
	}

	graph := BuildLineageGraph([]models.Pipeline{p})

	// Should have 3 lineage nodes: 2 file assets + 1 processing
	if len(graph.Nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d: %+v", len(graph.Nodes), graph.Nodes)
	}

	// Should have 2 edges (following actual DAG, not cartesian)
	if len(graph.Edges) != 2 {
		t.Fatalf("expected 2 edges, got %d: %+v", len(graph.Edges), graph.Edges)
	}
}

func TestLineage_NoCartesianProduct(t *testing.T) {
	// Pipeline with 2 sources and 2 sinks, but specific connections
	p := models.Pipeline{
		ID:   "p1",
		Name: "Multi",
		Nodes: []models.Node{
			{ID: "s1", Type: models.NodeTypeSourceFile, Name: "A", Config: map[string]interface{}{"path": "/a.csv"}},
			{ID: "s2", Type: models.NodeTypeSourceFile, Name: "B", Config: map[string]interface{}{"path": "/b.csv"}},
			{ID: "o1", Type: models.NodeTypeSinkFile, Name: "X", Config: map[string]interface{}{"path": "/x.csv"}},
			{ID: "o2", Type: models.NodeTypeSinkFile, Name: "Y", Config: map[string]interface{}{"path": "/y.csv"}},
		},
		Edges: []models.Edge{
			{From: "s1", To: "o1"}, // A -> X
			{From: "s2", To: "o2"}, // B -> Y
		},
	}

	graph := BuildLineageGraph([]models.Pipeline{p})

	// Old code would create 4 edges (2x2), new code should create exactly 2
	if len(graph.Edges) != 2 {
		t.Errorf("expected 2 edges (no cartesian product), got %d", len(graph.Edges))
	}
}

func TestLineage_ProcessingNodes(t *testing.T) {
	p := models.Pipeline{
		ID:   "p1",
		Name: "ETL",
		Nodes: []models.Node{
			{ID: "s1", Type: models.NodeTypeSourceFile, Name: "Input", Config: map[string]interface{}{"path": "/in.csv"}},
			{ID: "c1", Type: models.NodeTypeCode, Name: "Process", Config: map[string]interface{}{"script": "pass"}},
			{ID: "q1", Type: models.NodeTypeQualityCheck, Name: "Check", Config: map[string]interface{}{}},
			{ID: "o1", Type: models.NodeTypeSinkFile, Name: "Output", Config: map[string]interface{}{"path": "/out.csv"}},
		},
		Edges: []models.Edge{
			{From: "s1", To: "c1"},
			{From: "c1", To: "q1"},
			{From: "q1", To: "o1"},
		},
	}

	graph := BuildLineageGraph([]models.Pipeline{p})

	// 2 file assets + 2 processing nodes = 4
	if len(graph.Nodes) != 4 {
		t.Fatalf("expected 4 nodes, got %d", len(graph.Nodes))
	}

	// Count processing nodes
	procCount := 0
	for _, n := range graph.Nodes {
		if n.Type == "processing" {
			procCount++
			if n.PipelineID != "p1" {
				t.Errorf("processing node missing pipeline_id")
			}
		}
	}
	if procCount != 2 {
		t.Errorf("expected 2 processing nodes, got %d", procCount)
	}

	// 3 edges following the chain
	if len(graph.Edges) != 3 {
		t.Errorf("expected 3 edges, got %d", len(graph.Edges))
	}
}

func TestLineage_CrossPipelineViaSharedAsset(t *testing.T) {
	p1 := models.Pipeline{
		ID:   "p1",
		Name: "Writer",
		Nodes: []models.Node{
			{ID: "s1", Type: models.NodeTypeSourceFile, Name: "In", Config: map[string]interface{}{"path": "/raw.csv"}},
			{ID: "o1", Type: models.NodeTypeSinkFile, Name: "Out", Config: map[string]interface{}{"path": "/shared.csv"}},
		},
		Edges: []models.Edge{{From: "s1", To: "o1"}},
	}
	p2 := models.Pipeline{
		ID:   "p2",
		Name: "Reader",
		Nodes: []models.Node{
			{ID: "s1", Type: models.NodeTypeSourceFile, Name: "In", Config: map[string]interface{}{"path": "/shared.csv"}},
			{ID: "o1", Type: models.NodeTypeSinkFile, Name: "Out", Config: map[string]interface{}{"path": "/final.csv"}},
		},
		Edges: []models.Edge{{From: "s1", To: "o1"}},
	}

	graph := BuildLineageGraph([]models.Pipeline{p1, p2})

	// /shared.csv should be a single node used by both pipelines
	sharedCount := 0
	for _, n := range graph.Nodes {
		if n.Name == "shared.csv" {
			sharedCount++
		}
	}
	if sharedCount != 1 {
		t.Errorf("expected 1 shared asset node, got %d", sharedCount)
	}

	// 3 unique file assets: raw.csv, shared.csv, final.csv
	assetCount := 0
	for _, n := range graph.Nodes {
		if n.Type == "file" {
			assetCount++
		}
	}
	if assetCount != 3 {
		t.Errorf("expected 3 file assets, got %d", assetCount)
	}
}

func TestLineage_VariableResolution(t *testing.T) {
	// Set env var for testing
	t.Setenv("TEST_PATH", "/resolved/data.csv")

	p := models.Pipeline{
		ID:   "p1",
		Name: "Vars",
		Nodes: []models.Node{
			{ID: "s1", Type: models.NodeTypeSourceFile, Name: "In", Config: map[string]interface{}{"path": "${env.TEST_PATH}"}},
			{ID: "o1", Type: models.NodeTypeSinkFile, Name: "Out", Config: map[string]interface{}{"path": "/out.csv"}},
		},
		Edges: []models.Edge{{From: "s1", To: "o1"}},
	}

	graph := BuildLineageGraph([]models.Pipeline{p})

	// The source asset should have the resolved path
	found := false
	for _, n := range graph.Nodes {
		if n.ID == "file:/resolved/data.csv" {
			found = true
		}
	}
	if !found {
		t.Error("expected resolved variable in asset ID")
		for _, n := range graph.Nodes {
			t.Logf("  node: %+v", n)
		}
	}
}

func TestLineage_SQLGenerateCreatesTableAsset(t *testing.T) {
	p := models.Pipeline{
		ID:   "p1",
		Name: "SQL",
		Nodes: []models.Node{
			{ID: "s1", Type: models.NodeTypeSourceFile, Name: "In", Config: map[string]interface{}{"path": "/in.csv"}},
			{ID: "g1", Type: models.NodeTypeSQLGenerate, Name: "Gen SQL", Config: map[string]interface{}{"table": "users", "dialect": "postgres"}},
		},
		Edges: []models.Edge{{From: "s1", To: "g1"}},
	}

	graph := BuildLineageGraph([]models.Pipeline{p})

	// Should have a table:users asset
	found := false
	for _, n := range graph.Nodes {
		if n.ID == "table:users" && n.Type == "table" {
			found = true
		}
	}
	if !found {
		t.Error("expected table:users asset from sql_generate node")
	}
}

func TestLineage_SinkDBUsesUpstreamTableName(t *testing.T) {
	p := models.Pipeline{
		ID:   "p1",
		Name: "DB Pipeline",
		Nodes: []models.Node{
			{ID: "s1", Type: models.NodeTypeSourceFile, Name: "In", Config: map[string]interface{}{"path": "/in.csv"}},
			{ID: "g1", Type: models.NodeTypeSQLGenerate, Name: "Gen", Config: map[string]interface{}{"table": "orders"}},
			{ID: "d1", Type: models.NodeTypeSinkDB, Name: "Write DB", Config: map[string]interface{}{"uri": "postgres://localhost/mydb"}},
		},
		Edges: []models.Edge{
			{From: "s1", To: "g1"},
			{From: "g1", To: "d1"},
		},
	}

	graph := BuildLineageGraph([]models.Pipeline{p})

	// sink_db should resolve to table:orders (from upstream sql_generate)
	found := false
	for _, n := range graph.Nodes {
		if n.ID == "table:orders" {
			found = true
		}
	}
	if !found {
		t.Error("expected sink_db to resolve to table:orders via upstream sql_generate")
		for _, n := range graph.Nodes {
			t.Logf("  node: %+v", n)
		}
	}
}

func TestLineage_EdgeDeduplication(t *testing.T) {
	// Same pipeline processed twice shouldn't create duplicate edges
	p := models.Pipeline{
		ID:   "p1",
		Name: "Test",
		Nodes: []models.Node{
			{ID: "s1", Type: models.NodeTypeSourceFile, Name: "In", Config: map[string]interface{}{"path": "/in.csv"}},
			{ID: "o1", Type: models.NodeTypeSinkFile, Name: "Out", Config: map[string]interface{}{"path": "/out.csv"}},
		},
		Edges: []models.Edge{{From: "s1", To: "o1"}},
	}

	graph := BuildLineageGraph([]models.Pipeline{p, p})

	if len(graph.Edges) != 1 {
		t.Errorf("expected 1 deduplicated edge, got %d", len(graph.Edges))
	}
}

func TestExtractTableFromQuery(t *testing.T) {
	tests := []struct {
		query string
		want  string
	}{
		{"SELECT * FROM users", "users"},
		{"select * from orders WHERE id > 5", "orders"},
		{"SELECT a.* FROM schema.table AS a", "schema.table"},
		{"", "unknown_table"},
	}

	for _, tc := range tests {
		got := extractTableFromQuery(tc.query)
		if got != tc.want {
			t.Errorf("extractTableFromQuery(%q) = %q, want %q", tc.query, got, tc.want)
		}
	}
}

func TestExtractDBName(t *testing.T) {
	tests := []struct {
		uri  string
		want string
	}{
		{"postgres://user:pass@host:5432/mydb", "mydb"},
		{"postgres://host/testdb?sslmode=disable", "testdb"},
		{"", "unknown_db"},
	}

	for _, tc := range tests {
		got := extractDBName(tc.uri)
		if got != tc.want {
			t.Errorf("extractDBName(%q) = %q, want %q", tc.uri, got, tc.want)
		}
	}
}
