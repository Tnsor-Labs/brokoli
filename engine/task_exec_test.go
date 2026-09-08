package engine

// End-to-end 'task' node execution (ADR-033 rollout phase 2b, issue
// #439 step 5) through the real engine: a task node with a real
// task-bundle/v2 archive runs through Runner.runTask,
// pkg/taskharness+pyharness, and a real python3, and its scalar output
// becomes the node's DataSet the same way every other node type's does.

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/taskbundlev2"
	"github.com/Tnsor-Labs/brokoli/pkg/taskharness/jvmharness"
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

// An unrecognised output kind is refused by name. All four kinds the
// contract defines -- scalar, dataset, artifact, collection -- now have
// readers, so this guards a candidate claiming something outside the
// enum rather than a not-yet-built branch.
func TestReadTaskResult_UnknownOutputKindIsRefusedByName(t *testing.T) {
	digest := "sha256:" + strings.Repeat("0", 62) + "aa"
	dir := t.TempDir()
	resultPath := writeTestResult(t, dir, `{
		"contract": "brokoli.task-result/v1",
		"interface_digest": "`+digest+`",
		"outputs": {"result": {"kind": "hologram"}}
	}`)
	_, err := readTaskResult(context.Background(), nil, "run-1", resultPath, dir, digest, nil)
	if err == nil || !strings.Contains(err.Error(), "hologram") {
		t.Fatalf("expected the unrecognised kind named, got: %v", err)
	}
}

