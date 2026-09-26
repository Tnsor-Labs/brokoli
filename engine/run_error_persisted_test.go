package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

/*
 * A run that failed has to persist why.
 *
 * Every terminal path put the reason in the run.terminal event payload
 * and none of them put it on the run, so runs.error stayed empty while
 * the event beside it held the full message. The run rendered as failed
 * with nothing to act on.
 *
 * Production hid this: when a remote worker reports a run's outcome the
 * server writes the error itself, so only runs executed in-process were
 * affected -- which is every deployment running the core engine, and any
 * fleet the moment its workers stop claiming.
 *
 * The invariant these pin is that the stored row agrees with what
 * ProjectRun would rebuild from the same run's events. Those are two
 * paths to one answer and they disagreed.
 */

// runFailingPipeline runs a one-node pipeline whose node cannot succeed.
// Port 1 is not listenable, so the node fails without reaching anything.
func runFailingPipeline(t *testing.T) (store.Store, *models.Run) {
	t.Helper()
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "runerr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	now := time.Now().UTC()
	pipe := &models.Pipeline{
		ID: "p-fail", PipelineID: "p-fail", Name: "p-fail", Enabled: true,
		Nodes: []models.Node{{
			ID: "s1", Name: "Fetch Orders", Type: models.NodeTypeSourceAPI,
			Config: map[string]interface{}{"url": "http://127.0.0.1:1/never"},
		}},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.CreatePipeline(pipe); err != nil {
		t.Fatal(err)
	}
	run, execErr := NewRunner(s, nil, pipe, nil, nil, nil, nil, "", nil).Execute()
	if execErr == nil {
		t.Fatal("the pipeline was supposed to fail")
	}
	return s, run
}

// terminalEventError returns the error the run's own terminal event
// recorded, which is where the message always survived.
func terminalEventError(t *testing.T, s store.Store, runID string) string {
	t.Helper()
	events, err := s.ListEventsByRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.EventType == models.RunEventTerminal || e.EventType == models.RunEventCancelled {
			return e.Payload.Error
		}
	}
	t.Fatalf("run %s has no terminal event", runID)
	return ""
}

// The headline: a failed run says why, in the run itself.
func TestFailedRunPersistsWhyItFailed(t *testing.T) {
	s, run := runFailingPipeline(t)

	stored, err := s.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != models.RunStatusFailed {
		t.Fatalf("status = %q, want failed", stored.Status)
	}
	if stored.Error == "" {
		t.Fatal("run is persisted as failed with an EMPTY error; the reason exists only in " +
			"run_events and logs, so the run view has nothing to show")
	}
	for _, want := range []string{"Fetch Orders", "s1"} {
		if !strings.Contains(stored.Error, want) {
			t.Errorf("persisted error %q does not mention %q", stored.Error, want)
		}
	}
}

// The invariant. The row written live and the row rebuilt from events
// are two answers to one question and must not differ.
func TestPersistedFailedRunAgreesWithItsOwnEvents(t *testing.T) {
	s, run := runFailingPipeline(t)

	stored, err := s.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	events, err := s.ListEventsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	projected := ProjectRun(run.ID, events)

	if stored.Status != projected.Status {
		t.Errorf("status: stored %q, projected %q", stored.Status, projected.Status)
	}
	if stored.Error != projected.Error {
		t.Errorf("error disagrees between the stored row and its own events:\n  stored    = %q\n  projected = %q",
			stored.Error, projected.Error)
	}
}

// The in-memory run handed back to the caller must carry it too: callers
// read the returned run rather than re-fetching it.
func TestReturnedRunCarriesTheError(t *testing.T) {
	s, run := runFailingPipeline(t)

	if run.Error == "" {
		t.Error("Execute returned a failed run whose Error is empty")
	}
	stored, err := s.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Error != stored.Error {
		t.Errorf("returned run error %q does not match the persisted %q", run.Error, stored.Error)
	}
}

// Cancellation is a terminal outcome with a reason too, and it goes
// through a different function (finalizeCancelled).
func TestCancelledRunPersistsThatItWasCancelled(t *testing.T) {
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "cancel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	now := time.Now().UTC()
	pipe := &models.Pipeline{
		ID: "p-cancel", PipelineID: "p-cancel", Name: "p-cancel", Enabled: true,
		Nodes: []models.Node{{
			ID: "s1", Name: "Source", Type: models.NodeTypeSourceAPI,
			Config: map[string]interface{}{"url": "http://127.0.0.1:1/never"},
		}},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.CreatePipeline(pipe); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(s, nil, pipe, nil, nil, nil, nil, "", nil)
	r.Cancel()
	run, execErr := r.Execute()
	if execErr == nil {
		t.Fatal("a pre-cancelled run should return an error")
	}

	stored, err := s.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != models.RunStatusCancelled {
		t.Fatalf("status = %q, want cancelled", stored.Status)
	}
	if stored.Error == "" {
		t.Error("a cancelled run is persisted with an empty error, so nothing distinguishes it " +
			"from a run cancelled for some other reason")
	}
	if got, want := stored.Error, terminalEventError(t, s, run.ID); got != want {
		t.Errorf("stored error %q does not match its own event payload %q", got, want)
	}
}

// The control, and a real regression risk: runner.go clears run.Error on
// the success path on purpose, because a recovery-requeued run carries
// the previous execution's explanation. Setting the error on the failure
// paths must not leak into a successful run.
func TestSuccessfulRunPersistsNoError(t *testing.T) {
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "ok.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	csv := filepath.Join(t.TempDir(), "in.csv")
	if err := os.WriteFile(csv, []byte("id,name\n1,a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	pipe := &models.Pipeline{
		ID: "p-ok", PipelineID: "p-ok", Name: "p-ok", Enabled: true,
		Nodes: []models.Node{{
			ID: "s1", Name: "Source", Type: models.NodeTypeSourceFile,
			Config: map[string]interface{}{"path": csv, "format": "csv"},
		}},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.CreatePipeline(pipe); err != nil {
		t.Fatal(err)
	}
	run, execErr := NewRunner(s, nil, pipe, nil, nil, nil, nil, "", nil).Execute()
	if execErr != nil {
		t.Fatalf("run failed: %v", execErr)
	}
	stored, err := s.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != models.RunStatusSuccess {
		t.Fatalf("status = %q, want success", stored.Status)
	}
	if stored.Error != "" {
		t.Errorf("a successful run carries error %q; the failure paths leaked into it", stored.Error)
	}
}
