package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

var (
	_ InstanceAttemptStore = (*SQLiteStore)(nil)
	_ InstanceAttemptStore = (*PostgresStore)(nil)
)

type instanceAttemptStore interface {
	InstanceAttemptStore
	ExecutionAttemptStore
	CreatePipeline(*models.Pipeline) error
	CreateRun(*models.Run) error
	ListExpansionInstancesByRun(runID string) ([]models.ExpansionInstance, error)
	WithTx(func(*sql.Tx) error) error
}

func onBothDialects(t *testing.T, check func(t *testing.T, s instanceAttemptStore)) {
	t.Run("sqlite", func(t *testing.T) {
		s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "ia.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		check(t, s)
	})
	t.Run("postgres", func(t *testing.T) {
		dsn := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
		if dsn == "" {
			t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
		}
		s, err := NewPostgresStore(dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		check(t, s)
	})
}

func newInstanceRun(t *testing.T, s instanceAttemptStore) string {
	t.Helper()
	now := time.Now().UTC()
	pid := "p-ia-" + time.Now().Format("150405.000000000")
	if err := s.CreatePipeline(&models.Pipeline{ID: pid, Name: pid, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	run := &models.Run{ID: "r-" + pid, PipelineID: pid, Status: models.RunStatusRunning, StartedAt: &now}
	if err := s.CreateRun(run); err != nil {
		t.Fatal(err)
	}
	return run.ID
}

func newItem(runID, key string) (*models.ExpansionInstance, *models.ExecutionAttempt) {
	now := time.Now().UTC()
	ei := &models.ExpansionInstance{
		ID: runID + "-" + key, RunID: runID, NodeID: "n", NodeAttempt: 0, InstanceIndex: 0, InstanceKey: key,
		Status: models.RunStatusRunning, StartedAt: &now,
	}
	a := &models.ExecutionAttempt{
		RunID: runID, NodeID: "n", InstanceKey: key, Attempt: 0,
		Status: models.AttemptStatusQueued, IdempotencyKey: runID + ":n:" + key + ":0",
	}
	return ei, a
}

// Start writes what the separate calls wrote: the instance row, and the
// attempt created, claimed by this process and started.
func TestStartInstanceAttemptCreatesClaimsAndAcks(t *testing.T) {
	onBothDialects(t, func(t *testing.T, s instanceAttemptStore) {
		runID := newInstanceRun(t, s)
		ei, a := newItem(runID, "k1")
		gen, claimed, err := s.StartInstanceAttempt(ei, a, "me", time.Minute)
		if err != nil || !claimed || gen != 1 {
			t.Fatalf("start = gen %d claimed %v err %v; want gen 1, claimed", gen, claimed, err)
		}
		got, err := s.GetExecutionAttempt(runID, "n", "k1", 0)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != models.AttemptStatusStarted || got.ClaimedBy != "me" || got.FencingGeneration != 1 {
			t.Fatalf("attempt %+v, want started, claimed by me, generation 1", got)
		}
		if insts, _ := s.ListExpansionInstancesByRun(runID); len(insts) != 1 || insts[0].Status != models.RunStatusRunning {
			t.Fatalf("instances %+v, want one running", insts)
		}
	})
}

// A claim another process holds is refused, as with the separate calls,
// and the rows written before it are kept.
func TestStartInstanceAttemptRespectsAnotherProcessesClaim(t *testing.T) {
	onBothDialects(t, func(t *testing.T, s instanceAttemptStore) {
		runID := newInstanceRun(t, s)
		_, a := newItem(runID, "k1")
		if err := s.WithTx(func(tx *sql.Tx) error { return s.CreateExecutionAttemptTx(tx, a) }); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := s.ClaimAttempt(runID, "n", "k1", 0, "other", time.Hour); err != nil || !ok {
			t.Fatalf("other's claim: %v %v", ok, err)
		}
		ei, a2 := newItem(runID, "k1")
		_, claimed, err := s.StartInstanceAttempt(ei, a2, "me", time.Minute)
		if err != nil || claimed {
			t.Fatalf("start = claimed %v err %v; want refused without an error", claimed, err)
		}
		got, _ := s.GetExecutionAttempt(runID, "n", "k1", 0)
		if got.ClaimedBy != "other" {
			t.Fatalf("attempt now claimed by %q, want still other", got.ClaimedBy)
		}
		if insts, _ := s.ListExpansionInstancesByRun(runID); len(insts) != 1 {
			t.Fatalf("instances %+v, want the instance row kept", insts)
		}
	})
}

// Settle records the outcome on both rows; a settle the fencing check
// refuses still records the instance's outcome, and reports the refusal.
func TestSettleInstanceAttempt(t *testing.T) {
	onBothDialects(t, func(t *testing.T, s instanceAttemptStore) {
		runID := newInstanceRun(t, s)

		ok1, a1 := newItem(runID, "ok")
		gen, _, _ := s.StartInstanceAttempt(ok1, a1, "me", time.Minute)
		ok1.Status, ok1.RowCount = models.RunStatusSuccess, 3
		if iErr, aErr := s.SettleInstanceAttempt(ok1, "ok", gen); iErr != nil || aErr != nil {
			t.Fatalf("settle success: %v %v", iErr, aErr)
		}
		if got, _ := s.GetExecutionAttempt(runID, "n", "ok", 0); got.Status != models.AttemptStatusCompleted {
			t.Fatalf("attempt %s, want completed", got.Status)
		}

		bad, a2 := newItem(runID, "bad")
		gen, _, _ = s.StartInstanceAttempt(bad, a2, "me", time.Minute)
		bad.Status, bad.Error = models.RunStatusFailed, "boom"
		if iErr, aErr := s.SettleInstanceAttempt(bad, "bad", gen); iErr != nil || aErr != nil {
			t.Fatalf("settle failure: %v %v", iErr, aErr)
		}
		if got, _ := s.GetExecutionAttempt(runID, "n", "bad", 0); got.Status != models.AttemptStatusFailed || got.Error != "boom" {
			t.Fatalf("attempt %+v, want failed with boom", got)
		}

		stale, a3 := newItem(runID, "stale")
		gen, _, _ = s.StartInstanceAttempt(stale, a3, "me", time.Minute)
		stale.Status = models.RunStatusSuccess
		iErr, aErr := s.SettleInstanceAttempt(stale, "stale", gen+1)
		if iErr != nil || aErr == nil {
			t.Fatalf("stale settle = instance %v attempt %v; want the instance updated and the settle refused", iErr, aErr)
		}
		if got, _ := s.GetExecutionAttempt(runID, "n", "stale", 0); got.Status != models.AttemptStatusStarted {
			t.Fatalf("attempt %s after a refused settle, want still started", got.Status)
		}
		insts, _ := s.ListExpansionInstancesByRun(runID)
		byKey := map[string]models.RunStatus{}
		for _, i := range insts {
			byKey[i.InstanceKey] = i.Status
		}
		if byKey["ok"] != models.RunStatusSuccess || byKey["bad"] != models.RunStatusFailed || byKey["stale"] != models.RunStatusSuccess {
			t.Fatalf("instance statuses %v", byKey)
		}
	})
}
