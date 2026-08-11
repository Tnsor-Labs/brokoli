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

func TestPhysicalInstancesRoundTripAndCascade(t *testing.T) {
	s := planStore(t)
	old := time.Now().AddDate(0, 0, -40).UTC()
	seedRunForPlan(t, s, "inst-run", old)

	start := time.Now().UTC()
	insts := []models.PhysicalInstance{
		{LogicalNodeID: "src", Kind: models.WorkUnitSingle, InstanceKey: "src", Status: models.RunStatusSuccess, RowCount: 5, StartedAt: &start, DurationMs: 12, Attempt: 0},
		{LogicalNodeID: "fan", Kind: models.WorkUnitExpansion, InstanceKey: "fan[0]", Index: 0, Status: models.RunStatusSuccess, RowCount: 3},
	}
	if err := s.SavePhysicalInstances("inst-run", insts); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListPhysicalInstances("inst-run")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d instances, want 2", len(got))
	}
	// Upsert: re-saving with a changed status refreshes, not duplicates.
	insts[0].Status = models.RunStatusFailed
	if err := s.SavePhysicalInstances("inst-run", insts); err != nil {
		t.Fatal(err)
	}
	got, _ = s.ListPhysicalInstances("inst-run")
	if len(got) != 2 {
		t.Fatalf("upsert duplicated rows: got %d", len(got))
	}
	// Cascade: purging the run removes its instances.
	if _, err := s.PurgeRunsOlderThan(30); err != nil {
		t.Fatal(err)
	}
	after, _ := s.ListPhysicalInstances("inst-run")
	if len(after) != 0 {
		t.Fatalf("instances survived run purge: %d", len(after))
	}
}

func TestSavePhysicalInstancesEmptyIsNoop(t *testing.T) {
	s := planStore(t)
	if err := s.SavePhysicalInstances("nope", nil); err != nil {
		t.Fatalf("empty save should be a no-op, got %v", err)
	}
}
