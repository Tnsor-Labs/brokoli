package engine

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

func newDepTestStore(t *testing.T) store.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "deps.db")
	s, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func newPipe(t *testing.T, s store.Store, id, name string, deps []models.DependencyRule) *models.Pipeline {
	t.Helper()
	now := time.Now().UTC()
	p := &models.Pipeline{
		ID:              id,
		Name:            name,
		Nodes:           []models.Node{},
		Edges:           []models.Edge{},
		DependencyRules: deps,
		Enabled:         true,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := s.CreatePipeline(p); err != nil {
		t.Fatalf("CreatePipeline %s: %v", id, err)
	}
	return p
}

func putRun(t *testing.T, s store.Store, pipelineID string, status models.RunStatus, finishedAgo time.Duration) {
	t.Helper()
	finished := time.Now().UTC().Add(-finishedAgo)
	run := &models.Run{
		ID:         "run-" + pipelineID + "-" + string(status) + "-" + finished.Format("150405.000000000"),
		PipelineID: pipelineID,
		Status:     status,
		StartedAt:  &finished,
		FinishedAt: &finished,
	}
	if err := s.CreateRun(run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
}

func TestCheckDependencies_Satisfied(t *testing.T) {
	s := newDepTestStore(t)
	newPipe(t, s, "up", "Upstream", nil)
	putRun(t, s, "up", models.RunStatusSuccess, 5*time.Minute)
	down := newPipe(t, s, "down", "Downstream", []models.DependencyRule{
		{PipelineID: "up", State: models.DepStateSucceeded, Mode: models.DepModeGate},
	})
	ok, _, reason := CheckDependencies(s, down, time.Now().UTC())
	if !ok {
		t.Errorf("expected satisfied, got reason=%q", reason)
	}
}

func TestCheckDependencies_NoRunsBlocks(t *testing.T) {
	s := newDepTestStore(t)
	newPipe(t, s, "up", "Upstream", nil)
	down := newPipe(t, s, "down", "Downstream", []models.DependencyRule{
		{PipelineID: "up"},
	})
	ok, _, reason := CheckDependencies(s, down, time.Now().UTC())
	if ok {
		t.Errorf("expected blocked, got satisfied")
	}
	if reason == "" {
		t.Error("expected reason")
	}
}

func TestCheckDependencies_FailedUpstreamBlocks(t *testing.T) {
	s := newDepTestStore(t)
	newPipe(t, s, "up", "Upstream", nil)
	putRun(t, s, "up", models.RunStatusFailed, 5*time.Minute)
	down := newPipe(t, s, "down", "Downstream", []models.DependencyRule{
		{PipelineID: "up", State: models.DepStateSucceeded},
	})
	ok, _, _ := CheckDependencies(s, down, time.Now().UTC())
	if ok {
		t.Error("expected blocked when upstream failed")
	}
}

func TestCheckDependencies_CompletedAcceptsFailed(t *testing.T) {
	s := newDepTestStore(t)
	newPipe(t, s, "up", "Upstream", nil)
	putRun(t, s, "up", models.RunStatusFailed, 5*time.Minute)
	down := newPipe(t, s, "down", "Downstream", []models.DependencyRule{
		{PipelineID: "up", State: models.DepStateCompleted},
	})
	ok, _, _ := CheckDependencies(s, down, time.Now().UTC())
	if !ok {
		t.Error("expected satisfied with state=completed and failed run")
	}
}

func TestCheckDependencies_FreshnessWindow(t *testing.T) {
	s := newDepTestStore(t)
	newPipe(t, s, "up", "Upstream", nil)
	putRun(t, s, "up", models.RunStatusSuccess, 2*time.Hour)
	down := newPipe(t, s, "down", "Downstream", []models.DependencyRule{
		{PipelineID: "up", State: models.DepStateSucceeded, WithinSec: 3600}, // 1h window
	})
	ok, _, reason := CheckDependencies(s, down, time.Now().UTC())
	if ok {
		t.Errorf("expected stale block, got reason=%q", reason)
	}
}

func TestCheckDependencies_TriggerModeDoesNotBlock(t *testing.T) {
	s := newDepTestStore(t)
	newPipe(t, s, "up", "Upstream", nil)
	// Trigger mode: not satisfied should NOT block a direct run (gate-only blocks)
	down := newPipe(t, s, "down", "Downstream", []models.DependencyRule{
		{PipelineID: "up", Mode: models.DepModeTrigger},
	})
	ok, _, _ := CheckDependencies(s, down, time.Now().UTC())
	if !ok {
		t.Error("trigger-mode unsatisfied dep should NOT block manual runs")
	}
}

func TestCheckDependencies_MissingUpstreamBlocks(t *testing.T) {
	s := newDepTestStore(t)
	down := newPipe(t, s, "down", "Downstream", []models.DependencyRule{
		{PipelineID: "nonexistent"},
	})
	ok, _, _ := CheckDependencies(s, down, time.Now().UTC())
	if ok {
		t.Error("expected blocked when upstream doesn't exist")
	}
}

func TestDetectCycle_Direct(t *testing.T) {
	s := newDepTestStore(t)
	newPipe(t, s, "a", "A", nil)
	newPipe(t, s, "b", "B", []models.DependencyRule{{PipelineID: "a"}})
	// Now update A to depend on B — creates a->b->a cycle
	a, _ := s.GetPipeline("a")
	a.DependencyRules = []models.DependencyRule{{PipelineID: "b"}}
	if err := DetectDependencyCycle(s, a); err == nil {
		t.Error("expected cycle detection")
	}
}

func TestDetectCycle_Indirect(t *testing.T) {
	s := newDepTestStore(t)
	newPipe(t, s, "a", "A", nil)
	newPipe(t, s, "b", "B", []models.DependencyRule{{PipelineID: "a"}})
	newPipe(t, s, "c", "C", []models.DependencyRule{{PipelineID: "b"}})
	a, _ := s.GetPipeline("a")
	a.DependencyRules = []models.DependencyRule{{PipelineID: "c"}}
	if err := DetectDependencyCycle(s, a); err == nil {
		t.Error("expected indirect cycle detection a->c->b->a")
	}
}

func TestDetectCycle_NoCycle(t *testing.T) {
	s := newDepTestStore(t)
	newPipe(t, s, "a", "A", nil)
	newPipe(t, s, "b", "B", []models.DependencyRule{{PipelineID: "a"}})
	newPipe(t, s, "c", "C", []models.DependencyRule{{PipelineID: "b"}})
	c, _ := s.GetPipeline("c")
	if err := DetectDependencyCycle(s, c); err != nil {
		t.Errorf("unexpected cycle: %v", err)
	}
}

func TestPipelinesDependingOn(t *testing.T) {
	s := newDepTestStore(t)
	newPipe(t, s, "up", "Upstream", nil)
	newPipe(t, s, "d1", "D1", []models.DependencyRule{{PipelineID: "up"}})
	newPipe(t, s, "d2", "D2", []models.DependencyRule{{PipelineID: "up", Mode: models.DepModeTrigger}})
	newPipe(t, s, "other", "Other", nil)

	deps, err := s.PipelinesDependingOn("up")
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 2 {
		t.Errorf("expected 2 dependents, got %d", len(deps))
	}
}
