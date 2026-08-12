package engine

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/store"
)

// fakeInstanceJobQueue simulates a remote worker for
// dispatchExpansionInstanceRemotely tests: Enqueue starts a goroutine that,
// after a short delay, settles the job's execution attempt exactly the way
// a real worker plus the enterprise instance-result endpoint would (write
// the result through ArtifactStore, then CompleteAttempt/FailAttempt using
// the fencing generation the job already carries) — proving the
// dispatcher's own wait-and-fetch logic is correct without needing a real
// worker or the enterprise repo to exist.
type fakeInstanceJobQueue struct {
	attempts  store.ExecutionAttemptStore
	artifacts ArtifactStore
	delay     time.Duration
	// respond is called with the enqueued job on the simulated worker's
	// goroutine; return failWith non-empty to simulate a failed instance
	// instead of a successful one. Nil respond means "never respond" —
	// simulating a worker that never reports back, for the timeout test.
	respond func(job extensions.RunJob) (columns []string, rows []common.DataRow, failWith string)
}

func (f *fakeInstanceJobQueue) Enqueue(job extensions.RunJob) error {
	if f.respond == nil {
		return nil // simulated worker never reports back
	}
	go func() {
		time.Sleep(f.delay)
		cols, rows, failWith := f.respond(job)
		if failWith != "" {
			_ = f.attempts.FailAttempt(job.RunID, job.NodeID, job.InstanceKey, job.Attempt, job.FencingGeneration, failWith)
			return
		}
		_ = f.artifacts.WriteArtifact(job.RunID, job.NodeID, job.InstanceKey, &common.DataSet{Columns: cols, Rows: rows})
		_ = f.attempts.CompleteAttempt(job.RunID, job.NodeID, job.InstanceKey, job.Attempt, job.FencingGeneration)
	}()
	return nil
}
func (f *fakeInstanceJobQueue) Dequeue() (extensions.RunJob, error) {
	return extensions.RunJob{}, extensions.ErrQueueClosed
}
func (f *fakeInstanceJobQueue) Ack(jobID string) error             { return nil }
func (f *fakeInstanceJobQueue) Fail(jobID string, err error) error { return nil }
func (f *fakeInstanceJobQueue) Len() int                           { return 0 }
func (f *fakeInstanceJobQueue) Close() error                       { return nil }

// remoteDispatchTestPipeline is a one-item expansion pipeline — the
// smallest shape that exercises dispatchExpansionInstanceRemotely without
// the local-vs-remote branch mattering for anything other than execution
// itself.
func remoteDispatchTestPipeline(id string) *models.Pipeline {
	now := time.Now().UTC()
	return &models.Pipeline{
		ID: id, Name: id, Enabled: true, CreatedAt: now, UpdatedAt: now,
		Nodes: []models.Node{
			{ID: "files", Type: models.NodeTypeSourceFile, Name: "Files", Config: map[string]interface{}{"path": "", "format": "csv"}},
			{
				ID: "parse", Type: models.NodeTypeCode, Name: "Parse",
				Config: map[string]interface{}{
					"script":    `output_data = {"columns": columns, "rows": rows}`,
					"expansion": map[string]interface{}{"over": map[string]interface{}{"file": "files"}},
					"timeout":   float64(5),
				},
			},
		},
		Edges: []models.Edge{{From: "files", To: "parse"}},
	}
}

