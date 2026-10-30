package engine

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// getRunCounter counts GetRun calls. Embedding store.Store hides every
// optional capability, so on its own it is a store that can only GetRun.
type getRunCounter struct {
	store.Store
	getRuns int
}

func (s *getRunCounter) GetRun(id string) (*models.Run, error) {
	s.getRuns++
	return s.Store.GetRun(id)
}

// flagReader adds the flag read back on top of the counter.
type flagReader struct {
	*getRunCounter
	reader store.RunCancelRequestReader
}

func (s flagReader) RunCancelRequested(id string) (bool, error) {
	return s.reader.RunCancelRequested(id)
}

// A running run checks its cancel flag every wave and every few seconds.
// GetRun also loaded every node run for it; a store that can read the
// flag alone is asked for that, and any other store still answers.
func TestTheCancelCheckReadsOnlyTheFlagWhenTheStoreCan(t *testing.T) {
	st, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Now().UTC()
	if err := st.CreatePipeline(&models.Pipeline{ID: "p", Name: "p", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(&models.Run{ID: "r", PipelineID: "p", Status: models.RunStatusRunning, StartedAt: &now}); err != nil {
		t.Fatal(err)
	}

	counter := &getRunCounter{Store: st}
	withFlag := &Runner{store: flagReader{getRunCounter: counter, reader: st}}
	fallback := &Runner{store: counter}

	if withFlag.cancelRequested("r") || fallback.cancelRequested("r") {
		t.Fatal("a cancel was seen before one was requested")
	}
	if _, err := st.RequestRunCancel("r"); err != nil {
		t.Fatal(err)
	}
	counter.getRuns = 0
	if !withFlag.cancelRequested("r") {
		t.Error("the flag read missed a requested cancel")
	}
	if counter.getRuns != 0 {
		t.Errorf("the check called GetRun %d times on a store that reads the flag", counter.getRuns)
	}
	if !fallback.cancelRequested("r") {
		t.Error("a store without the flag read missed a requested cancel")
	}
	if counter.getRuns != 1 {
		t.Errorf("the fallback called GetRun %d times, want 1", counter.getRuns)
	}
	if withFlag.cancelRequested("missing") || fallback.cancelRequested("missing") {
		t.Error("a missing run read as a cancel request")
	}
}
