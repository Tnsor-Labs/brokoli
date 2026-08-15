package engine

import (
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// idempotentFakeQueue implements extensions.JobQueue with the interface's
// idempotency contract: re-enqueuing a job ID that is already pending is a
// no-op. That contract is exactly what makes the redispatch sweep safe to
// run anywhere, so the fake must honor it for the tests to mean anything.
type idempotentFakeQueue struct {
	mu      sync.Mutex
	pending map[string]extensions.RunJob
	order   []string
}

func newIdempotentFakeQueue() *idempotentFakeQueue {
	return &idempotentFakeQueue{pending: map[string]extensions.RunJob{}}
}

func (q *idempotentFakeQueue) Enqueue(job extensions.RunJob) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, ok := q.pending[job.ID]; ok {
		return nil
	}
	q.pending[job.ID] = job
	q.order = append(q.order, job.ID)
	return nil
}

func (q *idempotentFakeQueue) Dequeue() (extensions.RunJob, error) {
	return extensions.RunJob{}, extensions.ErrQueueClosed
}

func (q *idempotentFakeQueue) Ack(string) error         { return nil }
func (q *idempotentFakeQueue) Fail(string, error) error { return nil }

func (q *idempotentFakeQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.pending)
}

func (q *idempotentFakeQueue) Close() error { return nil }

func (q *idempotentFakeQueue) job(id string) (extensions.RunJob, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	j, ok := q.pending[id]
	return j, ok
}

func seedPendingWithOutbox(t *testing.T, s *store.SQLiteStore, pipeID, runID string, age time.Duration, params map[string]string) {
	t.Helper()
	run := &models.Run{ID: runID, PipelineID: pipeID, Status: models.RunStatusPending, OrgID: "org-1", Params: params}
	if err := s.CreateRun(run); err != nil {
		t.Fatal(err)
	}
	attempt := &models.ExecutionAttempt{
		RunID:          runID,
		Status:         models.AttemptStatusQueued,
		IdempotencyKey: runID,
		CreatedAt:      time.Now().UTC().Add(-age),
	}
	if err := s.WithTx(func(tx *sql.Tx) error {
		return s.CreateExecutionAttemptTx(tx, attempt)
	}); err != nil {
		t.Fatal(err)
	}
}

// TestRedispatchPendingOnce_ReenqueuesLostDispatch is the #216 scenario:
// a run accepted and durably outboxed, whose queue delivery no longer
// exists (Redis wiped). The sweep must rebuild the RunJob — including
// params and org — from the durable rows alone.
func TestRedispatchPendingOnce_ReenqueuesLostDispatch(t *testing.T) {
	eng, s := newExecCtxTestEngine(t)
	seedCancelTestPipeline(t, s, "p-redispatch")
	queue := newIdempotentFakeQueue()
	eng.JobQueue = queue

	seedPendingWithOutbox(t, s, "p-redispatch", "run-lost-dispatch", 10*time.Minute, map[string]string{"who": "sweep"})

	n, err := eng.redispatchPendingOnce(2 * time.Minute)
	if err != nil {
		t.Fatalf("redispatchPendingOnce: %v", err)
	}
	if n != 1 {
		t.Fatalf("redispatched = %d, want 1", n)
	}
	job, ok := queue.job("run-lost-dispatch")
	if !ok {
		t.Fatal("job was not enqueued")
	}
	if job.RunID != "run-lost-dispatch" || job.PipelineID != "p-redispatch" || job.OrgID != "org-1" {
		t.Fatalf("job identity rebuilt wrong: %+v", job)
	}
	if job.Params["who"] != "sweep" {
		t.Fatalf("job params not rebuilt from the run row: %+v", job.Params)
	}
	if job.IdempotencyKey != "run-lost-dispatch" {
		t.Fatalf("idempotency key = %q, want the run ID", job.IdempotencyKey)
	}
}