// With nowhere to put the bytes there is no reference to return, and
// inlining the content would defeat the artifact kind entirely -- so
// this refuses by name rather than falling back silently.
func TestReadTaskResult_ArtifactWithoutABlobStoreIsRefusedByName(t *testing.T) {
	digest := "sha256:" + strings.Repeat("0", 62) + "aa"
	dir := t.TempDir()
	resultPath := writeTestResult(t, dir, `{
		"contract": "brokoli.task-result/v1",
		"interface_digest": "`+digest+`",
		"outputs": {"result": {"kind": "artifact", "path": "out.bin", "codec": "application/pdf", "size_bytes": 3, "checksum": "sha256:`+strings.Repeat("0", 64)+`"}}
	}`)
	_, err := readTaskResult(context.Background(), nil, "run-1", resultPath, dir, digest, nil)
	if err == nil || !strings.Contains(err.Error(), "no artifact blob store") {
		t.Fatalf("err = %v, want a named refusal about the missing blob store", err)
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
	if _, err := readTaskResult(context.Background(), nil, "run-1", resultPath, dir, digest, nil); err == nil {
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

// ADR-032 section 10: "Input validation occurs before user code
// observes a value." A producer emitting rows the consumer's declared
// input type refuses must fail the run at the boundary -- the consuming
// task should never start, so the error names the contract instead of
// surfacing as a confusing failure inside a task that did nothing wrong.
func TestCrossLanguage_InputViolatingTheConsumersContractFailsAtTheBoundary(t *testing.T) {
	skipIfNoPython3(t)
	e := newTaskEngine(t)
	producer := e.bundle(t, "def run():\n    return [{'n': 'not-an-int'}]\n")
	// The consumer would happily run -- it is the CONTRACT that refuses.
	consumer := e.bundle(t, "def run(input):\n    return [{'n': r['n']} for r in input]\n")

	datasetPort := map[string]interface{}{"value": map[string]interface{}{"kind": "dataset"}}
	typedInput := map[string]interface{}{"value": map[string]interface{}{
		"kind": "dataset",
		"row": map[string]interface{}{
			"kind":   "record",
			"fields": []interface{}{map[string]interface{}{"name": "n", "type": map[string]interface{}{"kind": "int64"}, "required": true}},
		},
	}}
	pipeline := &models.Pipeline{
		ID: "p-xlang-bad-input", Name: "p-xlang-bad-input", Enabled: true, OrgID: taskOrg,
		Nodes: []models.Node{
			{ID: "producer", Type: models.NodeTypeTask, Name: "Producer", Config: map[string]interface{}{
				"task_bundle": map[string]interface{}{"digest": producer, "format": taskbundlev2.Format},
			}, Interface: map[string]interface{}{
				"contract": "brokoli.task-interface/v1",
				"inputs":   map[string]interface{}{},
				"outputs":  map[string]interface{}{"result": datasetPort},
			}},
			{ID: "consumer", Type: models.NodeTypeTask, Name: "Consumer", Config: map[string]interface{}{
				"task_bundle": map[string]interface{}{"digest": consumer, "format": taskbundlev2.Format},
			}, Interface: map[string]interface{}{
				"contract": "brokoli.task-interface/v1",
				"inputs":   map[string]interface{}{"input": typedInput},
				"outputs":  map[string]interface{}{"result": datasetPort},
			}},
		},
		Edges: []models.Edge{{From: "producer", To: "consumer"}},
	}
	if err := e.s.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}

	_, execErr := e.eng.RunPipeline(pipeline.ID)
	if execErr == nil {
		t.Fatal("rows violating the consumer's declared input type were accepted")
	}
	if !errors.Is(execErr, ErrTaskInputContractViolation) {
		t.Fatalf("err = %v, want ErrTaskInputContractViolation", execErr)
	}
}

// The output half of ADR-032 section 10, symmetric with the input test
// above: a task producing rows its OWN declared output type refuses must
// fail before those rows are committed and flow downstream. Without this
// the boundary was one-sided -- inputs checked, outputs trusted.
func TestTaskNodeDatasetOutputRowsAreValidatedAgainstTheDeclaredRowType(t *testing.T) {
	skipIfNoPython3(t)
	e := newTaskEngine(t)
	digest := e.bundle(t, "def run():\n    return [{'n': 1}, {'n': 'not-an-int'}]\n")

	pipeline := &models.Pipeline{
		ID: "p-task-bad-output-rows", Name: "p-task-bad-output-rows", Enabled: true, OrgID: taskOrg,
		Nodes: []models.Node{
			{ID: "task", Type: models.NodeTypeTask, Name: "Task", Config: map[string]interface{}{
				"task_bundle": map[string]interface{}{"digest": digest, "format": taskbundlev2.Format},
			}, Interface: map[string]interface{}{
				"contract": "brokoli.task-interface/v1",
				"inputs":   map[string]interface{}{},
				"outputs": map[string]interface{}{"result": map[string]interface{}{
					"value": map[string]interface{}{
						"kind": "dataset",
						"row": map[string]interface{}{
							"kind":   "record",
							"fields": []interface{}{map[string]interface{}{"name": "n", "type": map[string]interface{}{"kind": "int64"}, "required": true}},
						},
					},
				}},
			}},
		},
	}
	if err := e.s.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}

	_, execErr := e.eng.RunPipeline(pipeline.ID)
	if execErr == nil {
		t.Fatal("a task emitting rows its declared output type refuses ran to success")
	}
	if !errors.Is(execErr, ErrTaskOutputContractViolation) {
		t.Fatalf("err = %v, want ErrTaskOutputContractViolation", execErr)
	}
	if !strings.Contains(execErr.Error(), "$[1]") {
		t.Errorf("err = %v, want the offending row index named", execErr)
	}
}

// runPipelineWithArtifactOutput declares the task's "result" port as an
// artifact, optionally constraining its media types.
func (e *taskTestEngine) runPipelineWithArtifactOutput(t *testing.T, id, digest string, mediaTypes []string) (*models.Run, error) {
	t.Helper()
	value := map[string]interface{}{"kind": "artifact"}
	if len(mediaTypes) > 0 {
		mt := make([]interface{}, 0, len(mediaTypes))
		for _, m := range mediaTypes {
			mt = append(mt, m)
		}
		value["media_types"] = mt
	}
	pipeline := &models.Pipeline{
		ID: id, Name: id, Enabled: true, OrgID: taskOrg,
		Nodes: []models.Node{
			{ID: "task", Type: models.NodeTypeTask, Name: "Task", Config: map[string]interface{}{
				"task_bundle": map[string]interface{}{"digest": digest, "format": taskbundlev2.Format},
			}, Interface: map[string]interface{}{
				"contract": "brokoli.task-interface/v1",
				"inputs":   map[string]interface{}{},
				"outputs":  map[string]interface{}{"result": map[string]interface{}{"value": value}},
			}},
		},
	}
	if err := e.s.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}
	return e.eng.RunPipeline(pipeline.ID)
}

