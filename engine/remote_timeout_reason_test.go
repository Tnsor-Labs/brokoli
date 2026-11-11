package engine

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// A job no worker takes timed out with "timed out waiting for a worker"
// and no more, which reads the same whether no worker was running or none
// advertised what the job needs (#529). The failure names the wait and
// the capabilities, on the error and on the settled attempt.
func TestRemoteTimeoutNamesTheCapabilitiesTheJobNeeded(t *testing.T) {
	prev := remoteInstanceDispatchMargin
	remoteInstanceDispatchMargin = 300 * time.Millisecond
	t.Cleanup(func() { remoteInstanceDispatchMargin = prev })

	s := newExpansionTestStore(t, "remote-timeout-reason").(*store.SQLiteStore)
	pipe := remoteDispatchTestPipeline("remote-timeout-reason")
	if err := s.CreatePipeline(pipe); err != nil {
		t.Fatal(err)
	}
	run := &models.Run{ID: "run-remote-timeout", PipelineID: pipe.ID, Status: models.RunStatusRunning}
	if err := s.CreateRun(run); err != nil {
		t.Fatal(err)
	}
	if err := s.WithTx(func(tx *sql.Tx) error {
		return s.CreateExecutionAttemptTx(tx, &models.ExecutionAttempt{
			RunID: run.ID, NodeID: "parse", InstanceKey: "item-1", Attempt: 1,
			Status: models.AttemptStatusQueued, IdempotencyKey: "k",
		})
	}); err != nil {
		t.Fatal(err)
	}
	gen, claimed, err := s.ClaimAttempt(run.ID, "parse", "item-1", 1, "test", store.DefaultLeaseDuration)
	if err != nil || !claimed {
		t.Fatalf("ClaimAttempt: claimed=%v err=%v", claimed, err)
	}
	r := &Runner{
		ctx: context.Background(), store: s, run: run, pipe: pipe,
		instanceJobQueue:     &countingInstanceQueue{}, // no worker ever takes it
		requiredCapabilities: []string{"gpu"},
		artifactStore:        NewLocalDiskArtifactStore(filepath.Join(t.TempDir(), "artifacts")),
	}

	_, err = r.dispatchInstanceWorkOrderRemotely("parse", 1, "item-1", gen, &extensions.InstanceWorkOrder{}, 0, []string{"task-runtime/v1"})
	if err == nil {
		t.Fatal("the dispatch did not time out")
	}
	for _, want := range []string{"timed out waiting for a worker", "within 300ms", "all of gpu, task-runtime/v1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q, want it to mention %q", err, want)
		}
	}
	a, gerr := s.GetExecutionAttempt(run.ID, "parse", "item-1", 1)
	if gerr != nil {
		t.Fatal(gerr)
	}
	if a.Status != models.AttemptStatusFailed || a.Error != err.Error() {
		t.Errorf("attempt %s %q, want failed with the same reason", a.Status, a.Error)
	}
}

// Without capabilities, there is nothing to name, and the message says to
// look at the workers themselves.
func TestRemoteTimeoutReasonWithoutCapabilities(t *testing.T) {
	got := remoteTimeoutReason("item-1", nil, 2*time.Minute)
	if !strings.Contains(got, "no result within 2m0s") || !strings.Contains(got, "worker is running and connected") {
		t.Errorf("reason %q", got)
	}
}
