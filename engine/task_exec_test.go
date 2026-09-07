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
// suite above) since the reference harnesses only ever emit the kinds
// the engine asked them for -- there is no way to drive an
// "artifact"-kind candidate through a real run without an adapter that
// produces one, which no phase has built.
//
// "dataset" WAS in this list until phase 5a gave it a reader; what
// remains are the two kinds that still have none, each needing its own
// reference-handling contract rather than a decoder.
func TestReadTaskResult_ArtifactAndCollectionKindsAreNotYetSupported(t *testing.T) {
	digest := "sha256:" + strings.Repeat("0", 62) + "aa"
	for _, kind := range []string{"artifact", "collection"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			resultPath := writeTestResult(t, dir, `{
				"contract": "brokoli.task-result/v1",
				"interface_digest": "`+digest+`",
				"outputs": {"result": {"kind": "`+kind+`", "path": "out.bin"}}
			}`)
			_, err := readTaskResult(resultPath, dir, digest, nil)
			if err == nil || !strings.Contains(err.Error(), "not yet supported") {
				t.Fatalf("expected a clear not-yet-supported error for %q, got: %v", kind, err)
			}
		})
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
	if _, err := readTaskResult(resultPath, dir, digest, nil); err == nil {
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

// Capability matching is AND-superset, so it cannot express "python OR
// node". A bundle offering a CHOICE of runtimes therefore gets only the
// protocol tag: naming either class would wrongly exclude a worker that
// has the other one and could have run the bundle fine.
func TestTaskRuntimeCapabilities_RuntimeChoiceGetsOnlyTheProtocolTag(t *testing.T) {
	pythonOnly := &taskbundlev2.Manifest{Payloads: []taskbundlev2.Payload{
		{ID: "p", Runtime: taskbundlev2.RuntimePython},
	}}
	if got := taskRuntimeCapabilities(pythonOnly); len(got) != 2 || got[1] != taskRuntimeCapabilityFor(taskbundlev2.RuntimePython) {
		t.Errorf("single-runtime bundle caps = %v, want the protocol tag plus the python tag", got)
	}

	mixed := &taskbundlev2.Manifest{Payloads: []taskbundlev2.Payload{
		{ID: "p", Runtime: taskbundlev2.RuntimePython},
		{ID: "n", Runtime: taskbundlev2.RuntimeNode},
	}}
	if got := taskRuntimeCapabilities(mixed); len(got) != 1 || got[0] != taskRuntimeCapability {
		t.Errorf("multi-runtime bundle caps = %v, want only the bare protocol tag", got)
	}

	if got := taskRuntimeCapabilities(nil); len(got) != 1 || got[0] != taskRuntimeCapability {
		t.Errorf("nil manifest caps = %v, want only the bare protocol tag", got)
	}
}

// runPipelineWithDatasetOutput declares the task's "result" port as a
// dataset, which is what tells the harness to serialize returned rows to
// a staging file instead of inlining one scalar (ADR-033 phase 5a).
func (e *taskTestEngine) runPipelineWithDatasetOutput(t *testing.T, id, digest string) (*models.Run, error) {
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
						"value": map[string]interface{}{"kind": "dataset"},
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

// The data-plane payoff: a task node can finally produce ROWS, not just
// one scalar, so downstream nodes have real data to consume.
func TestTaskNodeProducesADatasetOutput_Python(t *testing.T) {
	skipIfNoPython3(t)
	e := newTaskEngine(t)
	digest := e.bundle(t, "def run():\n    return [{'id': 1, 'name': 'a'}, {'id': 2, 'name': 'b'}]\n")
	run, err := e.runPipelineWithDatasetOutput(t, "p-task-dataset-py", digest)
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	ds, err := e.eng.ArtifactStore.ReadArtifact(run.ID, "task", "")
	if err != nil {
		t.Fatalf("read task artifact: %v", err)
	}
	if len(ds.Rows) != 2 {
		t.Fatalf("rows = %v, want 2", ds.Rows)
	}
	if toF64(ds.Rows[1]["id"]) != 2 || ds.Rows[0]["name"] != "a" {
		t.Errorf("rows decoded wrong: %v", ds.Rows)
	}
	if strings.Join(ds.Columns, ",") != "id,name" {
		t.Errorf("columns = %v, want [id name]", ds.Columns)
	}
}

// The same contract, the same result, through the other adapter -- the
// portable data boundary is the protocol's, not either language's.
func TestTaskNodeProducesADatasetOutput_Node(t *testing.T) {
	skipIfNoNode(t)
	e := newTaskEngine(t)
	digest := e.nodeBundle(t, "export function run() {\n  return [{ id: 1, name: 'a' }, { id: 2, name: 'b' }];\n}\n")
	run, err := e.runPipelineWithDatasetOutput(t, "p-task-dataset-node", digest)
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	ds, err := e.eng.ArtifactStore.ReadArtifact(run.ID, "task", "")
	if err != nil {
		t.Fatalf("read task artifact: %v", err)
	}
	if len(ds.Rows) != 2 {
		t.Fatalf("rows = %v, want 2", ds.Rows)
	}
	if toF64(ds.Rows[1]["id"]) != 2 || ds.Rows[0]["name"] != "a" {
		t.Errorf("rows decoded wrong: %v", ds.Rows)
	}
}

// A Node task may stream rows -- refusing an async generator would make
// the adapter worse than the language it wraps.
func TestTaskNodeProducesADatasetOutput_NodeAsyncGenerator(t *testing.T) {
	skipIfNoNode(t)
	e := newTaskEngine(t)
	digest := e.nodeBundle(t, "export async function* run() {\n  yield { id: 1 };\n  yield { id: 2 };\n  yield { id: 3 };\n}\n")
	run, err := e.runPipelineWithDatasetOutput(t, "p-task-dataset-node-stream", digest)
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	ds, err := e.eng.ArtifactStore.ReadArtifact(run.ID, "task", "")
	if err != nil {
		t.Fatalf("read task artifact: %v", err)
	}
	if len(ds.Rows) != 3 {
		t.Fatalf("rows = %v, want 3 streamed rows", ds.Rows)
	}
}

// Declaring a dataset and returning something that is not rows is a
// contract violation with a precise message, not a confusing
// serialization error.
func TestTaskNodeDatasetOutputRejectsANonRowReturn(t *testing.T) {
	skipIfNoPython3(t)
	e := newTaskEngine(t)
	digest := e.bundle(t, "def run():\n    return 42\n")
	_, execErr := e.runPipelineWithDatasetOutput(t, "p-task-dataset-bad", digest)
	if execErr == nil {
		t.Fatal("a task declaring a dataset output but returning a scalar ran to success")
	}
	if !strings.Contains(execErr.Error(), "dataset output") {
		t.Fatalf("failure does not explain the dataset contract violation: %s", execErr)
	}
}

// crossLanguagePipeline wires a producing task to a consuming task:
// producer declares a dataset output, consumer declares a dataset input
// AND output, so the consumer stops being source-capable and may
// receive the edge (engine/validate.go's nodeIsSourceCapable).
func (e *taskTestEngine) crossLanguagePipeline(t *testing.T, id, producerDigest, consumerDigest string) (*models.Run, error) {
	t.Helper()
	datasetPort := map[string]interface{}{
		"value": map[string]interface{}{"kind": "dataset"},
	}
	pipeline := &models.Pipeline{
		ID: id, Name: id, Enabled: true, OrgID: taskOrg,
		Nodes: []models.Node{
			{ID: "producer", Type: models.NodeTypeTask, Name: "Producer", Config: map[string]interface{}{
				"task_bundle": map[string]interface{}{"digest": producerDigest, "format": taskbundlev2.Format},
			}, Interface: map[string]interface{}{
				"contract": "brokoli.task-interface/v1",
				"inputs":   map[string]interface{}{},
				"outputs":  map[string]interface{}{"result": datasetPort},
			}},
			{ID: "consumer", Type: models.NodeTypeTask, Name: "Consumer", Config: map[string]interface{}{
				"task_bundle": map[string]interface{}{"digest": consumerDigest, "format": taskbundlev2.Format},
			}, Interface: map[string]interface{}{
				"contract": "brokoli.task-interface/v1",
				"inputs":   map[string]interface{}{"input": datasetPort},
				"outputs":  map[string]interface{}{"result": datasetPort},
			}},
		},
		Edges: []models.Edge{{From: "producer", To: "consumer"}},
	}
	if err := e.s.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}
	return e.eng.RunPipeline(pipeline.ID)
}

// ADR-033's own acceptance gate: "one Python task feeds a Node task and
// one Node task feeds a Python task through the same pinned backend."
// This is the first direction.
func TestCrossLanguage_PythonTaskFeedsNodeTask(t *testing.T) {
	skipIfNoPython3(t)
	skipIfNoNode(t)
	e := newTaskEngine(t)
	producer := e.bundle(t, "def run():\n    return [{'n': 1}, {'n': 2}, {'n': 3}]\n")
	consumer := e.nodeBundle(t, "export function run({ input }) {\n  return input.map((r) => ({ n: r.n, doubled: r.n * 2 }));\n}\n")

	run, err := e.crossLanguagePipeline(t, "p-xlang-py-to-node", producer, consumer)
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	ds, err := e.eng.ArtifactStore.ReadArtifact(run.ID, "consumer", "")
	if err != nil {
		t.Fatalf("read consumer artifact: %v", err)
	}
	if len(ds.Rows) != 3 {
		t.Fatalf("consumer rows = %v, want the producer's 3 rows transformed", ds.Rows)
	}
	if toF64(ds.Rows[2]["doubled"]) != 6 {
		t.Errorf("row 2 = %v, want doubled=6", ds.Rows[2])
	}
}

// The other direction, which is the half that proves the boundary is the
// protocol's rather than one language's serialization habits.
func TestCrossLanguage_NodeTaskFeedsPythonTask(t *testing.T) {
	skipIfNoPython3(t)
	skipIfNoNode(t)
	e := newTaskEngine(t)
	producer := e.nodeBundle(t, "export function run() {\n  return [{ n: 10 }, { n: 20 }];\n}\n")
	consumer := e.bundle(t, "def run(input):\n    return [{'n': r['n'], 'halved': r['n'] / 2} for r in input]\n")

	run, err := e.crossLanguagePipeline(t, "p-xlang-node-to-py", producer, consumer)
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	ds, err := e.eng.ArtifactStore.ReadArtifact(run.ID, "consumer", "")
	if err != nil {
		t.Fatalf("read consumer artifact: %v", err)
	}
	if len(ds.Rows) != 2 {
		t.Fatalf("consumer rows = %v, want 2", ds.Rows)
	}
	if toF64(ds.Rows[1]["halved"]) != 10 {
		t.Errorf("row 1 = %v, want halved=10", ds.Rows[1])
	}
}

// A task declaring an input port is a consumer, not a source, so an edge
// into it must validate -- the rule that previously made this shape
// impossible keyed on node TYPE rather than declared contract.
func TestTaskNodeDeclaringAnInputPortMayReceiveEdges(t *testing.T) {
	consumer := models.Node{ID: "c", Type: models.NodeTypeTask, Interface: map[string]interface{}{
		"contract": "brokoli.task-interface/v1",
		"inputs":   map[string]interface{}{"input": map[string]interface{}{"value": map[string]interface{}{"kind": "dataset"}}},
		"outputs":  map[string]interface{}{},
	}}
	if nodeIsSourceCapable(consumer, nil) {
		t.Error("a task declaring an input port is still treated as a source, so edges into it are rejected")
	}

	// ...while a task that declares nothing stays a source, since
	// absence is honest and nothing said it consumes anything.
	bare := models.Node{ID: "b", Type: models.NodeTypeTask}
	if !nodeIsSourceCapable(bare, nil) {
		t.Error("a task with no declared input stopped being source-capable, which breaks single-task pipelines")
	}
}
