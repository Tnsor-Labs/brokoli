package engine

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

func newEngineTestStore(t *testing.T) store.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "engine.db")
	s, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestRunPipeline_BlockedByUnsatisfiedDep(t *testing.T) {
	s := newEngineTestStore(t)
	now := time.Now().UTC()
	srcNode := models.Node{
		ID: "src", Type: models.NodeTypeSourceFile, Name: "csv",
		Config: map[string]interface{}{"path": "/tmp/nonexistent.csv"},
	}
	up := &models.Pipeline{
		ID: "up", Name: "Upstream",
		Nodes:     []models.Node{srcNode},
		Edges:     []models.Edge{},
		CreatedAt: now, UpdatedAt: now, Enabled: true,
	}
	if err := s.CreatePipeline(up); err != nil {
		t.Fatalf("CreatePipeline: %v", err)
	}
	down := &models.Pipeline{
		ID: "down", Name: "Downstream",
		Nodes:           []models.Node{srcNode},
		Edges:           []models.Edge{},
		DependencyRules: []models.DependencyRule{{PipelineID: "up", State: models.DepStateSucceeded, Mode: models.DepModeGate}},
		CreatedAt:       now, UpdatedAt: now, Enabled: true,
	}
	if err := s.CreatePipeline(down); err != nil {
		t.Fatalf("CreatePipeline: %v", err)
	}

	e := NewEngine(s)
	run, err := e.RunPipeline("down")
	if err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}
	if run.Status != models.RunStatusBlocked {
		t.Errorf("expected blocked status, got %q", run.Status)
	}
	if run.Error == "" {
		t.Error("expected error reason")
	}

	// Verify it was persisted.
	stored, err := s.GetRun(run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if stored.Status != models.RunStatusBlocked {
		t.Errorf("persisted status = %q, want blocked", stored.Status)
	}
}

func TestRunPipeline_SatisfiedDepAllowsRun(t *testing.T) {
	s := newEngineTestStore(t)
	now := time.Now().UTC()
	up := &models.Pipeline{
		ID: "up", Name: "Upstream",
		Nodes: []models.Node{}, Edges: []models.Edge{},
		CreatedAt: now, UpdatedAt: now, Enabled: true,
	}
	s.CreatePipeline(up)
	// Simulate a completed upstream run.
	finished := time.Now().UTC()
	s.CreateRun(&models.Run{
		ID: "up-run", PipelineID: "up", Status: models.RunStatusSuccess,
		StartedAt: &finished, FinishedAt: &finished,
	})
	down := &models.Pipeline{
		ID: "down", Name: "Downstream",
		Nodes:           []models.Node{}, // empty pipeline; just need to pass dep check
		Edges:           []models.Edge{},
		DependencyRules: []models.DependencyRule{{PipelineID: "up"}},
		CreatedAt:       now, UpdatedAt: now, Enabled: true,
	}
	s.CreatePipeline(down)

	// Dep check should pass — we don't actually care about the run body succeeding here.
	ok, _, reason := CheckDependencies(s, down, time.Now().UTC())
	if !ok {
		t.Errorf("expected dep satisfied, got %q", reason)
	}
}
