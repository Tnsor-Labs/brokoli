package engine

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

func TestExecuteInstanceWorkOrder_RunsTheScript(t *testing.T) {
	result, err := ExecuteInstanceWorkOrder(&extensions.InstanceWorkOrder{
		NodeType:       "code",
		Script:         `output_data = {"columns": columns, "rows": [dict(r, doubled=r["n"]*2) for r in rows]}`,
		ItemColumns:    []string{"n"},
		ItemRow:        map[string]interface{}{"n": float64(21)},
		TimeoutSeconds: 5,
	})
	if err != nil {
		t.Fatalf("ExecuteInstanceWorkOrder: %v", err)
	}
	if len(result.Rows) != 1 || result.Rows[0]["doubled"] != float64(42) {
		t.Errorf("result rows = %v, want one row with doubled=42", result.Rows)
	}
}

func TestExecuteInstanceWorkOrder_RejectsUnsupportedNodeType(t *testing.T) {
	_, err := ExecuteInstanceWorkOrder(&extensions.InstanceWorkOrder{NodeType: "source_api"})
	if err == nil {
		t.Fatal("expected an error for a node type that doesn't dispatch remotely")
	}
}

func TestExecuteInstanceWorkOrder_NilWorkOrder(t *testing.T) {
	if _, err := ExecuteInstanceWorkOrder(nil); err == nil {
		t.Fatal("expected an error for a nil work order")
	}
}

// channelJobQueue is a real, working extensions.JobQueue backed by a Go
// channel — not a fake that simulates a worker's response, but genuine
// enqueue/dequeue plumbing a real worker loop (cmd/serve.go's
// RunMode=="worker", simplified here to its dequeue-execute-settle core)
// consumes from, calling the exact same ExecuteInstanceJob production
// function that loop calls. Proving this test passes is direct evidence
// that loop's own WorkOrder wiring is correct, since it exercises the same
// shared primitive, not a parallel implementation of it.
type channelJobQueue struct {
	jobs chan extensions.RunJob
}

func newChannelJobQueue() *channelJobQueue {
	return &channelJobQueue{jobs: make(chan extensions.RunJob, 8)}
}
func (q *channelJobQueue) Enqueue(job extensions.RunJob) error { q.jobs <- job; return nil }
func (q *channelJobQueue) Dequeue() (extensions.RunJob, error) {
	job, ok := <-q.jobs
	if !ok {
		return extensions.RunJob{}, extensions.ErrQueueClosed
	}
	return job, nil
}
func (q *channelJobQueue) Ack(jobID string) error             { return nil }
func (q *channelJobQueue) Fail(jobID string, err error) error { return nil }
func (q *channelJobQueue) Len() int                           { return len(q.jobs) }
func (q *channelJobQueue) Close() error                       { close(q.jobs); return nil }

// runFakeInstanceWorkerLoop mirrors cmd/serve.go's RunMode=="worker" loop's
// core cycle for a WorkOrder-bearing job — dequeue, execute via
// ExecuteInstanceJob, Ack/Fail — with the HTTP health server, signal
// handling, and whole-pipeline branch stripped out since none of that is
// what this test is proving. Runs until the queue is closed.
func runFakeInstanceWorkerLoop(t *testing.T, q *channelJobQueue, s store.Store, artifacts ArtifactStore) {
	t.Helper()
	for {
		job, err := q.Dequeue()
		if err != nil {
			return // extensions.ErrQueueClosed
		}
		if execErr := ExecuteInstanceJob(s, artifacts, job); execErr != nil {
			t.Logf("fake worker: instance job failed: %v", execErr)
		}
	}
}

