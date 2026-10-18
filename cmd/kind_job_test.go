package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/extensions"
)

// settleQueue records how runKindJob settles a job.
type settleQueue struct {
	extensions.JobQueue
	mu      sync.Mutex
	acked   []string
	failed  []string
	renewed int
}

func (q *settleQueue) Ack(id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.acked = append(q.acked, id)
	return nil
}

func (q *settleQueue) Fail(id string, _ error) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.failed = append(q.failed, id)
	return nil
}

func (q *settleQueue) RenewClaim(string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.renewed++
	return nil
}

func TestKindJobIsAcknowledgedWhenHandled(t *testing.T) {
	q := &settleQueue{}
	var got extensions.RunJob
	runKindJob(context.Background(), q, func(_ context.Context, j extensions.RunJob) error {
		got = j
		return nil
	}, extensions.RunJob{ID: "j1", Kind: "repository_sync", Params: map[string]string{"sync": "s1"}})
	if len(q.acked) != 1 || q.acked[0] != "j1" || len(q.failed) != 0 {
		t.Fatalf("acked %v failed %v, want j1 acknowledged", q.acked, q.failed)
	}
	if got.Kind != "repository_sync" || got.Params["sync"] != "s1" {
		t.Fatalf("handler got %+v", got)
	}
}

func TestKindJobReturnsToTheQueueWhenItCannotBeHandled(t *testing.T) {
	q := &settleQueue{}
	runKindJob(context.Background(), q, func(context.Context, extensions.RunJob) error {
		return errors.New("input unavailable")
	}, extensions.RunJob{ID: "j2", Kind: "repository_sync"})
	if len(q.failed) != 1 || q.failed[0] != "j2" || len(q.acked) != 0 {
		t.Fatalf("acked %v failed %v, want j2 returned", q.acked, q.failed)
	}
}

func TestKindJobKeepsItsClaimWhileRunning(t *testing.T) {
	prev := jobClaimRenewInterval
	jobClaimRenewInterval = 5 * time.Millisecond
	t.Cleanup(func() { jobClaimRenewInterval = prev })

	q := &settleQueue{}
	runKindJob(context.Background(), q, func(context.Context, extensions.RunJob) error {
		time.Sleep(60 * time.Millisecond)
		return nil
	}, extensions.RunJob{ID: "j3", Kind: "repository_sync"})
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.renewed == 0 {
		t.Fatal("a long kind job never renewed its claim")
	}
}

// The kind travels with the job through a queue's JSON encoding, and a
// pipeline run has none.
func TestRunJobKindRoundTrips(t *testing.T) {
	data, err := json.Marshal(extensions.RunJob{ID: "j", Kind: "repository_sync"})
	if err != nil {
		t.Fatal(err)
	}
	var back extensions.RunJob
	if err := json.Unmarshal(data, &back); err != nil || back.Kind != "repository_sync" {
		t.Fatalf("round trip = %+v (%v)", back, err)
	}
	run, _ := json.Marshal(extensions.RunJob{ID: "r", RunID: "r", PipelineID: "p"})
	if strings.Contains(string(run), `"kind"`) {
		t.Fatalf("a pipeline run serialized a kind: %s", run)
	}
}
