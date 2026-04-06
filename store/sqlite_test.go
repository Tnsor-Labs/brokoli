package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
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

func TestWithTx_Commit(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().Truncate(time.Millisecond)

	// Create pipeline via transaction
	err := s.WithTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO pipelines (id, name, description, nodes, edges, schedule, webhook_url, params, tags, sla_deadline, sla_timezone, depends_on, webhook_token, enabled, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			"tx-pipe-1", "TX Pipeline", "", "[]", "[]", "", "", "{}", "[]", "", "", "[]", "", 1,
			now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
		)
		return err
	})
	if err != nil {
		t.Fatalf("WithTx commit: %v", err)
	}

	// Verify the pipeline exists
	p, err := s.GetPipeline("tx-pipe-1")
	if err != nil {
		t.Fatalf("GetPipeline after tx commit: %v", err)
	}
	if p.Name != "TX Pipeline" {
		t.Errorf("Name = %q, want %q", p.Name, "TX Pipeline")
	}
}

func TestWithTx_Rollback(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().Truncate(time.Millisecond)

	// Transaction that returns an error should rollback
	err := s.WithTx(func(tx *sql.Tx) error {
		tx.Exec(
			`INSERT INTO pipelines (id, name, description, nodes, edges, schedule, webhook_url, params, tags, sla_deadline, sla_timezone, depends_on, webhook_token, enabled, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			"tx-pipe-rollback", "Rollback Pipeline", "", "[]", "[]", "", "", "{}", "[]", "", "", "[]", "", 1,
			now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
		)
		return fmt.Errorf("intentional error")
	})
	if err == nil {
		t.Fatal("WithTx should have returned an error")
	}

	// Verify the pipeline does NOT exist
	_, err = s.GetPipeline("tx-pipe-rollback")
	if err == nil {
		t.Fatal("Pipeline should not exist after rollback")
	}
}

func TestDLQ_AddAndList(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().Truncate(time.Millisecond)

	// Create a pipeline (FK constraint)
	s.CreatePipeline(&models.Pipeline{
		ID: "pipe-dlq", Name: "DLQ Test", Nodes: []models.Node{}, Edges: []models.Edge{},
		CreatedAt: now, UpdatedAt: now,
	})

	// Add DLQ entry
	err := s.AddToDLQ("pipe-dlq", "run-1", "node-1", "Load CSV", "connection refused", `{"key":"value"}`)
	if err != nil {
		t.Fatalf("AddToDLQ: %v", err)
	}

	// List
	entries, err := s.ListDLQ("pipe-dlq", false, 10)
	if err != nil {
		t.Fatalf("ListDLQ: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}

	e := entries[0]
	if e.PipelineID != "pipe-dlq" {
		t.Errorf("PipelineID = %q, want %q", e.PipelineID, "pipe-dlq")
	}
	if e.RunID != "run-1" {
		t.Errorf("RunID = %q, want %q", e.RunID, "run-1")
	}
	if e.NodeID != "node-1" {
		t.Errorf("NodeID = %q, want %q", e.NodeID, "node-1")
	}
	if e.NodeName != "Load CSV" {
		t.Errorf("NodeName = %q, want %q", e.NodeName, "Load CSV")
	}
	if e.Error != "connection refused" {
		t.Errorf("Error = %q, want %q", e.Error, "connection refused")
	}
	if e.Payload != `{"key":"value"}` {
		t.Errorf("Payload = %q, want %q", e.Payload, `{"key":"value"}`)
	}
	if e.Resolved {
		t.Error("Resolved should be false")
	}
}

func TestDLQ_Resolve(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().Truncate(time.Millisecond)

	s.CreatePipeline(&models.Pipeline{
		ID: "pipe-dlq-r", Name: "DLQ Resolve", Nodes: []models.Node{}, Edges: []models.Edge{},
		CreatedAt: now, UpdatedAt: now,
	})

	s.AddToDLQ("pipe-dlq-r", "run-1", "", "", "timeout", "")

	entries, _ := s.ListDLQ("pipe-dlq-r", false, 10)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	// Resolve it
	if err := s.ResolveDLQ(entries[0].ID); err != nil {
		t.Fatalf("ResolveDLQ: %v", err)
	}

	// List without resolved — should be empty
	unresolved, _ := s.ListDLQ("pipe-dlq-r", false, 10)
	if len(unresolved) != 0 {
		t.Errorf("unresolved entries = %d, want 0", len(unresolved))
	}

	// List with resolved — should have 1
	all, _ := s.ListDLQ("pipe-dlq-r", true, 10)
	if len(all) != 1 {
		t.Errorf("all entries = %d, want 1", len(all))
	}
	if !all[0].Resolved {
		t.Error("entry should be resolved")
	}
	if all[0].ResolvedAt == "" {
		t.Error("ResolvedAt should not be empty")
	}
}

func TestDLQ_ListLimit(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().Truncate(time.Millisecond)

	s.CreatePipeline(&models.Pipeline{
		ID: "pipe-dlq-l", Name: "DLQ Limit", Nodes: []models.Node{}, Edges: []models.Edge{},
		CreatedAt: now, UpdatedAt: now,
	})

	// Add 5 entries
	for i := 0; i < 5; i++ {
		s.AddToDLQ("pipe-dlq-l", fmt.Sprintf("run-%d", i), "", "", fmt.Sprintf("error %d", i), "")
	}

	// List with limit 3
	entries, err := s.ListDLQ("pipe-dlq-l", false, 3)
	if err != nil {
		t.Fatalf("ListDLQ: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("len(entries) = %d, want 3", len(entries))
	}
}

func TestGetPipelineByPipelineID(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().Truncate(time.Millisecond)

	p := &models.Pipeline{
		ID:         "pipe-pid-1",
		Name:       "Orders ETL",
		PipelineID: "orders-etl",
		Source:     models.PipelineSourceGit,
		Nodes:      []models.Node{},
		Edges:      []models.Edge{},
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := s.CreatePipeline(p); err != nil {
		t.Fatalf("CreatePipeline: %v", err)
	}

	got, err := s.GetPipelineByPipelineID("orders-etl")
	if err != nil {
		t.Fatalf("GetPipelineByPipelineID: %v", err)
	}
	if got.ID != "pipe-pid-1" {
		t.Errorf("ID = %q, want %q", got.ID, "pipe-pid-1")
	}
	if got.Name != "Orders ETL" {
		t.Errorf("Name = %q, want %q", got.Name, "Orders ETL")
	}
	if got.PipelineID != "orders-etl" {
		t.Errorf("PipelineID = %q, want %q", got.PipelineID, "orders-etl")
	}
	if got.Source != models.PipelineSourceGit {
		t.Errorf("Source = %q, want %q", got.Source, models.PipelineSourceGit)
	}
}

func TestGetPipelineByPipelineID_NotFound(t *testing.T) {
	s := newTestStore(t)

	_, err := s.GetPipelineByPipelineID("nonexistent-pipeline")
	if err == nil {
		t.Fatal("expected error for non-existent pipeline_id, got nil")
	}
}

func TestPipeline_SourceField(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().Truncate(time.Millisecond)

	// Create with source="git"
	p := &models.Pipeline{
		ID:         "pipe-src-1",
		Name:       "Git Pipeline",
		PipelineID: "git-pipeline",
		Source:     models.PipelineSourceGit,
		Nodes:      []models.Node{},
		Edges:      []models.Edge{},
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := s.CreatePipeline(p); err != nil {
		t.Fatalf("CreatePipeline: %v", err)
	}

	got, err := s.GetPipeline("pipe-src-1")
	if err != nil {
		t.Fatalf("GetPipeline: %v", err)
	}
	if got.Source != models.PipelineSourceGit {
		t.Errorf("Source = %q, want %q", got.Source, models.PipelineSourceGit)
	}

	// Create with source="ui"
	p2 := &models.Pipeline{
		ID:         "pipe-src-2",
		Name:       "UI Pipeline",
		PipelineID: "ui-pipeline",
		Source:     models.PipelineSourceUI,
		Nodes:      []models.Node{},
		Edges:      []models.Edge{},
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := s.CreatePipeline(p2); err != nil {
		t.Fatalf("CreatePipeline: %v", err)
	}

	got2, err := s.GetPipeline("pipe-src-2")
	if err != nil {
		t.Fatalf("GetPipeline: %v", err)
	}
	if got2.Source != models.PipelineSourceUI {
		t.Errorf("Source = %q, want %q", got2.Source, models.PipelineSourceUI)
	}
}

// Enterprise store tests (Organization CRUD, OrgMember CRUD) moved to brokoli-enterprise.

func TestOrganization_Validate(t *testing.T) {
	tests := []struct {
		name string
		org  models.Organization
		ok   bool
	}{
		{"valid", models.Organization{Name: "Acme", Slug: "acme"}, true},
		{"empty name", models.Organization{Name: "", Slug: "acme"}, false},
		{"empty slug", models.Organization{Name: "Acme", Slug: ""}, false},
		{"uppercase slug", models.Organization{Name: "Acme", Slug: "Acme"}, false},
		{"slug with spaces", models.Organization{Name: "Acme", Slug: "my org"}, false},
		{"short slug", models.Organization{Name: "A", Slug: "a"}, false},
		{"valid slug with hyphen", models.Organization{Name: "Acme Corp", Slug: "acme-corp"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.org.Validate()
			if tt.ok && err != nil {
				t.Errorf("expected valid, got error: %v", err)
			}
			if !tt.ok && err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

// Suppress unused import warning
var _ = os.TempDir