// A task's opaque bytes become a REFERENCE, never inline content: the
// node's output is the same four-column uri/media_type/size/checksum row
// a source_api artifact response already produces, so downstream has one
// representation to understand rather than two.
func TestTaskNodeProducesAnArtifactOutput(t *testing.T) {
	skipIfNoPython3(t)
	e := newTaskEngine(t)
	digest := e.bundle(t, "def run():\n    return b'%PDF-1.4 fake bytes'\n")
	run, err := e.runPipelineWithArtifactOutput(t, "p-task-artifact", digest, []string{"application/pdf"})
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	ds, err := e.eng.ArtifactStore.ReadArtifact(run.ID, "task", "")
	if err != nil {
		t.Fatalf("read task artifact: %v", err)
	}
	if len(ds.Rows) != 1 {
		t.Fatalf("rows = %v, want one reference row", ds.Rows)
	}
	row := ds.Rows[0]
	if row["media_type"] != "application/pdf" {
		t.Errorf("media_type = %v, want application/pdf", row["media_type"])
	}
	if uri, _ := row["uri"].(string); uri == "" {
		t.Errorf("uri is empty; the bytes were not stored by reference: %v", row)
	}
	// The bytes themselves must NOT be inlined into the row.
	for _, v := range row {
		if s, ok := v.(string); ok && strings.Contains(s, "fake bytes") {
			t.Errorf("artifact content leaked into the output row: %v", row)
		}
	}
}

// Declaring an artifact and returning something that is not bytes is a
// contract violation with a precise message.
func TestTaskNodeArtifactOutputRejectsANonBytesReturn(t *testing.T) {
	skipIfNoPython3(t)
	e := newTaskEngine(t)
	digest := e.bundle(t, "def run():\n    return {'not': 'bytes'}\n")
	_, execErr := e.runPipelineWithArtifactOutput(t, "p-task-artifact-nonbytes", digest, nil)
	if execErr == nil {
		t.Fatal("a task declaring an artifact output but returning a dict ran to success")
	}
	if !strings.Contains(execErr.Error(), "artifact output") {
		t.Fatalf("failure does not explain the artifact contract violation: %s", execErr)
	}
}

// runPipelineWithCollectionOutput declares the task's "result" port as a
// collection of the given item kind.
func (e *taskTestEngine) runPipelineWithCollectionOutput(t *testing.T, id, digest string, itemValue map[string]interface{}) (*models.Run, error) {
	t.Helper()
	pipeline := &models.Pipeline{
		ID: id, Name: id, Enabled: true, OrgID: taskOrg,
		Nodes: []models.Node{
			{ID: "task", Type: models.NodeTypeTask, Name: "Task", Config: map[string]interface{}{
				"task_bundle": map[string]interface{}{"digest": digest, "format": taskbundlev2.Format},
			}, Interface: map[string]interface{}{
				"contract": "brokoli.task-interface/v1",
				"inputs":   map[string]interface{}{},
				"outputs": map[string]interface{}{"result": map[string]interface{}{
					"value": map[string]interface{}{
						"kind":     "collection",
						"items":    itemValue,
						"ordered":  true,
						"item_key": map[string]interface{}{"kind": "string"},
					},
				}},
			}},
		},
	}
	if err := e.s.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}
	return e.eng.RunPipeline(pipeline.ID)
}

// A collection becomes one row per item, each carrying its declared key
// so the items stay separately addressable downstream.
func TestTaskNodeProducesACollectionOfScalars(t *testing.T) {
	skipIfNoPython3(t)
	e := newTaskEngine(t)
	digest := e.bundle(t, "def run():\n    return {'alpha': 1, 'beta': 2}\n")
	run, err := e.runPipelineWithCollectionOutput(t, "p-task-coll-scalar", digest,
		map[string]interface{}{"kind": "scalar", "type": map[string]interface{}{"kind": "int64"}})
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	ds, err := e.eng.ArtifactStore.ReadArtifact(run.ID, "task", "")
	if err != nil {
		t.Fatalf("read task artifact: %v", err)
	}
	if len(ds.Rows) != 2 {
		t.Fatalf("rows = %v, want one row per item", ds.Rows)
	}
	keys := map[string]bool{}
	for _, r := range ds.Rows {
		k, _ := r[ItemKeyColumn].(string)
		keys[k] = true
		if _, hasValue := r["value"]; !hasValue {
			t.Errorf("row %v carries no value column", r)
		}
	}
	if !keys["alpha"] || !keys["beta"] {
		t.Errorf("item keys = %v, want alpha and beta", keys)
	}
}

