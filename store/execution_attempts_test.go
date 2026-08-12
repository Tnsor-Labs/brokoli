package store

import (
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

// seedRunForAttempts creates the pipeline + run rows execution_attempts'
// run_id foreign key requires, mirroring seedRunForEvents in
// run_events_test.go.
func seedRunForAttempts(t *testing.T, s *SQLiteStore, pipelineID, runID string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := s.CreatePipeline(&models.Pipeline{
		ID: pipelineID, Name: "Attempt Test Pipeline", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreatePipeline: %v", err)
	}
	if err := s.CreateRun(&models.Run{ID: runID, PipelineID: pipelineID, Status: models.RunStatusRunning, StartedAt: &now}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
}

// createAttempt inserts a queued outbox/intent row via CreateExecutionAttemptTx
// (the only writer of a fresh row — ClaimAttempt only ever transitions an
// existing row), exactly the way Engine.RunPipelineAsync's dispatch outbox does.
func createAttempt(t *testing.T, s *SQLiteStore, runID, nodeID string, attempt int) {
	t.Helper()
	err := s.WithTx(func(tx *sql.Tx) error {
		return s.CreateExecutionAttemptTx(tx, &models.ExecutionAttempt{
			RunID:          runID,
			NodeID:         nodeID,
			Attempt:        attempt,
			Status:         models.AttemptStatusQueued,
			IdempotencyKey: runID + ":" + nodeID,
		})
	})
	if err != nil {
		t.Fatalf("createAttempt(%s,%s,%d): %v", runID, nodeID, attempt, err)
	}
}

func TestCreateExecutionAttemptTxIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	seedRunForAttempts(t, s, "pipe-outbox", "run-outbox")

	createAttempt(t, s, "run-outbox", "node-a", 0)
	// Re-creating the same (run_id, node_id, attempt) row must be a silent
	// no-op, not an error — duplicate outbox writes from a redelivered
	// dispatch must be safe.
	createAttempt(t, s, "run-outbox", "node-a", 0)

	got, err := s.GetExecutionAttempt("run-outbox", "node-a", "", 0)
	if err != nil {
		t.Fatalf("GetExecutionAttempt: %v", err)
	}
	if got.Status != models.AttemptStatusQueued {
		t.Fatalf("status = %q, want queued", got.Status)
	}
	if got.FencingGeneration != 0 {
		t.Fatalf("fencing generation = %d, want 0 (never claimed)", got.FencingGeneration)
	}
}

func TestClaimAttemptCASOnlyOneWinnerAmongConcurrentClaims(t *testing.T) {
	s := newTestStore(t)
	seedRunForAttempts(t, s, "pipe-claim", "run-claim")
	createAttempt(t, s, "run-claim", "node-a", 0)

	const workers = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	generations := make(map[int64]int)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			gen, ok, err := s.ClaimAttempt("run-claim", "node-a", "", 0, fmt.Sprintf("worker-%d", worker), time.Minute)
			if err != nil {
				t.Errorf("ClaimAttempt(worker %d): %v", worker, err)
				return
			}
			if ok {
				mu.Lock()
				wins++
				generations[gen]++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()

	if wins != 1 {
		t.Fatalf("concurrent claims won by %d workers, want exactly 1", wins)
	}
	if generations[1] != 1 {
		t.Fatalf("winning fencing generation = %v, want {1: 1}", generations)
	}

	got, err := s.GetExecutionAttempt("run-claim", "node-a", "", 0)
	if err != nil {
		t.Fatalf("GetExecutionAttempt: %v", err)
	}
	if got.Status != models.AttemptStatusClaimed {
		t.Fatalf("status after claim = %q, want claimed", got.Status)
	}
	if got.ClaimedBy == "" {
		t.Fatal("claimed_by not recorded")
	}
	if got.FencingGeneration != 1 {
		t.Fatalf("fencing generation = %d, want 1", got.FencingGeneration)
	}
}

func TestClaimAttemptRejectsAlreadyClaimedLiveLease(t *testing.T) {
	s := newTestStore(t)
	seedRunForAttempts(t, s, "pipe-live", "run-live")
	createAttempt(t, s, "run-live", "node-a", 0)

	gen1, ok, err := s.ClaimAttempt("run-live", "node-a", "", 0, "worker-1", time.Hour)
	if err != nil || !ok {
		t.Fatalf("first claim: ok=%v err=%v", ok, err)
	}
	if gen1 != 1 {
		t.Fatalf("first claim generation = %d, want 1", gen1)
	}

	// A second worker attempting to claim the same, still-live-leased
	// attempt must lose (no-op, not an error).
	gen2, ok2, err := s.ClaimAttempt("run-live", "node-a", "", 0, "worker-2", time.Hour)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if ok2 {
		t.Fatalf("second claim succeeded against a live lease (generation %d), want rejected", gen2)
	}
}

func TestClaimAttemptExpiredLeaseCanBeReclaimedWithHigherFencingGeneration(t *testing.T) {
	s := newTestStore(t)
	seedRunForAttempts(t, s, "pipe-expire", "run-expire")
	createAttempt(t, s, "run-expire", "node-a", 0)

	gen1, ok, err := s.ClaimAttempt("run-expire", "node-a", "", 0, "worker-1", -time.Second) // already expired
	if err != nil || !ok {
		t.Fatalf("first claim: ok=%v err=%v", ok, err)
	}
	if gen1 != 1 {
		t.Fatalf("first claim generation = %d, want 1", gen1)
	}

	// worker-1's lease is already expired, so worker-2 must be able to
	// reclaim it, and the fencing generation must strictly increase so
	// worker-1 can detect its lease was reassigned if it wakes back up.
	gen2, ok2, err := s.ClaimAttempt("run-expire", "node-a", "", 0, "worker-2", time.Hour)
	if err != nil {
		t.Fatalf("reclaim after expiry: %v", err)
	}
	if !ok2 {
		t.Fatal("reclaim after expiry was rejected, want accepted")
	}
	if gen2 <= gen1 {
		t.Fatalf("reclaim generation = %d, want > %d", gen2, gen1)
	}

	// worker-1, using its now-stale fencing generation, must fail to renew.
	renewed, err := s.RenewLease("run-expire", "node-a", "", 0, "worker-1", gen1, time.Hour)
	if err != nil {
		t.Fatalf("RenewLease (stale worker-1): %v", err)
	}
	if renewed {
		t.Fatal("stale worker-1 renewed the lease worker-2 now holds")
	}

	// worker-2, using the correct (current) fencing generation, must renew.
	renewed2, err := s.RenewLease("run-expire", "node-a", "", 0, "worker-2", gen2, time.Hour)
	if err != nil {
		t.Fatalf("RenewLease (worker-2): %v", err)
	}
	if !renewed2 {
		t.Fatal("worker-2 failed to renew its own lease")
	}
}

func TestClaimAttemptCannotReclaimTerminalAttempt(t *testing.T) {
	s := newTestStore(t)
	seedRunForAttempts(t, s, "pipe-terminal", "run-terminal")
	createAttempt(t, s, "run-terminal", "node-a", 0)

	gen, ok, err := s.ClaimAttempt("run-terminal", "node-a", "", 0, "worker-1", time.Hour)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if err := s.CompleteAttempt("run-terminal", "node-a", "", 0, gen); err != nil {
		t.Fatalf("CompleteAttempt: %v", err)
	}

	// Duplicate delivery: re-claiming an already-completed attempt is a
	// documented no-op, never an error.
	_, ok2, err := s.ClaimAttempt("run-terminal", "node-a", "", 0, "worker-2", time.Hour)
	if err != nil {
		t.Fatalf("reclaim of completed attempt: %v", err)
	}
	if ok2 {
		t.Fatal("reclaim of a completed attempt succeeded, want rejected")
	}
}

func TestAckAttemptIsIdempotentAndFencingChecked(t *testing.T) {
	s := newTestStore(t)
	seedRunForAttempts(t, s, "pipe-ack", "run-ack")
	createAttempt(t, s, "run-ack", "node-a", 0)

	gen, ok, err := s.ClaimAttempt("run-ack", "node-a", "", 0, "worker-1", time.Hour)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}

	if err := s.AckAttempt("run-ack", "node-a", "", 0, "worker-1", gen); err != nil {
		t.Fatalf("AckAttempt: %v", err)
	}
	got, err := s.GetExecutionAttempt("run-ack", "node-a", "", 0)
	if err != nil {
		t.Fatalf("GetExecutionAttempt: %v", err)
	}
	if got.Status != models.AttemptStatusStarted {
		t.Fatalf("status after ack = %q, want started", got.Status)
	}

	// Re-acking with the same (still-current) fencing generation is a no-op.
	if err := s.AckAttempt("run-ack", "node-a", "", 0, "worker-1", gen); err != nil {
		t.Fatalf("re-AckAttempt: %v", err)
	}

	// Acking with a stale fencing generation must error.
	if err := s.AckAttempt("run-ack", "node-a", "", 0, "worker-1", gen+99); err == nil {
		t.Fatal("AckAttempt with wrong fencing generation succeeded, want error")
	}
}

func TestCompleteAndFailAttemptAreIdempotentNoOpsOnDuplicateSettle(t *testing.T) {
	s := newTestStore(t)
	seedRunForAttempts(t, s, "pipe-settle", "run-settle")
	createAttempt(t, s, "run-settle", "node-a", 0)

	gen, ok, err := s.ClaimAttempt("run-settle", "node-a", "", 0, "worker-1", time.Hour)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}

	if err := s.CompleteAttempt("run-settle", "node-a", "", 0, gen); err != nil {
		t.Fatalf("CompleteAttempt: %v", err)
	}
	// Duplicate delivery completing the same attempt again: documented no-op.
	if err := s.CompleteAttempt("run-settle", "node-a", "", 0, gen); err != nil {
		t.Fatalf("duplicate CompleteAttempt: %v", err)
	}

	got, err := s.GetExecutionAttempt("run-settle", "node-a", "", 0)
	if err != nil {
		t.Fatalf("GetExecutionAttempt: %v", err)
	}
	if got.Status != models.AttemptStatusCompleted {
		t.Fatalf("status = %q, want completed", got.Status)
	}

	// Failing an already-completed attempt is a genuine conflict, not a
	// silent duplicate settle of the same outcome — must error.
	if err := s.FailAttempt("run-settle", "node-a", "", 0, gen, "late failure"); err == nil {
		t.Fatal("FailAttempt on an already-completed attempt succeeded, want error")
	}
}

func TestFailAttemptRecordsError(t *testing.T) {
	s := newTestStore(t)
	seedRunForAttempts(t, s, "pipe-fail", "run-fail")
	createAttempt(t, s, "run-fail", "node-a", 0)

	gen, ok, err := s.ClaimAttempt("run-fail", "node-a", "", 0, "worker-1", time.Hour)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if err := s.FailAttempt("run-fail", "node-a", "", 0, gen, "boom"); err != nil {
		t.Fatalf("FailAttempt: %v", err)
	}
	got, err := s.GetExecutionAttempt("run-fail", "node-a", "", 0)
	if err != nil {
		t.Fatalf("GetExecutionAttempt: %v", err)
	}
	if got.Status != models.AttemptStatusFailed || got.Error != "boom" {
		t.Fatalf("attempt = %+v, want status=failed error=boom", got)
	}

	// Duplicate delivery failing the same attempt again with the same
	// generation: documented no-op.
	if err := s.FailAttempt("run-fail", "node-a", "", 0, gen, "boom again"); err != nil {
		t.Fatalf("duplicate FailAttempt: %v", err)
	}
}
