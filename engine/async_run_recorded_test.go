package engine

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// slowCreateStore takes its time recording a run, as a busy database
// does: long enough that a run recorded after its id is handed out is
// visibly missing when the caller looks for it at once.
type slowCreateStore struct {
	*store.SQLiteStore
}

func (s slowCreateStore) CreateRun(r *models.Run) error {
	time.Sleep(300 * time.Millisecond)
	return s.SQLiteStore.CreateRun(r)
}

// TestAnAsyncRunExistsWhenItsIDIsReturned: the API answers 202 with the
// id RunPipelineAsyncOpts returns, and a client may ask for that run
// immediately (an agent's trigger_run does). On the in-process path the
// runner used to record the run inside its goroutine, so the lookup could
// answer 404.
func TestAnAsyncRunExistsWhenItsIDIsReturned(t *testing.T) {
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "async.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	seedCancelTestPipeline(t, s, "p-async")
	eng := drainEngineOnCleanup(t, NewEngine(slowCreateStore{s}))

	id, err := eng.RunPipelineAsyncOpts("p-async", RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.GetRun(id)
	if err != nil || run == nil {
		t.Fatalf("run %s is not recorded when its id is returned: %v", id, err)
	}
	if run.Status != models.RunStatusRunning && run.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %s", run.Status)
	}
}
