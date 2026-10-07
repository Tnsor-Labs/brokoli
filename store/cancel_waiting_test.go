package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

// CancelWaitingRun moves only a waiting run, so it and the watcher's wake
// (ClaimWaitingRun) cannot both win.
func testCancelWaitingRun(t *testing.T, s Store) {
	t.Helper()
	canceller, ok := s.(WaitingRunCanceller)
	if !ok {
		t.Fatal("store does not implement WaitingRunCanceller")
	}
	now := time.Now().UTC()
	pid := "p-cancel-waiting-" + now.Format("150405.000000000")
	if err := s.CreatePipeline(&models.Pipeline{ID: pid, Name: pid, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	mk := func(id string, status models.RunStatus) {
		if err := s.CreateRun(&models.Run{ID: id, PipelineID: pid, Status: status, StartedAt: &now}); err != nil {
			t.Fatal(err)
		}
	}
	mk(pid+"-w", models.RunStatusWaiting)
	mk(pid+"-r", models.RunStatusRunning)

	if ok, err := canceller.CancelWaitingRun(pid+"-w", now); err != nil || !ok {
		t.Fatalf("cancel waiting run: %v %v", ok, err)
	}
	if got, _ := s.GetRun(pid + "-w"); got.Status != models.RunStatusCancelled || got.FinishedAt == nil {
		t.Fatalf("waiting run after cancel = %s (finished %v)", got.Status, got.FinishedAt)
	}
	if claimed, _ := s.ClaimWaitingRun(pid + "-w"); claimed {
		t.Fatal("a cancelled run was woken")
	}
	if ok, err := canceller.CancelWaitingRun(pid+"-r", now); err != nil || ok {
		t.Fatalf("cancel of a running run through CancelWaitingRun = %v %v, want false", ok, err)
	}
	if got, _ := s.GetRun(pid + "-r"); got.Status != models.RunStatusRunning {
		t.Fatalf("running run changed to %s", got.Status)
	}
}

func TestSQLiteCancelWaitingRun(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "cw.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	testCancelWaitingRun(t, s)
}

func TestPostgresCancelWaitingRun(t *testing.T) {
	url := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	s, err := NewPostgresStore(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	testCancelWaitingRun(t, s)
}
