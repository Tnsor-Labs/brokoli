package engine

// End-to-end 'task' node execution (ADR-033 rollout phase 2b, issue
// #439 step 5) through the real engine: a task node with a real
// task-bundle/v2 archive runs through Runner.runTask,
// pkg/taskharness+pyharness, and a real python3, and its scalar output
// becomes the node's DataSet the same way every other node type's does.

import (
	"errors"
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

// nodeBundle is bundle's node counterpart (ADR-033 phase 4a): the same
// one-file, one-payload shape, declaring the "node" runtime class so the
// engine selects the Node reference adapter for it.
func (e *taskTestEngine) nodeBundle(t *testing.T, source string) string {
	t.Helper()
	placeholderDigest := "sha256:" + strings.Repeat("0", 62) + "aa"
	archive, err := taskbundlev2.Assemble(
		map[string]string{"fixture_task.mjs": source},
		&taskbundlev2.Manifest{
			Format:          taskbundlev2.Format,
			Name:            "fixture-task",
			InterfaceDigest: placeholderDigest,
			SourceDigest:    placeholderDigest,
			Payloads: []taskbundlev2.Payload{{
				ID:            "node-any",
				Runtime:       taskbundlev2.RuntimeNode,
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

// runPipelineWithOutputInterface is runPipeline plus an explicit ADR-032
// Interface on the task node declaring its "result" output port as
// scalar-kind outputType -- exercising phase 3b's output-boundary
// validation, which only runs when the node carries an explicit
// Interface (models.Node.Interface) in the first place.
func (e *taskTestEngine) runPipelineWithOutputInterface(t *testing.T, id, digest string, outputType map[string]interface{}) (*models.Run, error) {
	t.Helper()
	pipeline := &models.Pipeline{
		ID: id, Name: id, Enabled: true, OrgID: taskOrg,
		Nodes: []models.Node{
			{ID: "task", Type: models.NodeTypeTask, Name: "Task", Config: map[string]interface{}{
				"task_bundle": map[string]interface{}{"digest": digest, "format": taskbundlev2.Format},
			}, Interface: map[string]interface{}{
				"contract": "brokoli.task-interface/v1",
				"inputs":   map[string]interface{}{},
				"outputs": map[string]interface{}{
					"result": map[string]interface{}{
						"value": map[string]interface{}{"kind": "scalar", "type": outputType},
					},
				},
			}},
		},
	}
	if err := e.s.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
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

// ADR-032 section 10 / phase 3b: a task's own claim that it produced
// valid output is no longer trusted uncritically once it declares an
// output contract. A task declaring an int64 "result" that actually
// returns a string must fail the run instead of silently flowing a
// string downstream as if it were the declared type.
func TestTaskNodeOutputContractViolationFailsTheRun(t *testing.T) {
	skipIfNoPython3(t)
	e := newTaskEngine(t)
	digest := e.bundle(t, "def run():\n    return 'not-a-number'\n")
	_, execErr := e.runPipelineWithOutputInterface(t, "p-task-output-violation", digest, map[string]interface{}{"kind": "int64"})
	if execErr == nil {
		t.Fatal("a task returning a string against a declared int64 output ran to success")
	}
	if !errors.Is(execErr, ErrTaskOutputContractViolation) {
		t.Fatalf("error does not wrap ErrTaskOutputContractViolation: %v", execErr)
	}
	if !strings.Contains(execErr.Error(), "output") || !strings.Contains(execErr.Error(), "int64") {
		t.Fatalf("failure does not name the direction and expected type: %s", execErr)
	}
	if strings.Contains(execErr.Error(), "not-a-number") {
		t.Fatalf("failure leaked the observed value instead of a redacted kind/shape summary: %s", execErr)
	}
}

// The same declared contract, satisfied, must not change the happy path
// -- validation is an added check, not a new transform.
func TestTaskNodeOutputSatisfyingItsContractRunsNormally(t *testing.T) {
	skipIfNoPython3(t)
	e := newTaskEngine(t)
	digest := e.bundle(t, "def run():\n    return 42\n")
	run, execErr := e.runPipelineWithOutputInterface(t, "p-task-output-ok", digest, map[string]interface{}{"kind": "int64"})
	if execErr != nil {
		t.Fatalf("run returned error: %v", execErr)
	}
	row := e.firstTaskRow(t, run)
	if got := toF64(row["result"]); got != 42 {
		t.Fatalf("task output result = %v (type %T), want 42", row["result"], row["result"])
	}
}

func skipIfNoNode(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH")
	}
}

// ADR-033 phase 4a: the second required reference adapter. A task bundle
// declaring only a "node" payload runs end to end through the real
// engine and a real node subprocess -- the same pipeline shape, store,
// dispatch path and result contract a python task uses, with only the
// adapter differing.
func TestTaskNodeRunsANodePayloadEndToEnd(t *testing.T) {
	skipIfNoNode(t)
	e := newTaskEngine(t)
	digest := e.nodeBundle(t, "export function run() {\n  return 42;\n}\n")
	run, err := e.runPipeline(t, "p-task-node", digest, nil)
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	row := e.firstTaskRow(t, run)
	if got := toF64(row["result"]); got != 42 {
		t.Fatalf("task output result = %v (type %T), want 42", row["result"], row["result"])
	}
}

// Run parameters reach a node task as ONE object argument, the forced
// difference from Python's func(**kwargs) -- proving the kwargs
// convention each adapter documents is the one actually delivered.
func TestTaskNodeNodePayloadReceivesRunParametersAsAnObject(t *testing.T) {
	skipIfNoNode(t)
	e := newTaskEngine(t)
	digest := e.nodeBundle(t, "export function run({ name }) {\n  return `hello ${name}`;\n}\n")
	run, err := e.runPipeline(t, "p-task-node-kwargs", digest, map[string]string{"name": "world"})
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	row := e.firstTaskRow(t, run)
	if got := row["result"]; got != "hello world" {
		t.Fatalf("task output result = %v, want %q", got, "hello world")
	}
}

// A node task's throw maps into the same failure taxonomy a python
// task's does -- the taxonomy belongs to the protocol, not to either
// adapter.
func TestTaskNodeNodePayloadThrowingFailsTheRunWithUserCode(t *testing.T) {
	skipIfNoNode(t)
	e := newTaskEngine(t)
	digest := e.nodeBundle(t, "export function run() {\n  throw new Error('node boom');\n}\n")
	_, execErr := e.runPipeline(t, "p-task-node-raises", digest, nil)
	if execErr == nil {
		t.Fatal("a throwing node task ran to success")
	}
	if !strings.Contains(execErr.Error(), "user_code") || !strings.Contains(execErr.Error(), "node boom") {
		t.Fatalf("failure does not name the user_code category and message: %s", execErr)
	}
}

// Phase 3b's output-contract validation is adapter-independent: the same
// declared int64 output rejects a node task's string return exactly as
// it rejects a python one.
func TestTaskNodeNodePayloadOutputContractIsEnforced(t *testing.T) {
	skipIfNoNode(t)
	e := newTaskEngine(t)
	digest := e.nodeBundle(t, "export function run() {\n  return 'not-a-number';\n}\n")
	_, execErr := e.runPipelineWithOutputInterface(t, "p-task-node-contract", digest, map[string]interface{}{"kind": "int64"})
	if execErr == nil {
		t.Fatal("a node task violating its declared output contract ran to success")
	}
	if !errors.Is(execErr, ErrTaskOutputContractViolation) {
		t.Fatalf("error does not wrap ErrTaskOutputContractViolation: %v", execErr)
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
	if _, err := readTaskResult(resultPath, digest, nil); err == nil || !strings.Contains(err.Error(), "not yet supported") {
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
	if _, err := readTaskResult(resultPath, digest, nil); err == nil {
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
