package engine

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

func newSyncTestScheduler(t *testing.T) (*Scheduler, store.Store) {
	t.Helper()
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "sched-sync.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return NewScheduler(NewEngine(s), s, nil), s
}

func addScheduled(t *testing.T, s store.Store, id, schedule string, enabled bool) *models.Pipeline {
	t.Helper()
	p := &models.Pipeline{ID: id, Name: id, Schedule: schedule, Enabled: enabled}
	if err := s.CreatePipeline(p); err != nil {
		t.Fatalf("CreatePipeline: %v", err)
	}
	return p
}

// A pipeline created after the scheduler started must still be picked
// up. In distributed mode the scheduler runs in its own pod while
// SyncPipeline is called from the API's process, so nothing carried the
// change across and nothing re-read the store: the pipeline never ran,
// with no error and no missed-run warning.
func TestSchedulerPicksUpPipelinesCreatedAfterStart(t *testing.T) {
	sched, st := newSyncTestScheduler(t)
	if err := sched.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer sched.Stop()

	if got := len(sched.Status()); got != 0 {
		t.Fatalf("expected nothing scheduled at boot, got %d", got)
	}

	addScheduled(t, st, "p-new", "* * * * *", true)
	sched.syncSchedulesFromStore()

	status := sched.Status()
	if len(status) != 1 || status[0].PipelineID != "p-new" {
		t.Fatalf("expected the new pipeline to be scheduled, got %+v", status)
	}
}

// Disabling a pipeline removes its entry.
func TestSchedulerDropsDisabledPipeline(t *testing.T) {
	sched, st := newSyncTestScheduler(t)
	p := addScheduled(t, st, "p1", "* * * * *", true)
	if err := sched.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer sched.Stop()
	if len(sched.Status()) != 1 {
		t.Fatal("expected one schedule after start")
	}

	p.Enabled = false
	if err := st.UpdatePipeline(p); err != nil {
		t.Fatalf("UpdatePipeline: %v", err)
	}
	sched.syncSchedulesFromStore()

	if got := len(sched.Status()); got != 0 {
		t.Fatalf("expected the disabled pipeline to be dropped, got %d", got)
	}
}

// A pipeline deleted from the store stops being scheduled.
func TestSchedulerDropsDeletedPipeline(t *testing.T) {
	sched, st := newSyncTestScheduler(t)
	addScheduled(t, st, "p1", "* * * * *", true)
	if err := sched.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer sched.Stop()

	if err := st.DeletePipeline("p1"); err != nil {
		t.Fatalf("DeletePipeline: %v", err)
	}
	sched.syncSchedulesFromStore()

	if got := len(sched.Status()); got != 0 {
		t.Fatalf("expected the deleted pipeline to be dropped, got %d", got)
	}
}

// A changed cron expression takes effect without a restart.
func TestSchedulerAppliesChangedSchedule(t *testing.T) {
	sched, st := newSyncTestScheduler(t)
	p := addScheduled(t, st, "p1", "0 5 * * *", true)
	if err := sched.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer sched.Stop()
	before := sched.NextRun("p1")

	p.Schedule = "*/2 * * * *"
	if err := st.UpdatePipeline(p); err != nil {
		t.Fatalf("UpdatePipeline: %v", err)
	}
	sched.syncSchedulesFromStore()

	after := sched.NextRun("p1")
	if !after.Before(before) {
		t.Fatalf("expected the tighter schedule to move the next run earlier: before=%v after=%v", before, after)
	}
}

// The reconcile loop runs on its own, not only when called directly.
func TestScheduleSyncLoopFires(t *testing.T) {
	prev := scheduleSyncInterval
	scheduleSyncInterval = 20 * time.Millisecond
	defer func() { scheduleSyncInterval = prev }()

	sched, st := newSyncTestScheduler(t)
	if err := sched.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer sched.Stop()

	addScheduled(t, st, "p-late", "* * * * *", true)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(sched.Status()) == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the sync loop never registered a pipeline added after start")
}
