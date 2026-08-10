package engine

// A real run must persist its physical plan (ADR-015, #90 M2), and the
// persisted plan must match what the planner produces for that pipeline
// — the on-demand planner is the oracle.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

func TestRunPersistsPhysicalPlanMatchingPlanner(t *testing.T) {
	dir := t.TempDir()
	s, err := store.NewSQLiteStore(filepath.Join(dir, "plan-e2e.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	csv := filepath.Join(dir, "in.csv")
	if err := os.WriteFile(csv, []byte("id\n1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out.csv")
	pipeline := &models.Pipeline{
		ID: "plan-e2e", Name: "plan e2e",
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceFile, Name: "Src", Config: map[string]interface{}{"path": csv, "format": "csv"}},
			{ID: "sink", Type: models.NodeTypeSinkFile, Name: "Sink", Config: map[string]interface{}{"path": out, "format": "csv"}},
		},
		Edges:     []models.Edge{{From: "src", To: "sink"}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}

	run, err := NewEngine(s).RunPipeline(pipeline.ID)
	if err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}

	stored, err := s.GetRunPlan(run.ID)
	if err != nil {
		t.Fatalf("plan not persisted for run: %v", err)
	}
	// The stored snapshot equals a fresh plan of the same pipeline.
	want, err := PlanPipeline(pipeline)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, _ := json.Marshal(want)
	if stored != string(wantJSON) {
		t.Fatalf("persisted plan != planner output:\n stored: %s\n want:   %s", stored, wantJSON)
	}

	var plan models.PhysicalPlan
	if err := json.Unmarshal([]byte(stored), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.StaticInstanceCount != 2 || len(plan.Stages) != 2 {
		t.Fatalf("unexpected persisted plan shape: static=%d stages=%d", plan.StaticInstanceCount, len(plan.Stages))
	}
}
