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

func seedRemoteTaskBundle(t *testing.T, s *store.SQLiteStore, source string) string {
	t.Helper()
	placeholderDigest := "sha256:" + strings.Repeat("0", 62) + "aa"
	archive, err := taskbundlev2.Assemble(
		map[string]string{"fixture_task.py": source},
		&taskbundlev2.Manifest{
			Format:          taskbundlev2.Format,
			Name:            "fixture-task",
			InterfaceDigest: placeholderDigest,
			SourceDigest:    placeholderDigest,
			Payloads: []taskbundlev2.Payload{{
				ID:            "python-any",
				Runtime:       taskbundlev2.RuntimePython,
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
