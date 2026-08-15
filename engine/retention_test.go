package engine

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/store"
)

// TestPurgeExpiredRunsOnce covers the #214 sweep in one pass over a mixed
// population: an expired run goes away WITH its artifacts, a recent run
// and a pending run (no started_at — awaiting dispatch, possibly for a
// long time in a paused deployment) both survive untouched.
func TestPurgeExpiredRunsOnce(t *testing.T) {
	dir := t.TempDir()
	s, err := store.NewSQLiteStore(filepath.Join(dir, "retention.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	eng := drainEngineOnCleanup(t, NewEngine(s))
	artDir := filepath.Join(dir, "artifacts")
	eng.ArtifactStore = NewLocalDiskArtifactStore(artDir)

	seedCancelTestPipeline(t, s, "p-retention")

	old := time.Now().UTC().AddDate(0, 0, -10)
	recent := time.Now().UTC().Add(-time.Hour)
	for _, r := range []*models.Run{
		{ID: "run-expired", PipelineID: "p-retention", Status: models.RunStatusSuccess, StartedAt: &old, FinishedAt: &old},
		{ID: "run-recent", PipelineID: "p-retention", Status: models.RunStatusSuccess, StartedAt: &recent, FinishedAt: &recent},
		{ID: "run-still-pending", PipelineID: "p-retention", Status: models.RunStatusPending},
	} {
		if err := s.CreateRun(r); err != nil {
			t.Fatal(err)
		}
	}
	for _, runID := range []string{"run-expired", "run-recent"} {
		ds := &common.DataSet{Columns: []string{"id"}, Rows: []common.DataRow{{"id": 1}}}
		if err := eng.ArtifactStore.WriteArtifact(runID, "source", "", ds); err != nil {
			t.Fatal(err)
		}
	}

	purged, err := eng.purgeExpiredRunsOnce(7)
	if err != nil {
		t.Fatalf("purgeExpiredRunsOnce: %v", err)
	}
	if purged != 1 {
		t.Fatalf("purged = %d, want 1", purged)
	}

	if _, err := s.GetRun("run-expired"); err == nil {
		t.Fatal("expired run still exists")
	}
	if _, err := eng.ArtifactStore.ReadArtifact("run-expired", "source", ""); err == nil {
		t.Fatal("expired run's artifact still exists")
	}
	if _, err := s.GetRun("run-recent"); err != nil {
		t.Fatalf("recent run was purged: %v", err)
	}
	if _, err := eng.ArtifactStore.ReadArtifact("run-recent", "source", ""); err != nil {
		t.Fatalf("recent run's artifact was deleted: %v", err)
	}
	if _, err := s.GetRun("run-still-pending"); err != nil {
		t.Fatalf("pending run (no started_at) was purged: %v", err)
	}

	// Idempotent: an immediate second sweep finds nothing.
	purged, err = eng.purgeExpiredRunsOnce(7)
	if err != nil {
		t.Fatal(err)
	}
	if purged != 0 {
		t.Fatalf("second sweep purged %d, want 0", purged)
	}
}

// TestRetentionSweepDisabledByDefault pins the keep-forever default:
// days <= 0 must start nothing at all.
func TestRetentionSweepDisabledByDefault(t *testing.T) {
	eng, s := newExecCtxTestEngine(t)
	seedCancelTestPipeline(t, s, "p-retention-off")
	old := time.Now().UTC().AddDate(0, 0, -100)
	if err := s.CreateRun(&models.Run{ID: "run-ancient", PipelineID: "p-retention-off", Status: models.RunStatusSuccess, StartedAt: &old}); err != nil {
		t.Fatal(err)
	}

	eng.StartRunRetentionSweep(0, time.Millisecond)
	eng.StartRunRetentionSweep(-3, time.Millisecond)
	time.Sleep(50 * time.Millisecond)

	if _, err := s.GetRun("run-ancient"); err != nil {
		t.Fatalf("run purged despite retention being disabled: %v", err)
	}
}
