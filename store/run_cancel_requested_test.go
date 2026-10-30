package store

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

// cancelFlagStore is what the tests below need from a store.
type cancelFlagStore interface {
	Store
	RunCancelRequester
	RunCancelRequestReader
}

func seedRunWithNodes(t testing.TB, s Store, nodes int) string {
	t.Helper()
	now := time.Now().UTC()
	p := &models.Pipeline{ID: fmt.Sprintf("p-%d", now.UnixNano()), Name: "p", CreatedAt: now, UpdatedAt: now}
	if err := s.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	run := &models.Run{ID: fmt.Sprintf("r-%d", now.UnixNano()), PipelineID: p.ID, Status: models.RunStatusRunning, StartedAt: &now}
	if err := s.CreateRun(run); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < nodes; i++ {
		if err := s.CreateNodeRun(&models.NodeRun{ID: fmt.Sprintf("%s-n%d", run.ID, i), RunID: run.ID, NodeID: fmt.Sprintf("n%d", i), Status: models.RunStatusSuccess, Attempt: 1}); err != nil {
			t.Fatal(err)
		}
	}
	return run.ID
}

func testRunCancelRequested(t *testing.T, s cancelFlagStore) {
	runID := seedRunWithNodes(t, s, 3)
	if got, err := s.RunCancelRequested(runID); err != nil || got {
		t.Fatalf("before a request: %v, %v; want false", got, err)
	}
	if ok, err := s.RequestRunCancel(runID); err != nil || !ok {
		t.Fatalf("RequestRunCancel: %v, %v", ok, err)
	}
	if got, err := s.RunCancelRequested(runID); err != nil || !got {
		t.Fatalf("after a request: %v, %v; want true", got, err)
	}
	if run, err := s.GetRun(runID); err != nil || !run.CancelRequested {
		t.Fatalf("GetRun disagrees: %+v, %v", run, err)
	}
	if _, err := s.RunCancelRequested("no-such-run"); err == nil {
		t.Error("a missing run read as no request instead of an error")
	}
}

// The flag reads back as RequestRunCancel left it, as GetRun reports it.
func TestSQLiteRunCancelRequested(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	testRunCancelRequested(t, s)
}

func TestPostgresRunCancelRequested(t *testing.T) {
	testRunCancelRequested(t, openTestPostgresStore(t))
}

// What a running run pays per cancel check: GetRun loads every node run,
// the flag read one column.
func BenchmarkCancelCheck(b *testing.B) {
	s, err := NewSQLiteStore(filepath.Join(b.TempDir(), "c.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { s.Close() })
	for _, nodes := range []int{50, 500} {
		runID := seedRunWithNodes(b, s, nodes)
		b.Run(fmt.Sprintf("GetRun/%d-nodes", nodes), func(b *testing.B) {
			for b.Loop() {
				if _, err := s.GetRun(runID); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("RunCancelRequested/%d-nodes", nodes), func(b *testing.B) {
			for b.Loop() {
				if _, err := s.RunCancelRequested(runID); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