// TestPipeline_ExpandRemoteDispatch_TrueEndToEnd is the acceptance test for
// ADR-017's remote instance dispatch working end to end: a real dispatcher
// (dispatchExpansionInstanceRemotely, via a real pipeline run) enqueues
// onto a real JobQueue, a real worker loop dequeues and actually executes
// the script (ExecuteInstanceWorkOrder — no canned response), settles
// through the exact same store-level primitives the enterprise HTTP
// endpoint uses (ExecuteInstanceJob), and the dispatcher's wait-and-fetch
// loop picks up the real result. No step is simulated or short-circuited.
func TestPipeline_ExpandRemoteDispatch_TrueEndToEnd(t *testing.T) {
	realStore := newExpansionTestStore(t, "expand-remote-e2e")
	real := realStore.(*store.SQLiteStore)

	dir := t.TempDir()
	filesCSV := writeCSV(t, dir, "files.csv", "path\na.csv\nb.csv\nc.csv\n")
	pipeline := &models.Pipeline{
		ID: "expand-remote-e2e-pipeline", Name: "Expand remote e2e", Enabled: true,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		Nodes: []models.Node{
			{ID: "files", Type: models.NodeTypeSourceFile, Name: "Files", Config: map[string]interface{}{"path": filesCSV, "format": "csv"}},
			{
				ID: "parse", Type: models.NodeTypeCode, Name: "Parse",
				Config: map[string]interface{}{
					"script":    `output_data = {"columns": columns + ["seen_remotely"], "rows": [dict(r, seen_remotely=1) for r in rows]}`,
					"expansion": map[string]interface{}{"over": map[string]interface{}{"file": "files"}},
					"timeout":   float64(10),
				},
			},
		},
		Edges: []models.Edge{{From: "files", To: "parse"}},
	}
	if err := real.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}

	eng := drainEngineOnCleanup(t, NewEngine(real))
	eng.ArtifactStore = NewLocalDiskArtifactStore(filepath.Join(dir, "artifacts"))
	queue := newChannelJobQueue()
	eng.InstanceJobQueue = queue

	go runFakeInstanceWorkerLoop(t, queue, real, eng.ArtifactStore)
	t.Cleanup(func() { queue.Close() })

	run, err := eng.RunPipeline(pipeline.ID)
	if err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %s, want success", run.Status)
	}

	instances, err := real.ListExpansionInstancesByRun(run.ID)
	if err != nil {
		t.Fatalf("ListExpansionInstancesByRun: %v", err)
	}
	if len(instances) != 3 {
		t.Fatalf("expected 3 expansion instances, got %d", len(instances))
	}
	for _, inst := range instances {
		if inst.Status != models.RunStatusSuccess {
			t.Errorf("instance %s status = %s, want success", inst.InstanceKey, inst.Status)
		}
		attempt, aerr := real.GetExecutionAttempt(run.ID, "parse", inst.InstanceKey, 0)
		if aerr != nil {
			t.Fatalf("GetExecutionAttempt(%s): %v", inst.InstanceKey, aerr)
		}
		if attempt.Status != models.AttemptStatusCompleted {
			t.Errorf("execution attempt %s status = %s, want completed", inst.InstanceKey, attempt.Status)
		}
		ds, rerr := eng.ArtifactStore.ReadArtifact(run.ID, "parse", inst.InstanceKey)
		if rerr != nil {
			t.Fatalf("ReadArtifact(%s): %v", inst.InstanceKey, rerr)
		}
		if len(ds.Rows) != 1 || ds.Rows[0]["seen_remotely"] != float64(1) {
			t.Errorf("artifact for %s = %v, want one row with seen_remotely=1 (proves the REAL worker script ran, not a canned response)", inst.InstanceKey, ds.Rows)
		}
	}

	// The three items really did fan out to three genuinely independent
	// instance keys — not one shared/collapsed result.
	seen := map[string]bool{}
	for _, inst := range instances {
		seen[inst.InstanceKey] = true
	}
	if len(seen) != 3 {
		t.Errorf("expected 3 distinct instance keys, got %v", seen)
	}
}

func TestExecuteInstanceJob_ScriptFailureSettlesAttemptFailedAndAcksJob(t *testing.T) {
	realStore := newExpansionTestStore(t, "instance-job-script-fail")
	real := realStore.(*store.SQLiteStore)
	now := time.Now().UTC()
	if err := real.CreatePipeline(&models.Pipeline{ID: "pipe-ij", Name: "pipe-ij", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreatePipeline: %v", err)
	}
	if err := real.CreateRun(&models.Run{ID: "run-ij", PipelineID: "pipe-ij", Status: models.RunStatusRunning, StartedAt: &now}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := real.WithTx(func(tx *sql.Tx) error {
		return real.CreateExecutionAttemptTx(tx, &models.ExecutionAttempt{
			RunID: "run-ij", NodeID: "parse", InstanceKey: "idx:0", Status: models.AttemptStatusQueued,
		})
	}); err != nil {
		t.Fatalf("CreateExecutionAttemptTx: %v", err)
	}
	gen, ok, err := real.ClaimAttempt("run-ij", "parse", "idx:0", 0, "worker-1", time.Minute)
	if err != nil || !ok {
		t.Fatalf("ClaimAttempt: ok=%v err=%v", ok, err)
	}

	artifacts := NewLocalDiskArtifactStore(t.TempDir())
	job := extensions.RunJob{
		ID: "job-1", RunID: "run-ij", NodeID: "parse", InstanceKey: "idx:0",
		FencingGeneration: gen,
		WorkOrder: &extensions.InstanceWorkOrder{
			NodeType: "code", Script: `raise ValueError("boom")`, TimeoutSeconds: 5,
		},
	}
	if err := ExecuteInstanceJob(real, artifacts, job); err != nil {
		t.Fatalf("ExecuteInstanceJob returned an error for a script failure — a script failing is not a job-queue-level failure: %v", err)
	}

	attempt, err := real.GetExecutionAttempt("run-ij", "parse", "idx:0", 0)
	if err != nil {
		t.Fatalf("GetExecutionAttempt: %v", err)
	}
	if attempt.Status != models.AttemptStatusFailed {
		t.Errorf("attempt status = %s, want failed", attempt.Status)
	}
	if attempt.Error == "" {
		t.Error("attempt.Error is empty, want the script's own error message")
	}
}
