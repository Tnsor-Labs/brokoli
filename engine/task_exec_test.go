package engine

// End-to-end 'task' node execution (ADR-033 rollout phase 2b, issue
// #439 step 5) through the real engine: a task node with a real
// task-bundle/v2 archive runs through Runner.runTask,
// pkg/taskharness+pyharness, and a real python3, and its scalar output
// becomes the node's DataSet the same way every other node type's does.

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/taskbundlev2"
	"github.com/Tnsor-Labs/brokoli/store"
)

func skipIfNoPython3(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH")
	}
}

const taskOrg = "org-task2b"

type taskTestEngine struct {
	eng *Engine
	s   *store.SQLiteStore
}

func newTaskEngine(t *testing.T) *taskTestEngine {
	t.Helper()
	eng, s := newResumeTestEngine(t)
	return &taskTestEngine{eng: eng, s: s}
}

// bundle uploads a one-file python task-bundle/v2 (module "fixture_task",
// symbol "run") whose source is exactly the caller's, and returns its
// digest.
func (e *taskTestEngine) bundle(t *testing.T, source string) string {
	t.Helper()
	// placeholderDigest fills the manifest's own semantic digest fields
	// (interface/source/payload) -- distinct from the archive's content
	// address below, which is what the store is actually keyed by.
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
	if created, err := e.s.PutTaskBundleV2(taskOrg, digest, archive); err != nil || !created {
		t.Fatalf("seed task bundle v2: created=%v err=%v", created, err)
	}
	return digest
}

func (e *taskTestEngine) runPipeline(t *testing.T, id, digest string, params map[string]string) (*models.Run, error) {
	t.Helper()
	pipeline := &models.Pipeline{
		ID: id, Name: id, Enabled: true, OrgID: taskOrg,
		Nodes: []models.Node{
			{ID: "task", Type: models.NodeTypeTask, Name: "Task", Config: map[string]interface{}{
				"task_bundle": map[string]interface{}{"digest": digest, "format": taskbundlev2.Format},
			}},
		},
	}
	if err := e.s.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}
	if len(params) > 0 {
		return e.eng.RunPipeline(pipeline.ID, params)
	}
	return e.eng.RunPipeline(pipeline.ID)
}

func (e *taskTestEngine) firstTaskRow(t *testing.T, run *models.Run) map[string]interface{} {
	t.Helper()
	ds, err := e.eng.ArtifactStore.ReadArtifact(run.ID, "task", "")
	if err != nil {
		t.Fatalf("read task artifact: %v", err)
	}
	if len(ds.Rows) == 0 {
		t.Fatalf("task node produced no rows (cols %v)", ds.Columns)
	}
	return map[string]interface{}(ds.Rows[0])
}

func TestTaskNodeRunsAScalarOutputEndToEnd(t *testing.T) {
	skipIfNoPython3(t)
	e := newTaskEngine(t)
	digest := e.bundle(t, "def run():\n    return 42\n")
	run, err := e.runPipeline(t, "p-task-basic", digest, nil)
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	row := e.firstTaskRow(t, run)
	// toF64 (taskbundle_exec_test.go) tolerates the numeric widths the
	// artifact store round-trips a scalar through (int64 vs float64).
	if got := toF64(row["result"]); got != 42 {
		t.Fatalf("task output result = %v (type %T), want 42", row["result"], row["result"])
	}
}

func TestTaskNodeRaisingFailsTheRunWithUserCode(t *testing.T) {
	skipIfNoPython3(t)
	e := newTaskEngine(t)
	digest := e.bundle(t, "def run():\n    raise ValueError('boom')\n")
	_, execErr := e.runPipeline(t, "p-task-raises", digest, nil)
	if execErr == nil {
		t.Fatal("a raising task ran to success")
	}
	if !strings.Contains(execErr.Error(), "user_code") || !strings.Contains(execErr.Error(), "boom") {
		t.Fatalf("failure does not name the user_code category and message: %s", execErr)
	}
}

func TestTaskNodeMissingFromStoreFailsTheRun(t *testing.T) {
	skipIfNoPython3(t)
	e := newTaskEngine(t)
	_, execErr := e.runPipeline(t, "p-task-missing", "sha256:"+strings.Repeat("f", 64), nil)
	if execErr == nil {
		t.Fatal("a pipeline referencing an unstored task bundle ran to success")
	}
	if !strings.Contains(execErr.Error(), "not stored for this org") {
		t.Fatalf("missing-bundle failure does not explain itself: %s", execErr)
	}
}

func TestTaskNodeReceivesRunParametersAsKwargs(t *testing.T) {
	skipIfNoPython3(t)
	e := newTaskEngine(t)
	digest := e.bundle(t, "def run(name):\n    return 'hello ' + name\n")
	run, err := e.runPipeline(t, "p-task-kwargs", digest, map[string]string{"name": "world"})
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	row := e.firstTaskRow(t, run)
	if got := row["result"]; got != "hello world" {
		t.Fatalf("task output result = %v, want %q", got, "hello world")
	}
}

// readTaskResult's own unit coverage lives here (not the engine test
// suite above) since the reference harness always emits "scalar" --
// there is no way to drive a "dataset"-kind candidate through a real
// run without a second harness this phase doesn't build.
func TestReadTaskResult_DatasetOutputKindIsNotYetSupported(t *testing.T) {
	dir := t.TempDir()
	digest := "sha256:" + strings.Repeat("0", 62) + "aa"
	resultPath := writeTestResult(t, dir, `{
		"contract": "brokoli.task-result/v1",
		"interface_digest": "`+digest+`",
		"outputs": {"result": {"kind": "dataset", "path": "out.ndjson", "codec": "ndjson/v1", "size_bytes": 0, "checksum": "sha256:`+strings.Repeat("0", 64)+`"}}
	}`)
	if _, err := readTaskResult(resultPath, digest); err == nil || !strings.Contains(err.Error(), "not yet supported") {
		t.Fatalf("expected a clear not-yet-supported error, got: %v", err)
	}
}

func TestReadTaskResult_InterfaceDigestMismatchIsRefused(t *testing.T) {
	dir := t.TempDir()
	digest := "sha256:" + strings.Repeat("0", 62) + "aa"
	other := "sha256:" + strings.Repeat("1", 64)
	resultPath := writeTestResult(t, dir, `{
		"contract": "brokoli.task-result/v1",
		"interface_digest": "`+other+`",
		"outputs": {"result": {"kind": "scalar", "value": 1}}
	}`)
	if _, err := readTaskResult(resultPath, digest); err == nil {
		t.Fatal("expected an interface_digest mismatch to be refused")
	}
}

func writeTestResult(t *testing.T, dir, content string) string {
	t.Helper()
	path := dir + "/result.json"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestValidateTaskRuntimeV1_CapabilitiesAdvertiseTheFeatures(t *testing.T) {
	want := map[string]bool{"task-runtime-v1": false, "task-bundle-v2": false}
	for _, f := range models.SupportedExecutionFeatures {
		if _, ok := want[f]; ok {
			want[f] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Fatalf("%q is not in models.SupportedExecutionFeatures; the capabilities endpoint and SDK preflight cannot see it", name)
		}
	}
}
