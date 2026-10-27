package cmd

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

type listFailsStore struct{ store.Store }

func (listFailsStore) ListPipelines() ([]models.Pipeline, error) {
	return nil, errors.New("database unavailable")
}

// A scheduler that failed to start was logged as a warning and the server
// carried on without one, so nothing ran on a schedule. Starting it now
// returns the error, which serve returns, so the process exits.
func TestStartSchedulerFailsWhenTheSchedulerCannotStart(t *testing.T) {
	st, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	eng := engine.NewEngine(st)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = eng.Close(ctx)
	})

	sched, stop, err := startScheduler(eng, listFailsStore{st})
	if err == nil || !strings.Contains(err.Error(), "scheduler failed to start") {
		t.Fatalf("err = %v, want a scheduler start failure", err)
	}
	if sched != nil || stop != nil {
		t.Fatal("a failed start returned a scheduler")
	}

	sched, stop, err = startScheduler(eng, st)
	if err != nil {
		t.Fatalf("a working store failed to start the scheduler: %v", err)
	}
	if sched == nil {
		t.Fatal("no scheduler returned")
	}
	stop()
}