// Items that are bytes become artifacts — each staged separately, each
// with its own checksum, each stored by reference exactly as a top-level
// artifact output is. There is no weaker path into the store just
// because the bytes arrived inside a collection.
func TestTaskNodeProducesACollectionOfArtifacts(t *testing.T) {
	skipIfNoPython3(t)
	e := newTaskEngine(t)
	digest := e.bundle(t, "def run():\n    return {'first': b'one bytes', 'second': b'two bytes'}\n")
	run, err := e.runPipelineWithCollectionOutput(t, "p-task-coll-artifact", digest,
		map[string]interface{}{"kind": "artifact"})
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	ds, err := e.eng.ArtifactStore.ReadArtifact(run.ID, "task", "")
	if err != nil {
		t.Fatalf("read task artifact: %v", err)
	}
	if len(ds.Rows) != 2 {
		t.Fatalf("rows = %v, want one reference row per item", ds.Rows)
	}
	for _, r := range ds.Rows {
		if uri, _ := r["uri"].(string); uri == "" {
			t.Errorf("row %v has no uri; the item was not stored by reference", r)
		}
		if k, _ := r[ItemKeyColumn].(string); k == "" {
			t.Errorf("row %v has no item key", r)
		}
		for _, v := range r {
			if s, ok := v.(string); ok && strings.Contains(s, "bytes") && !strings.HasPrefix(s, "local://") {
				t.Errorf("item content leaked into the row: %v", r)
			}
		}
	}
	// Distinct content must yield distinct references.
	if ds.Rows[0]["uri"] == ds.Rows[1]["uri"] {
		t.Errorf("both items share a uri (%v); distinct content must not collide", ds.Rows[0]["uri"])
	}
}

func skipIfNoJDK(t *testing.T) {
	t.Helper()
	if _, err := jvmharness.Resolve(); err != nil {
		t.Skipf("no usable JDK: %v", err)
	}
}

