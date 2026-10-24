package engine

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

type listFailsStore struct{ store.Store }

func (listFailsStore) ListPipelines() ([]models.Pipeline, error) {
	return nil, errors.New("database unavailable")
}

func stopReturns(t *testing.T, sched *Scheduler, what string) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		sched.Stop()
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s: Stop did not return", what)
	}
}

// serve logs a failed Start and carries on, with Stop deferred. Stop
// waited for the three loops Start launches, which a failed Start never
// launched, so shutdown hung until the process was killed. A second Stop
// panicked closing a closed channel. Stop now returns in both cases.
func TestSchedulerStopReturnsAfterAFailedStartAndWhenCalledTwice(t *testing.T) {
	st, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "sched.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	eng := NewEngine(st)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = eng.Close(ctx)
	})

	failed := NewScheduler(eng, listFailsStore{st}, &fakeLeaderElector{leader: true})
	if err := failed.Start(); err == nil {
		t.Fatal("Start succeeded although listing pipelines failed")
	}
	stopReturns(t, failed, "after a failed Start")
	stopReturns(t, failed, "a second time after a failed Start")

	started := NewScheduler(eng, st, &fakeLeaderElector{leader: true})
	if err := started.Start(); err != nil {
		t.Fatal(err)
	}
	stopReturns(t, started, "after Start")
	stopReturns(t, started, "a second time after Start")
}
