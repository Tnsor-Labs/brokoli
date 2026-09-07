package engine

// ADR-033 rollout phase 2c: remote (distributed) task-node dispatch.
// Reuses fakeInstanceJobQueue (engine/expansion_remote_dispatch_test.go)
// to simulate a remote worker, but the simulated worker's "respond"
// callback calls the REAL ExecuteTaskWorkOrderContext against a real
// store -- proving the actual worker-side execution path, not just the
// generic dispatch-and-wait machinery already proven by the code-node
// remote-dispatch tests.

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/taskbundlev2"
	"github.com/Tnsor-Labs/brokoli/store"
)

const taskRemoteOrg = "org-task-remote"

func taskRemoteDispatchPipeline(id, digest string) *models.Pipeline {
	now := time.Now().UTC()
	return &models.Pipeline{
		ID: id, Name: id, Enabled: true, OrgID: taskRemoteOrg, CreatedAt: now, UpdatedAt: now,
		Nodes: []models.Node{
			{ID: "task", Type: models.NodeTypeTask, Name: "Task", Config: map[string]interface{}{
				"task_bundle": map[string]interface{}{"digest": digest, "format": taskbundlev2.Format},
			}},
		},
	}
}

// taskRemoteDispatchPipelineWithOutputInterface is taskRemoteDispatchPipeline
// plus an explicit ADR-032 Interface declaring the task's "result" output
// port as scalar-kind outputType, proving phase 3b's output-boundary
// validation travels through remote dispatch's WorkOrder (extensions.
// InstanceWorkOrder.NodeInterface), not just local execution.
func taskRemoteDispatchPipelineWithOutputInterface(id, digest string, outputType map[string]interface{}) *models.Pipeline {
	p := taskRemoteDispatchPipeline(id, digest)
	p.Nodes[0].Interface = map[string]interface{}{
		"contract": "brokoli.task-interface/v1",
		"inputs":   map[string]interface{}{},
		"outputs": map[string]interface{}{
			"result": map[string]interface{}{
				"value": map[string]interface{}{"kind": "scalar", "type": outputType},
			},
		},
	}
	return p
}

func seedRemoteTaskBundle(t *testing.T, s *store.SQLiteStore, source string) string {
	t.Helper()
	return seedRemoteTaskBundleFor(t, s, taskbundlev2.RuntimePython, "fixture_task.py", source)
}