// jvmBundle compiles source to .class files and packages them as a
// one-payload task-bundle/v2 with runtime "jvm".
//
// Compiled here rather than shipped as source because a JVM bundle
// carries bytecode -- that is what makes it a different bundle shape
// from python's and node's, and testing against source would test
// something no real bundle looks like.
// jvmBundlePackaged compiles a package-qualified class and packages it
// under the package path a classloader expects (com/example/X.class),
// which is what a real build tool's output looks like.
func (e *taskTestEngine) jvmBundlePackaged(t *testing.T, pkg, simpleName, source string) string {
	t.Helper()
	tc, err := jvmharness.Resolve()
	if err != nil {
		t.Skipf("no usable JDK: %v", err)
	}
	work := t.TempDir()
	srcDir := filepath.Join(work, "src", filepath.FromSlash(strings.ReplaceAll(pkg, ".", "/")))
	if err := os.MkdirAll(srcDir, 0o750); err != nil {
		t.Fatal(err)
	}
	srcPath := filepath.Join(srcDir, simpleName+".java")
	if err := os.WriteFile(srcPath, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(work, "classes")
	if out, err := exec.Command(tc.Javac, "-d", outDir, srcPath).CombinedOutput(); err != nil { // #nosec G204 -- resolved toolchain
		t.Fatalf("compile fixture: %v\n%s", err, out)
	}
	rel := filepath.ToSlash(filepath.Join(strings.ReplaceAll(pkg, ".", "/"), simpleName+".class"))
	classBytes, err := os.ReadFile(filepath.Join(outDir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read compiled class: %v", err)
	}

	placeholderDigest := "sha256:" + strings.Repeat("0", 62) + "aa"
	archive, err := taskbundlev2.Assemble(
		map[string]string{rel: string(classBytes)},
		&taskbundlev2.Manifest{
			Format:          taskbundlev2.Format,
			Name:            "fixture-task",
			InterfaceDigest: placeholderDigest,
			SourceDigest:    placeholderDigest,
			Payloads: []taskbundlev2.Payload{{
				ID: "jvm-any", Runtime: taskbundlev2.RuntimeJVM, OS: "any", Arch: "any",
				Entrypoint:    taskbundlev2.Entrypoint{Module: pkg + "." + simpleName, Symbol: "run"},
				Effects:       taskbundlev2.EffectPure,
				PayloadDigest: placeholderDigest,
			}},
			Files: []taskbundlev2.FileEntry{{Path: rel, Size: int64(len(classBytes)), SHA256: fmt.Sprintf("%x", sha256.Sum256(classBytes))}},
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

func (e *taskTestEngine) jvmBundle(t *testing.T, className, source string) string {
	t.Helper()
	tc, err := jvmharness.Resolve()
	if err != nil {
		t.Skipf("no usable JDK: %v", err)
	}
	srcDir, outDir := t.TempDir(), t.TempDir()
	srcPath := filepath.Join(srcDir, className+".java")
	if err := os.WriteFile(srcPath, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(tc.Javac, "-d", outDir, srcPath).CombinedOutput(); err != nil { // #nosec G204 -- resolved toolchain, test fixture
		t.Fatalf("compile fixture: %v\n%s", err, out)
	}
	classBytes, err := os.ReadFile(filepath.Join(outDir, className+".class"))
	if err != nil {
		t.Fatalf("read compiled class: %v", err)
	}

	placeholderDigest := "sha256:" + strings.Repeat("0", 62) + "aa"
	archive, err := taskbundlev2.Assemble(
		map[string]string{className + ".class": string(classBytes)},
		&taskbundlev2.Manifest{
			Format:          taskbundlev2.Format,
			Name:            "fixture-task",
			InterfaceDigest: placeholderDigest,
			SourceDigest:    placeholderDigest,
			Payloads: []taskbundlev2.Payload{{
				ID:      "jvm-any",
				Runtime: taskbundlev2.RuntimeJVM,
				OS:      "any",
				Arch:    "any",
				// Module carries the fully-qualified class and Symbol the
				// static method: the managed-language entrypoint shape
				// task-bundle/v2 already defines fits the JVM as-is, so
				// no new manifest fields were needed.
				Entrypoint:    taskbundlev2.Entrypoint{Module: className, Symbol: "run"},
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

// ADR-036 phase 4: a task bundle declaring only a "jvm" payload runs end
// to end through the real engine and a real JVM -- the same pipeline
// shape, store, dispatch path and result contract a python or node task
// uses, with only the adapter differing.
//
// This is where ADR-033's claim that the scheduler contains no
// language-specific invocation code gets tested against a COMPILED
// language for the first time: python and node are both interpreted and
// dynamically typed, so if the claim were weaker than believed, adding
// this third runtime class is where it would show. It did not --
// prepareTaskHarness needed one new case and nothing else.
func TestTaskNodeRunsAJVMPayloadEndToEnd(t *testing.T) {
	skipIfNoJDK(t)
	e := newTaskEngine(t)
	digest := e.jvmBundle(t, "FixtureTask", `
public final class FixtureTask {
    public static Object run() { return 42L; }
}
`)
	run, err := e.runPipeline(t, "p-task-jvm", digest, nil)
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	row := e.firstTaskRow(t, run)
	if got := toF64(row["result"]); got != 42 {
		t.Fatalf("task output result = %v (type %T), want 42", row["result"], row["result"])
	}
}

// The JVM is advertised as a supported runtime class, or a bundle
// declaring only a jvm payload is refused before it ever runs.
func TestJVMIsASupportedTaskRuntime(t *testing.T) {
	for _, r := range supportedTaskRuntimes {
		if r == taskbundlev2.RuntimeJVM {
			return
		}
	}
	t.Fatal("jvm is not in supportedTaskRuntimes; SelectPayload would skip every jvm payload")
}

// The engine half of the 64-bit contract (#479, #492, #496): a task's
// 64-bit output must reach the pipeline as an exact int64, not a float64
// that happens to print the same.
//
// Asserted per adapter, because each reaches this point differently: the
// python harness emits an exact int natively, the node harness emits one
// through BigInt handling it has only just gained, and the JVM harness
// through a long. A regression in any one of them is a wrong id in a
// pipeline, which is why this is checked at the engine boundary rather
// than only inside each adapter's own tests.
func TestTaskSixtyFourBitOutputReachesThePipelineExactly(t *testing.T) {
	const want = int64(9007199254740993)

	t.Run("python", func(t *testing.T) {
		skipIfNoPython3(t)
		e := newTaskEngine(t)
		digest := e.bundle(t, "def run():\n    return 9007199254740993\n")
		assertExactID(t, e, "p-int64-py", digest, want)
	})

	t.Run("node", func(t *testing.T) {
		skipIfNoNode(t)
		e := newTaskEngine(t)
		digest := e.nodeBundle(t, "export function run() {\n  return 9007199254740993n;\n}\n")
		assertExactID(t, e, "p-int64-node", digest, want)
	})

	t.Run("jvm", func(t *testing.T) {
		skipIfNoJDK(t)
		e := newTaskEngine(t)
		digest := e.jvmBundle(t, "FixtureTask",
			"public final class FixtureTask {\n    public static Object run() { return 9007199254740993L; }\n}\n")
		assertExactID(t, e, "p-int64-jvm", digest, want)
	})
}

func assertExactID(t *testing.T, e *taskTestEngine, pipelineID, digest string, want int64) {
	t.Helper()
	run, err := e.runPipeline(t, pipelineID, digest, nil)
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	got := e.firstTaskRow(t, run)["result"]
	// Compared as int64, never via a float64 helper: 2^53+1 and 2^53 are
	// the same float64, so a float comparison passes on the corrupted
	// value. That is precisely how #479 stayed hidden.
	n, ok := got.(int64)
	if !ok {
		t.Fatalf("result = %#v (%T), want an int64 -- a float64 cannot hold %d", got, got, want)
	}
	if n != want {
		t.Fatalf("result = %d, want %d (off by %d)", n, want, want-n)
	}
}

// A task with a DECLARED int64 output port must validate against it, not
// merely round-trip.
//
// This is the case a UseNumber decode breaks if the numbers are not
// normalized straight afterwards: taskinterface's int64 validation
// accepts a tagged value, an int64 or a whole float64, and rejects a
// json.Number outright -- so the exact value would reach validation and
// be refused for being the right type in the wrong Go representation.
func TestTaskDeclaredInt64PortAcceptsAnExactValue(t *testing.T) {
	skipIfNoPython3(t)
	e := newTaskEngine(t)
	digest := e.bundle(t, "def run():\n    return 9007199254740993\n")
	run, err := e.runPipelineWithOutputInterface(t, "p-int64-port", digest,
		map[string]interface{}{"kind": "int64"})
	if err != nil {
		t.Fatalf("a declared int64 port refused an exact 64-bit value: %v", err)
	}
	got := e.firstTaskRow(t, run)["result"]
	n, ok := got.(int64)
	if !ok || n != 9007199254740993 {
		t.Fatalf("result = %#v (%T), want int64(9007199254740993)", got, got)
	}
}

// A package-qualified class -- what any real build tool emits -- must
// run, not just a default-package one. A classloader resolves
// com.example.Rollup at com/example/Rollup.class beneath the bundle
// root, so this is what proves the layout `brokoli bundle jvm` produces
// is the layout the runtime needs. Flattening the package path would
// still yield a well-formed bundle whose classes simply cannot be
// found, and nothing else would catch that.
func TestTaskRunsAPackageQualifiedJVMClass(t *testing.T) {
	skipIfNoJDK(t)
	e := newTaskEngine(t)
	digest := e.jvmBundlePackaged(t, "com.example", "Rollup",
		"package com.example;\npublic final class Rollup {\n    public static Object run() { return 9007199254740993L; }\n}\n")
	run, err := e.runPipeline(t, "p-jvm-packaged", digest, nil)
	if err != nil {
		t.Fatalf("a package-qualified JVM class failed to run: %v", err)
	}
	got := e.firstTaskRow(t, run)["result"]
	n, ok := got.(int64)
	if !ok || n != 9007199254740993 {
		t.Fatalf("result = %#v (%T), want int64(9007199254740993)", got, got)
	}
}