// TestRedispatchPendingOnce_RespectsGraceAndStatus proves the sweep leaves
// alone everything that is not a lost dispatch: fresh pending runs still
// inside the grace window (a healthy backlog), runs already claimed or
// terminal, and pending rows with no outbox intent at all.
func TestRedispatchPendingOnce_RespectsGraceAndStatus(t *testing.T) {
	eng, s := newExecCtxTestEngine(t)
	seedCancelTestPipeline(t, s, "p-redispatch-skip")
	queue := newIdempotentFakeQueue()
	eng.JobQueue = queue

	// Fresh pending run, inside grace: waiting its turn, not lost.
	seedPendingWithOutbox(t, s, "p-redispatch-skip", "run-fresh", 0, nil)

	// Pending run with no outbox record: nothing proves dispatch intent.
	if err := s.CreateRun(&models.Run{ID: "run-no-outbox", PipelineID: "p-redispatch-skip", Status: models.RunStatusPending}); err != nil {
		t.Fatal(err)
	}

	// Running and terminal runs: not the sweep's business.
	started := time.Now().UTC()
	for _, r := range []*models.Run{
		{ID: "run-running", PipelineID: "p-redispatch-skip", Status: models.RunStatusRunning, StartedAt: &started},
		{ID: "run-done", PipelineID: "p-redispatch-skip", Status: models.RunStatusSuccess, StartedAt: &started},
	} {
		if err := s.CreateRun(r); err != nil {
			t.Fatal(err)
		}
	}

	n, err := eng.redispatchPendingOnce(2 * time.Minute)
	if err != nil {
		t.Fatalf("redispatchPendingOnce: %v", err)
	}
	if n != 0 || queue.Len() != 0 {
		t.Fatalf("redispatched=%d queueLen=%d, want 0/0", n, queue.Len())
	}
}

// TestRedispatchPendingOnce_IdempotentAcrossSweeps proves repeated sweeps
// (every instance runs one, every interval) cannot pile up duplicate
// deliveries: the queue's by-ID idempotency absorbs them.
func TestRedispatchPendingOnce_IdempotentAcrossSweeps(t *testing.T) {
	eng, s := newExecCtxTestEngine(t)
	seedCancelTestPipeline(t, s, "p-redispatch-idem")
	queue := newIdempotentFakeQueue()
	eng.JobQueue = queue

	seedPendingWithOutbox(t, s, "p-redispatch-idem", "run-idem", 10*time.Minute, nil)

	for i := 0; i < 3; i++ {
		if _, err := eng.redispatchPendingOnce(0); err != nil {
			t.Fatalf("sweep %d: %v", i, err)
		}
	}
	if queue.Len() != 1 {
		t.Fatalf("queue length after 3 sweeps = %d, want exactly 1 pending delivery", queue.Len())
	}
}

// TestRedispatchedRunIsClaimableAndCancellable closes the loop with the
// rest of the run lifecycle: a redispatched run is a perfectly normal
// pending run — claimable by ExecuteQueuedRun's CAS, and equally
// cancellable by the pending-cancel CAS, with exactly one side winning.
func TestRedispatchedRunIsClaimableAndCancellable(t *testing.T) {
	eng, s := newExecCtxTestEngine(t)
	seedCancelTestPipeline(t, s, "p-redispatch-claim")
	queue := newIdempotentFakeQueue()
	eng.JobQueue = queue

	seedPendingWithOutbox(t, s, "p-redispatch-claim", "run-reclaim", 10*time.Minute, nil)
	if _, err := eng.redispatchPendingOnce(0); err != nil {
		t.Fatal(err)
	}

	claimed, err := s.ClaimPendingRun("run-reclaim", "p-redispatch-claim", time.Now().UTC(), "trace")
	if err != nil {
		t.Fatal(err)
	}
	if !claimed {
		t.Fatal("redispatched run was not claimable")
	}
	cancelled, err := s.CancelPendingRun("run-reclaim", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if cancelled {
		t.Fatal("pending-cancel CAS succeeded on an already-claimed run — exactly-one-winner violated")
	}
}