func TestPipeline_ExpandRemoteDispatch_Succeeds(t *testing.T) {
	realStore := newExpansionTestStore(t, "expand-remote-ok")
	real := realStore.(*store.SQLiteStore)

	dir := t.TempDir()
	filesCSV := writeCSV(t, dir, "files.csv", "path\na.csv\n")
	pipeline := remoteDispatchTestPipeline("expand-remote-ok-pipeline")
	pipeline.Nodes[0].Config["path"] = filesCSV
	if err := real.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}

	eng := NewEngine(real)
	eng.ArtifactStore = NewLocalDiskArtifactStore(filepath.Join(dir, "artifacts"))
	eng.InstanceJobQueue = &fakeInstanceJobQueue{
		attempts: real, artifacts: eng.ArtifactStore, delay: 50 * time.Millisecond,
		respond: func(job extensions.RunJob) ([]string, []common.DataRow, string) {
			if job.WorkOrder == nil {
				t.Errorf("enqueued job has no WorkOrder — remote dispatch must populate it")
			} else if job.WorkOrder.NodeType != "code" {
				t.Errorf("WorkOrder.NodeType = %q, want code", job.WorkOrder.NodeType)
			}
			return []string{"path", "from"}, []common.DataRow{{"path": "a.csv", "from": "remote-worker"}}, ""
		},
	}

	run, err := eng.RunPipeline(pipeline.ID)
	if err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %s, want success", run.Status)
	}

	attempt, err := real.GetExecutionAttempt(run.ID, "parse", "idx:0", 0)
	if err != nil {
		t.Fatalf("GetExecutionAttempt: %v", err)
	}
	if attempt.Status != models.AttemptStatusCompleted {
		t.Errorf("attempt status = %s, want completed", attempt.Status)
	}
	if attempt.ClaimedBy == "" {
		t.Error("attempt claimed_by is empty, want the dispatching engine's instance ID")
	}

	ds, err := eng.ArtifactStore.ReadArtifact(run.ID, "parse", "idx:0")
	if err != nil {
		t.Fatalf("ReadArtifact: %v", err)
	}
	if len(ds.Rows) != 1 || ds.Rows[0]["from"] != "remote-worker" {
		t.Errorf("artifact rows = %v, want one row from=remote-worker (the fake worker's own result)", ds.Rows)
	}
}

func TestPipeline_ExpandRemoteDispatch_WorkerFailureFailsTheRun(t *testing.T) {
	realStore := newExpansionTestStore(t, "expand-remote-fail")
	real := realStore.(*store.SQLiteStore)

	dir := t.TempDir()
	filesCSV := writeCSV(t, dir, "files.csv", "path\na.csv\n")
	pipeline := remoteDispatchTestPipeline("expand-remote-fail-pipeline")
	pipeline.Nodes[0].Config["path"] = filesCSV
	if err := real.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}

	eng := NewEngine(real)
	eng.ArtifactStore = NewLocalDiskArtifactStore(filepath.Join(dir, "artifacts"))
	eng.InstanceJobQueue = &fakeInstanceJobQueue{
		attempts: real, artifacts: eng.ArtifactStore, delay: 50 * time.Millisecond,
		respond: func(job extensions.RunJob) ([]string, []common.DataRow, string) {
			return nil, nil, "simulated worker script failure"
		},
	}

	run, err := eng.RunPipeline(pipeline.ID)
	if err == nil {
		t.Fatal("expected RunPipeline to fail when the remote worker reports failure")
	}
	if run == nil || run.Status != models.RunStatusFailed {
		t.Fatalf("expected a failed run, got %+v", run)
	}

	attempt, aerr := real.GetExecutionAttempt(run.ID, "parse", "idx:0", 0)
	if aerr != nil {
		t.Fatalf("GetExecutionAttempt: %v", aerr)
	}
	if attempt.Status != models.AttemptStatusFailed || attempt.Error != "simulated worker script failure" {
		t.Errorf("attempt = %+v, want status=failed error=\"simulated worker script failure\"", attempt)
	}
}

