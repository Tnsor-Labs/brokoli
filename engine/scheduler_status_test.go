package engine

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/store"
)

// statusProbeStore records how Status reads the last runs: per pipeline,
// and whether the scheduler's lock was held at the time.
type statusProbeStore struct {
	store.Store
	sched       *Scheduler
	perPipeline int
	lockHeld    bool
}

func (s *statusProbeStore) ListRunsByPipeline(id string, limit int) ([]models.Run, error) {
	s.perPipeline++
	return s.Store.ListRunsByPipeline(id, limit)
}

func (s *statusProbeStore) GetLatestRunsByPipelineIDs(ids []string) (map[string]*models.Run, error) {
	if s.sched.mu.TryLock() {
		s.sched.mu.Unlock()
	} else {
		s.lockHeld = true
	}
	return s.Store.GetLatestRunsByPipelineIDs(ids)
}

// Status read each schedule's last run with its own query while holding
// the lock a firing schedule needs, so a long list held every schedule up.
// It now reads them in one query, after releasing the lock.
func TestSchedulerStatusReadsLastRunsOnceOutsideTheLock(t *testing.T) {
	st, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "sched.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	probe := &statusProbeStore{Store: st}
	sched := NewScheduler(drainEngineOnCleanup(t, NewEngine(st)), probe, &fakeLeaderElector{leader: true})
	probe.sched = sched

	now := time.Now().UTC()
	for _, id := range []string{"a", "b", "c"} {
		if err := st.CreatePipeline(&models.Pipeline{ID: id, Name: id, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := sched.Register(id, id, "0 * * * *", ""); err != nil {
			t.Fatal(err)
		}
	}
	started := time.Date(2026, 10, 29, 8, 0, 0, 0, time.UTC)
	if err := st.CreateRun(&models.Run{ID: common.NewID(), PipelineID: "b", Status: models.RunStatusSuccess, StartedAt: &started}); err != nil {
		t.Fatal(err)
	}

	infos := sched.Status()
	if probe.perPipeline != 0 {
		t.Errorf("Status made %d per-pipeline queries, want 0", probe.perPipeline)
	}
	if probe.lockHeld {
		t.Error("Status queried the store while holding the scheduler lock")
	}
	if len(infos) != 3 {
		t.Fatalf("%d schedules, want 3", len(infos))
	}
	for _, info := range infos {
		want := ""
		if info.PipelineID == "b" {
			want = "2026-10-29T08:00:00Z"
		}
		if info.LastRun != want {
			t.Errorf("%s: LastRun = %q, want %q", info.PipelineID, info.LastRun, want)
		}
	}
}