// seedRemoteTaskBundleFor builds and stores a one-file, one-payload
// bundle for the given runtime class -- the same fixture shape for both
// reference adapters, since what differs between them is the adapter the
// engine selects, not anything about the bundle's structure.
func seedRemoteTaskBundleFor(t *testing.T, s *store.SQLiteStore, runtimeClass, fileName, source string) string {
	t.Helper()
	placeholderDigest := "sha256:" + strings.Repeat("0", 62) + "aa"
	archive, err := taskbundlev2.Assemble(
		map[string]string{fileName: source},
		&taskbundlev2.Manifest{
			Format:          taskbundlev2.Format,
			Name:            "fixture-task",
			InterfaceDigest: placeholderDigest,
			SourceDigest:    placeholderDigest,
			Payloads: []taskbundlev2.Payload{{
				ID:            runtimeClass + "-any",
				Runtime:       runtimeClass,
				OS:            "any",
				Arch:          "any",
				Entrypoint:    taskbundlev2.Entrypoint{Module: "fixture_task", Symbol: "run"},
				Effects:       taskbundlev2.EffectPure,
				PayloadDigest: placeholderDigest,
			}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	digest := taskbundlev2.DigestOf(archive)
	if created, err := s.PutTaskBundleV2(taskRemoteOrg, digest, archive); err != nil || !created {
		t.Fatalf("seed task bundle v2: created=%v err=%v", created, err)
	}
	return digest
}

func TestTaskNodeRemoteDispatch_Succeeds(t *testing.T) {
	skipIfNoPython3(t)
	realStore := newExpansionTestStore(t, "task-remote-ok")
	real := realStore.(*store.SQLiteStore)
	digest := seedRemoteTaskBundle(t, real, "def run():\n    return 99\n")

	pipeline := taskRemoteDispatchPipeline("task-remote-ok-pipeline", digest)
	if err := real.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	eng := drainEngineOnCleanup(t, NewEngine(real))
	eng.ArtifactStore = NewLocalDiskArtifactStore(filepath.Join(dir, "artifacts"))
	eng.InstanceJobQueue = &fakeInstanceJobQueue{
		attempts: real, artifacts: eng.ArtifactStore, delay: 10 * time.Millisecond,
		respond: func(job extensions.RunJob) ([]string, []common.DataRow, string) {
			if job.WorkOrder == nil {
				t.Fatal("enqueued job has no WorkOrder")
			}
			if job.WorkOrder.NodeType != string(models.NodeTypeTask) {
				t.Errorf("WorkOrder.NodeType = %q, want task", job.WorkOrder.NodeType)
			}
			if job.WorkOrder.OrgID != taskRemoteOrg {
				t.Errorf("WorkOrder.OrgID = %q, want %q", job.WorkOrder.OrgID, taskRemoteOrg)
			}
			found := false
			for _, c := range job.RequiredCapabilities {
				if c == taskRuntimeCapability {
					found = true
				}
			}
			if !found {
				t.Errorf("RequiredCapabilities = %v, want it to include %q", job.RequiredCapabilities, taskRuntimeCapability)
			}
			// The real worker-side executor, run against the real store --
			// this is the actual thing being tested, not a fake result.
			ds, err := ExecuteTaskWorkOrderContext(context.Background(), real, job.RunID, job.NodeID, job.WorkOrder)
			if err != nil {
				return nil, nil, err.Error()
			}
			return ds.Columns, ds.Rows, ""
		},
	}

	run, err := eng.RunPipeline(pipeline.ID)
	if err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %s, want success", run.Status)
	}

	attempt, err := real.GetExecutionAttempt(run.ID, "task", "", 0)
	if err != nil {
		t.Fatalf("GetExecutionAttempt: %v", err)
	}
	if attempt.Status != models.AttemptStatusCompleted {
		t.Errorf("attempt status = %s, want completed", attempt.Status)
	}

	ds, err := eng.ArtifactStore.ReadArtifact(run.ID, "task", "")
	if err != nil {
		t.Fatalf("ReadArtifact: %v", err)
	}
	if len(ds.Rows) != 1 || toF64(ds.Rows[0]["result"]) != 99 {
		t.Errorf("artifact rows = %v, want one row result=99", ds.Rows)
	}
}

func TestTaskNodeRemoteDispatch_WorkerFailureFailsTheRun(t *testing.T) {
	skipIfNoPython3(t)
	realStore := newExpansionTestStore(t, "task-remote-fail")
	real := realStore.(*store.SQLiteStore)
	digest := seedRemoteTaskBundle(t, real, "def run():\n    raise ValueError('remote boom')\n")

	pipeline := taskRemoteDispatchPipeline("task-remote-fail-pipeline", digest)
	if err := real.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	eng := drainEngineOnCleanup(t, NewEngine(real))
	eng.ArtifactStore = NewLocalDiskArtifactStore(filepath.Join(dir, "artifacts"))
	eng.InstanceJobQueue = &fakeInstanceJobQueue{
		attempts: real, artifacts: eng.ArtifactStore, delay: 10 * time.Millisecond,
		respond: func(job extensions.RunJob) ([]string, []common.DataRow, string) {
			ds, err := ExecuteTaskWorkOrderContext(context.Background(), real, job.RunID, job.NodeID, job.WorkOrder)
			if err != nil {
				return nil, nil, err.Error()
			}
			return ds.Columns, ds.Rows, ""
		},
	}

	run, err := eng.RunPipeline(pipeline.ID)
	if err == nil {
		t.Fatal("expected RunPipeline to fail when the remote worker's task raises")
	}
	if run == nil || run.Status != models.RunStatusFailed {
		t.Fatalf("expected a failed run, got %+v", run)
	}
	attempt, aerr := real.GetExecutionAttempt(run.ID, "task", "", 0)
	if aerr != nil {
		t.Fatalf("GetExecutionAttempt: %v", aerr)
	}
	if attempt.Status != models.AttemptStatusFailed || !strings.Contains(attempt.Error, "remote boom") {
		t.Errorf("attempt = %+v, want status=failed error containing \"remote boom\"", attempt)
	}
}

// ADR-032 section 10 / phase 3b, remote path: the node's declared output
// Interface must actually travel over the WorkOrder
// (extensions.InstanceWorkOrder.NodeInterface) to a remote worker, not
// just be enforced locally -- a task returning a string against a
// declared int64 output must fail the same way it does in the local
// dispatch test (TestTaskNodeOutputContractViolationFailsTheRun,
// task_exec_test.go), proving the field is threaded through dispatch
// rather than silently dropped.
func TestTaskNodeRemoteDispatch_OutputContractViolationFailsTheRun(t *testing.T) {
	skipIfNoPython3(t)
	realStore := newExpansionTestStore(t, "task-remote-contract")
	real := realStore.(*store.SQLiteStore)
	digest := seedRemoteTaskBundle(t, real, "def run():\n    return 'not-a-number'\n")

	pipeline := taskRemoteDispatchPipelineWithOutputInterface("task-remote-contract-pipeline", digest, map[string]interface{}{"kind": "int64"})
	if err := real.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	eng := drainEngineOnCleanup(t, NewEngine(real))
	eng.ArtifactStore = NewLocalDiskArtifactStore(filepath.Join(dir, "artifacts"))
	eng.InstanceJobQueue = &fakeInstanceJobQueue{
		attempts: real, artifacts: eng.ArtifactStore, delay: 10 * time.Millisecond,
		respond: func(job extensions.RunJob) ([]string, []common.DataRow, string) {
			if job.WorkOrder == nil || job.WorkOrder.NodeInterface == nil {
				t.Fatal("enqueued job's WorkOrder carries no NodeInterface")
			}
			ds, err := ExecuteTaskWorkOrderContext(context.Background(), real, job.RunID, job.NodeID, job.WorkOrder)
			if err != nil {
				return nil, nil, err.Error()
			}
			return ds.Columns, ds.Rows, ""
		},
	}

	run, err := eng.RunPipeline(pipeline.ID)
	if err == nil {
		t.Fatal("expected RunPipeline to fail when the remote task's output violates its declared contract")
	}
	if run == nil || run.Status != models.RunStatusFailed {
		t.Fatalf("expected a failed run, got %+v", run)
	}
	attempt, aerr := real.GetExecutionAttempt(run.ID, "task", "", 0)
	if aerr != nil {
		t.Fatalf("GetExecutionAttempt: %v", aerr)
	}
	if !strings.Contains(attempt.Error, "violates its declared contract") {
		t.Errorf("attempt error = %q, want it to name the contract violation", attempt.Error)
	}
	if strings.Contains(attempt.Error, "not-a-number") {
		t.Errorf("attempt error leaked the observed value: %q", attempt.Error)
	}
}

// ADR-033 phase 4a on the remote path: adapter selection happens
// worker-side, inside ExecuteTaskWorkOrderContext, so a node payload has
// to work through remote dispatch too -- and that is worth proving
// rather than inferring from "both paths call executeTaskBundle." The
// WorkOrder carries only the bundle digest, with no hint of the runtime
// class anywhere in it, which is exactly why the dispatcher cannot tag
// the job by runtime today (the deferred per-runtime capability work).
func TestTaskNodeRemoteDispatch_NodePayloadSucceeds(t *testing.T) {
	skipIfNoNode(t)
	realStore := newExpansionTestStore(t, "task-remote-node")
	real := realStore.(*store.SQLiteStore)
	digest := seedRemoteTaskBundleFor(t, real, taskbundlev2.RuntimeNode, "fixture_task.mjs",
		"export function run() {\n  return 99;\n}\n")

	pipeline := taskRemoteDispatchPipeline("task-remote-node-pipeline", digest)
	if err := real.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	eng := drainEngineOnCleanup(t, NewEngine(real))
	eng.ArtifactStore = NewLocalDiskArtifactStore(filepath.Join(dir, "artifacts"))
	eng.InstanceJobQueue = &fakeInstanceJobQueue{
		attempts: real, artifacts: eng.ArtifactStore, delay: 10 * time.Millisecond,
		respond: func(job extensions.RunJob) ([]string, []common.DataRow, string) {
			ds, err := ExecuteTaskWorkOrderContext(context.Background(), real, job.RunID, job.NodeID, job.WorkOrder)
			if err != nil {
				return nil, nil, err.Error()
			}
			return ds.Columns, ds.Rows, ""
		},
	}

	run, err := eng.RunPipeline(pipeline.ID)
	if err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %s, want success", run.Status)
	}
	ds, err := eng.ArtifactStore.ReadArtifact(run.ID, "task", "")
	if err != nil {
		t.Fatalf("ReadArtifact: %v", err)
	}
	if len(ds.Rows) != 1 || toF64(ds.Rows[0]["result"]) != 99 {
		t.Errorf("artifact rows = %v, want one row result=99", ds.Rows)
	}

	// The pinned execution record must name the node adapter's own
	// environment, not a python one -- the digest covers adapter identity
	// per runtime class precisely so these cannot collide.
	rec, err := real.GetResolvedExecutionRecord(run.ID, "task")
	if err != nil {
		t.Fatalf("GetResolvedExecutionRecord: %v", err)
	}
	if rec.PayloadID != "node-any" {
		t.Errorf("pinned payload = %q, want node-any", rec.PayloadID)
	}
}

// TestExecuteInstanceJobContext_RoutesTaskNodesToTheTaskExecutor is a
// narrower, direct test of the executeInstanceJobContext routing itself
// (bypassing the dispatcher side entirely) -- constructs a RunJob by
// hand and confirms the shared-store worker loop reaches
// ExecuteTaskWorkOrderContext, not ExecuteInstanceWorkOrderContext's
// generic "unsupported node type" refusal.
func TestExecuteInstanceJobContext_RoutesTaskNodesToTheTaskExecutor(t *testing.T) {
	skipIfNoPython3(t)
	realStore := newExpansionTestStore(t, "task-remote-routing")
	real := realStore.(*store.SQLiteStore)
	digest := seedRemoteTaskBundle(t, real, "def run():\n    return 7\n")

	pipeline := taskRemoteDispatchPipeline("task-remote-routing-pipeline", digest)
	if err := real.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}
	run := &models.Run{ID: common.NewID(), PipelineID: pipeline.ID, Status: models.RunStatusRunning}
	if err := real.CreateRun(run); err != nil {
		t.Fatal(err)
	}
	if err := real.WithTx(func(tx *sql.Tx) error {
		return real.CreateExecutionAttemptTx(tx, &models.ExecutionAttempt{
			RunID: run.ID, NodeID: "task", InstanceKey: "", Attempt: 0,
			Status: models.AttemptStatusQueued, IdempotencyKey: run.ID + ":task",
		})
	}); err != nil {
		t.Fatalf("seed execution attempt: %v", err)
	}
	gen, claimed, err := real.ClaimAttempt(run.ID, "task", "", 0, "test-instance", store.DefaultLeaseDuration)
	if err != nil || !claimed {
		t.Fatalf("ClaimAttempt: claimed=%v err=%v", claimed, err)
	}

	dir := t.TempDir()
	artifacts := NewLocalDiskArtifactStore(filepath.Join(dir, "artifacts"))
	job := extensions.RunJob{
		ID: common.NewID(), PipelineID: pipeline.ID, RunID: run.ID, OrgID: taskRemoteOrg,
		NodeID: "task", InstanceKey: "", Attempt: 0, FencingGeneration: gen,
		WorkOrder: &extensions.InstanceWorkOrder{
			NodeType: string(models.NodeTypeTask), OrgID: taskRemoteOrg,
			Config: map[string]interface{}{
				"task_bundle": map[string]interface{}{"digest": digest, "format": taskbundlev2.Format},
			},
			TimeoutSeconds: 10,
		},
	}
	if err := ExecuteInstanceJob(real, artifacts, job); err != nil {
		t.Fatalf("ExecuteInstanceJob: %v", err)
	}

	attempt, err := real.GetExecutionAttempt(run.ID, "task", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.Status != models.AttemptStatusCompleted {
		t.Fatalf("attempt status = %s, want completed (a generic refusal would have failed it instead)", attempt.Status)
	}
	ds, err := artifacts.ReadArtifact(run.ID, "task", "")
	if err != nil {
		t.Fatalf("ReadArtifact: %v", err)
	}
	if len(ds.Rows) != 1 || toF64(ds.Rows[0]["result"]) != 7 {
		t.Errorf("artifact rows = %v, want one row result=7", ds.Rows)
	}
}

// ADR-033 phase 4b (runtime-aware placement): a task job now carries a
// per-runtime capability tag alongside the protocol tag, so a node
// bundle is not handed to a worker that only has python. The tag has to
// reach the ENQUEUED job -- that is the only place a queue backend can
// filter on it.
func TestTaskNodeRemoteDispatch_CarriesPerRuntimeCapabilityTag(t *testing.T) {
	// No skipIfNoNode: nothing here executes the bundle, so this runs
	// (and guards the tagging) even on a host without a node runtime.
	realStore := newExpansionTestStore(t, "task-remote-caps")
	real := realStore.(*store.SQLiteStore)
	digest := seedRemoteTaskBundleFor(t, real, taskbundlev2.RuntimeNode, "fixture_task.mjs",
		"export function run() {\n  return 1;\n}\n")

	pipeline := taskRemoteDispatchPipeline("task-remote-caps-pipeline", digest)
	if err := real.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	eng := drainEngineOnCleanup(t, NewEngine(real))
	eng.ArtifactStore = NewLocalDiskArtifactStore(filepath.Join(dir, "artifacts"))
	var gotCaps []string
	eng.InstanceJobQueue = &fakeInstanceJobQueue{
		attempts: real, artifacts: eng.ArtifactStore, delay: 10 * time.Millisecond,
		respond: func(job extensions.RunJob) ([]string, []common.DataRow, string) {
			// Capture and answer without executing: this test is about
			// what the DISPATCHER enqueues, and running the bundle for
			// real would cost a node subprocess to prove nothing extra
			// (TestTaskNodeRemoteDispatch_NodePayloadSucceeds already
			// covers worker-side execution). The engine package runs
			// close to its CI timeout -- see #329 -- so a full run per
			// assertion is a cost worth not paying twice.
			gotCaps = job.RequiredCapabilities
			return []string{"result"}, []common.DataRow{{"result": float64(1)}}, ""
		},
	}

	if _, err := eng.RunPipeline(pipeline.ID); err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}

	var hasProtocol, hasRuntime bool
	for _, c := range gotCaps {
		if c == taskRuntimeCapability {
			hasProtocol = true
		}
		if c == taskRuntimeCapabilityFor(taskbundlev2.RuntimeNode) {
			hasRuntime = true
		}
	}
	if !hasProtocol || !hasRuntime {
		t.Errorf("RequiredCapabilities = %v, want both %q and %q", gotCaps, taskRuntimeCapability, taskRuntimeCapabilityFor(taskbundlev2.RuntimeNode))
	}
	for _, c := range gotCaps {
		if c == taskRuntimeCapabilityFor(taskbundlev2.RuntimePython) {
			t.Errorf("a node-only bundle asked for the python runtime tag: %v", gotCaps)
		}
	}
}