// TestPipeline_ExpandRemoteDispatch_CancellationSettlesAsFailed exercises
// dispatchExpansionInstanceRemotely's waitCtx.Done() branch via
// cancellation rather than its own timeoutSec+remoteInstanceDispatchMargin
// deadline elapsing. Both share one code path (the select's <-waitCtx.Done()
// case), but the pure-timeout half of that path is not reachable through
// RunPipeline today: an expansion item's own timeoutSec and the enclosing
// node's outer per-attempt timeout (engine/runner.go's executeNode,
// node.Config["timeout"]) read the SAME config field, and the outer one
// carries no margin — so it always elapses strictly before this function's
// own (timeoutSec + remoteInstanceDispatchMargin) deadline could, and
// executeNode's own timeout fires the node's failure first. That coupling
// predates this change and is out of scope to fix here; cancellation is
// the genuinely reachable trigger for this branch, and proves the same
// fencing-checked settlement behavior the pure-timeout case would.
func TestPipeline_ExpandRemoteDispatch_CancellationSettlesAsFailed(t *testing.T) {
	realStore := newExpansionTestStore(t, "expand-remote-cancel")
	real := realStore.(*store.SQLiteStore)

	dir := t.TempDir()
	filesCSV := writeCSV(t, dir, "files.csv", "path\na.csv\n")
	pipeline := remoteDispatchTestPipeline("expand-remote-cancel-pipeline")
	pipeline.Nodes[0].Config["path"] = filesCSV
	if err := real.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}

	eng := NewEngine(real)
	eng.ArtifactStore = NewLocalDiskArtifactStore(filepath.Join(dir, "artifacts"))
	eng.InstanceJobQueue = &fakeInstanceJobQueue{respond: nil} // never responds

	type outcome struct {
		run *models.Run
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		run, err := eng.RunPipeline(pipeline.ID)
		done <- outcome{run, err}
	}()

	// Poll for the run row to exist, then cancel it — RunPipeline only
	// registers the run as active (what CancelRun looks up) after it
	// starts, so there is no run ID to cancel until then.
	var runID string
	deadline := time.Now().Add(5 * time.Second)
	for runID == "" && time.Now().Before(deadline) {
		runs, err := real.ListRunsByPipeline(pipeline.ID, 1)
		if err == nil && len(runs) == 1 {
			runID = runs[0].ID
		}
		time.Sleep(20 * time.Millisecond)
	}
	if runID == "" {
		t.Fatal("run never appeared to cancel")
	}
	// Poll for the instance's own attempt to reach Started (claimed and
	// acked — see executeExpansionInstance) rather than guessing at a fixed
	// sleep: cancelling before dispatchExpansionInstanceRemotely has even
	// claimed the attempt means this function's own cancellation handling
	// is never reached at all (some earlier point in the call stack notices
	// r.ctx first instead), leaving the attempt claimed-but-never-settled —
	// a real race this test hit under load before this fix, not a
	// pre-existing/unrelated flake.
	attemptDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(attemptDeadline) {
		attempt, err := real.GetExecutionAttempt(runID, "parse", "idx:0", 0)
		if err == nil && attempt.Status == models.AttemptStatusStarted {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := eng.CancelRun(runID); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}

	select {
	case out := <-done:
		if out.err == nil {
			t.Fatal("expected RunPipeline to fail when cancelled while waiting for a remote instance")
		}
		// CancelRun's own status write and the run's own failure path
		// (triggered by the same cancellation, via this function's
		// "pipeline cancelled" error) race independently of anything this
		// test is exercising — pre-existing, unrelated engine plumbing.
		// Either terminal outcome proves what matters here: the run does
		// not hang, and (checked below) the instance's attempt gets
		// settled rather than left dangling.
		if out.run == nil || (out.run.Status != models.RunStatusCancelled && out.run.Status != models.RunStatusFailed) {
			t.Fatalf("expected a cancelled or failed run, got %+v", out.run)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("RunPipeline did not return after cancellation")
	}

	// executeNode's own outer select (engine/runner.go) races its
	// attemptCtx.Done() case against the goroutine actually running
	// runNodeLogic — pre-existing behavior, not something this change
	// alters. On cancellation that outer select can return "pipeline
	// cancelled" for the node before the inner goroutine (still inside
	// dispatchExpansionInstanceRemotely) reaches its own waitCtx.Done()
	// case and calls FailAttempt — so settlement here is eventually
	// consistent with the run's own reported failure, not synchronous
	// with it. Poll instead of asserting immediately, and do so before
	// t.Cleanup closes the store out from under that still-running
	// goroutine.
	settleDeadline := time.Now().Add(5 * time.Second)
	var attempt *models.ExecutionAttempt
	for time.Now().Before(settleDeadline) {
		a, aerr := real.GetExecutionAttempt(runID, "parse", "idx:0", 0)
		if aerr != nil {
			t.Fatalf("GetExecutionAttempt: %v", aerr)
		}
		attempt = a
		if attempt.Status == models.AttemptStatusFailed || attempt.Status == models.AttemptStatusCompleted {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if attempt.Status != models.AttemptStatusFailed {
		t.Errorf("attempt status = %s, want failed (settled by the dispatcher's own cancellation handling)", attempt.Status)
	}
}
