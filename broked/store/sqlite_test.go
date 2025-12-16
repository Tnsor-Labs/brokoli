package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hc12r/broked/models"
)

func newTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestPipelineCRUD(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().Truncate(time.Millisecond)

	p := &models.Pipeline{
		ID:          "pipe-1",
		Name:        "Test Pipeline",
		Description: "A test pipeline",
		Nodes: []models.Node{
			{ID: "n1", Type: models.NodeTypeSourceFile, Name: "Load CSV", Config: map[string]interface{}{"path": "/data/test.csv"}, Position: models.Position{X: 100, Y: 200}},
			{ID: "n2", Type: models.NodeTypeSQLGenerate, Name: "Generate SQL", Config: map[string]interface{}{"dialect": "postgres"}, Position: models.Position{X: 300, Y: 200}},
		},
		Edges:     []models.Edge{{From: "n1", To: "n2"}},
		Schedule:  "0 2 * * *",
		Enabled:   true,
		CreatedAt: now,
		UpdatedAt: now,
	}

	// Create
	if err := s.CreatePipeline(p); err != nil {
		t.Fatalf("CreatePipeline: %v", err)
	}

	// Get
	got, err := s.GetPipeline("pipe-1")
	if err != nil {
		t.Fatalf("GetPipeline: %v", err)
	}
	if got.Name != "Test Pipeline" {
		t.Errorf("Name = %q, want %q", got.Name, "Test Pipeline")
	}
	if len(got.Nodes) != 2 {
		t.Errorf("len(Nodes) = %d, want 2", len(got.Nodes))
	}
	if len(got.Edges) != 1 {
		t.Errorf("len(Edges) = %d, want 1", len(got.Edges))
	}
	if got.Nodes[0].Type != models.NodeTypeSourceFile {
		t.Errorf("Node[0].Type = %q, want %q", got.Nodes[0].Type, models.NodeTypeSourceFile)
	}

	// List
	list, err := s.ListPipelines()
	if err != nil {
		t.Fatalf("ListPipelines: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("len(ListPipelines) = %d, want 1", len(list))
	}

	// Update
	p.Name = "Updated Pipeline"
	p.UpdatedAt = time.Now().Truncate(time.Millisecond)
	if err := s.UpdatePipeline(p); err != nil {
		t.Fatalf("UpdatePipeline: %v", err)
	}
	got, _ = s.GetPipeline("pipe-1")
	if got.Name != "Updated Pipeline" {
		t.Errorf("Name after update = %q, want %q", got.Name, "Updated Pipeline")
	}

	// Delete
	if err := s.DeletePipeline("pipe-1"); err != nil {
		t.Fatalf("DeletePipeline: %v", err)
	}
	list, _ = s.ListPipelines()
	if len(list) != 0 {
		t.Errorf("len after delete = %d, want 0", len(list))
	}
}

func TestRunLifecycle(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().Truncate(time.Millisecond)

	// Create a pipeline first (FK constraint)
	p := &models.Pipeline{
		ID: "pipe-1", Name: "P", Nodes: []models.Node{}, Edges: []models.Edge{},
		CreatedAt: now, UpdatedAt: now,
	}
	s.CreatePipeline(p)

	// Create run
	r := &models.Run{
		ID:         "run-1",
		PipelineID: "pipe-1",
		Status:     models.RunStatusPending,
	}
	if err := s.CreateRun(r); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	// Update to running
	startTime := time.Now().Truncate(time.Millisecond)
	r.Status = models.RunStatusRunning
	r.StartedAt = &startTime
	if err := s.UpdateRun(r); err != nil {
		t.Fatalf("UpdateRun: %v", err)
	}

	got, err := s.GetRun("run-1")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Status != models.RunStatusRunning {
		t.Errorf("Status = %q, want %q", got.Status, models.RunStatusRunning)
	}
	if got.StartedAt == nil {
		t.Error("StartedAt should not be nil")
	}

	// List runs
	runs, err := s.ListRunsByPipeline("pipe-1", 10)
	if err != nil {
		t.Fatalf("ListRunsByPipeline: %v", err)
	}
	if len(runs) != 1 {
		t.Errorf("len(runs) = %d, want 1", len(runs))
	}
}

func TestNodeRuns(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().Truncate(time.Millisecond)

	s.CreatePipeline(&models.Pipeline{
		ID: "pipe-1", Name: "P", Nodes: []models.Node{}, Edges: []models.Edge{},
		CreatedAt: now, UpdatedAt: now,
	})
	s.CreateRun(&models.Run{ID: "run-1", PipelineID: "pipe-1", Status: models.RunStatusRunning})

	nr := &models.NodeRun{
		ID:     "nr-1",
		RunID:  "run-1",
		NodeID: "n1",
		Status: models.RunStatusRunning,
	}
	if err := s.CreateNodeRun(nr); err != nil {
		t.Fatalf("CreateNodeRun: %v", err)
	}

	// Update to completed
	nr.Status = models.RunStatusSuccess
	nr.RowCount = 1500
	nr.DurationMs = 342
	if err := s.UpdateNodeRun(nr); err != nil {
		t.Fatalf("UpdateNodeRun: %v", err)
	}

	nodeRuns, err := s.ListNodeRunsByRun("run-1")
	if err != nil {
		t.Fatalf("ListNodeRunsByRun: %v", err)
	}
	if len(nodeRuns) != 1 {
		t.Fatalf("len(nodeRuns) = %d, want 1", len(nodeRuns))
	}
	if nodeRuns[0].RowCount != 1500 {
		t.Errorf("RowCount = %d, want 1500", nodeRuns[0].RowCount)
	}
}

func TestLogs(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().Truncate(time.Millisecond)

	s.CreatePipeline(&models.Pipeline{
		ID: "pipe-1", Name: "P", Nodes: []models.Node{}, Edges: []models.Edge{},
		CreatedAt: now, UpdatedAt: now,
	})
	s.CreateRun(&models.Run{ID: "run-1", PipelineID: "pipe-1", Status: models.RunStatusRunning})

	entries := []models.LogEntry{
		{RunID: "run-1", NodeID: "n1", Level: models.LogLevelInfo, Message: "Loading CSV", Timestamp: now},
		{RunID: "run-1", NodeID: "n1", Level: models.LogLevelInfo, Message: "Loaded 1500 rows", Timestamp: now.Add(time.Second)},
		{RunID: "run-1", NodeID: "n2", Level: models.LogLevelError, Message: "Type inference failed", Timestamp: now.Add(2 * time.Second)},
	}

	for _, e := range entries {
		if err := s.AppendLog(&e); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}

	logs, err := s.GetLogs("run-1")
	if err != nil {
		t.Fatalf("GetLogs: %v", err)
	}
	if len(logs) != 3 {
		t.Fatalf("len(logs) = %d, want 3", len(logs))
	}
	if logs[0].Message != "Loading CSV" {
		t.Errorf("logs[0].Message = %q, want %q", logs[0].Message, "Loading CSV")
	}
	if logs[2].Level != models.LogLevelError {
		t.Errorf("logs[2].Level = %q, want %q", logs[2].Level, models.LogLevelError)
	}
}

func TestCascadeDelete(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().Truncate(time.Millisecond)

	s.CreatePipeline(&models.Pipeline{
		ID: "pipe-1", Name: "P", Nodes: []models.Node{}, Edges: []models.Edge{},
		CreatedAt: now, UpdatedAt: now,
	})
	s.CreateRun(&models.Run{ID: "run-1", PipelineID: "pipe-1", Status: models.RunStatusSuccess})
	s.CreateNodeRun(&models.NodeRun{ID: "nr-1", RunID: "run-1", NodeID: "n1", Status: models.RunStatusSuccess})
	s.AppendLog(&models.LogEntry{RunID: "run-1", NodeID: "n1", Level: models.LogLevelInfo, Message: "done", Timestamp: now})

	// Deleting pipeline should cascade to runs, node_runs, and logs
	if err := s.DeletePipeline("pipe-1"); err != nil {
		t.Fatalf("DeletePipeline: %v", err)
	}

	runs, _ := s.ListRunsByPipeline("pipe-1", 10)
	if len(runs) != 0 {
		t.Errorf("runs after cascade delete = %d, want 0", len(runs))
	}

	logs, _ := s.GetLogs("run-1")
	if len(logs) != 0 {
		t.Errorf("logs after cascade delete = %d, want 0", len(logs))
	}
}

// Suppress unused import warning
var _ = os.TempDir
