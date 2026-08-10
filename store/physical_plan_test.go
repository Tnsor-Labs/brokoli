package store

// Round-trip and retention for the physical-plan snapshot (ADR-015,
// #90 M2). Retention is by ON DELETE CASCADE with the run, so purging a
// run must take its plan too.

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

func planStore(t *testing.T) *SQLiteStore {
	t.Helper()
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "plan.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func seedRunForPlan(t *testing.T, s *SQLiteStore, runID string, startedAt time.Time) {
	t.Helper()
	if err := s.CreatePipeline(&models.Pipeline{
		ID: "pipe-" + runID, Name: "p",
		Nodes:     []models.Node{{ID: "n", Type: models.NodeTypeSourceFile, Name: "n"}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRun(&models.Run{
		ID: runID, PipelineID: "pipe-" + runID, Status: models.RunStatusSuccess, StartedAt: &startedAt,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRunPlanRoundTrip(t *testing.T) {
	s := planStore(t)
	now := time.Now().UTC()
	seedRunForPlan(t, s, "run-1", now)

	if err := s.SaveRunPlan("run-1", `{"stages":[]}`); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRunPlan("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if got != `{"stages":[]}` {
		t.Fatalf("plan round-trip mismatch: %q", got)
	}

	// Idempotent re-save (a resumed run reuses the ID).
	if err := s.SaveRunPlan("run-1", `{"stages":[{"index":0}]}`); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetRunPlan("run-1")
	if got != `{"stages":[{"index":0}]}` {
		t.Fatalf("plan not overwritten: %q", got)
	}
}

func TestRunPlanMissingIsError(t *testing.T) {
	s := planStore(t)
	if _, err := s.GetRunPlan("ghost"); err == nil {
		t.Fatal("expected error for missing plan")
	}
}

func TestRunPlanCascadesWithPurge(t *testing.T) {
	s := planStore(t)
	old := time.Now().AddDate(0, 0, -40).UTC()
	seedRunForPlan(t, s, "old-run", old)
	if err := s.SaveRunPlan("old-run", `{"stages":[]}`); err != nil {
		t.Fatal(err)
	}
	// Purge runs older than 30 days: the plan must go with the run.
	if _, err := s.PurgeRunsOlderThan(30); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetRunPlan("old-run"); err == nil {
		t.Fatal("plan survived its run's purge — ON DELETE CASCADE not effective")
	}
}
