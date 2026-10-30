package engine

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// countingInstanceQueue records how many jobs reached it.
type countingInstanceQueue struct{ enqueued atomic.Int32 }

func (q *countingInstanceQueue) Enqueue(extensions.RunJob) error { q.enqueued.Add(1); return nil }
func (q *countingInstanceQueue) Dequeue() (extensions.RunJob, error) {
	return extensions.RunJob{}, extensions.ErrQueueClosed
}
func (q *countingInstanceQueue) Ack(string) error         { return nil }
func (q *countingInstanceQueue) Fail(string, error) error { return nil }
func (q *countingInstanceQueue) Len() int                 { return 0 }
func (q *countingInstanceQueue) Close() error             { return nil }

// A dispatch that reaches the dispatcher after the run was cancelled must
// not send the job to a worker, which would run it in full, and must
// settle the attempt it was given.
func TestRemoteDispatchAfterCancelSendsNoJob(t *testing.T) {
	s := newExpansionTestStore(t, "dispatch-after-cancel").(*store.SQLiteStore)
	pipe := remoteDispatchTestPipeline("dispatch-after-cancel")
	if err := s.CreatePipeline(pipe); err != nil {
		t.Fatal(err)
	}
	run := &models.Run{ID: "run-dispatch-after-cancel", PipelineID: pipe.ID, Status: models.RunStatusRunning}
	if err := s.CreateRun(run); err != nil {
		t.Fatal(err)
	}
	if err := s.WithTx(func(tx *sql.Tx) error {
		return s.CreateExecutionAttemptTx(tx, &models.ExecutionAttempt{
			RunID: run.ID, NodeID: "parse", InstanceKey: "page-3", Attempt: 1,
			Status: models.AttemptStatusQueued, IdempotencyKey: "k",
		})
	}); err != nil {
		t.Fatal(err)
	}
	gen, claimed, err := s.ClaimAttempt(run.ID, "parse", "page-3", 1, "test", store.DefaultLeaseDuration)
	if err != nil || !claimed {
		t.Fatalf("ClaimAttempt: claimed=%v err=%v", claimed, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	queue := &countingInstanceQueue{}
	r := &Runner{
		ctx: ctx, store: s, run: run, pipe: pipe, instanceJobQueue: queue,
		artifactStore: NewLocalDiskArtifactStore(filepath.Join(t.TempDir(), "artifacts")),
	}

	_, err = r.dispatchInstanceWorkOrderRemotely("parse", 1, "page-3", gen, &extensions.InstanceWorkOrder{}, 5, nil)
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Errorf("err = %v, want a cancellation", err)
	}
	if n := queue.enqueued.Load(); n != 0 {
		t.Errorf("%d job(s) sent to the workers after the cancel, want none", n)
	}
	a, err := s.GetExecutionAttempt(run.ID, "parse", "page-3", 1)
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != models.AttemptStatusFailed {
		t.Errorf("attempt status = %s, want failed", a.Status)
	}
}
