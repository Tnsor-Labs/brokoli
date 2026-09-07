package nodeharness

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/taskbundlev2"
	"github.com/Tnsor-Labs/brokoli/pkg/taskharness"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func nodeBinary(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	return path
}

func TestMaterializeWritesAnExecutableScript(t *testing.T) {
	dir := t.TempDir()
	path, err := Materialize(dir)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read materialized harness: %v", err)
	}
	if !strings.Contains(string(data), "brokoli.task-runtime/v1") {
		t.Error("materialized harness.mjs doesn't look like the reference harness")
	}
}

func TestWriteInvocationRejectsMissingModuleOrSymbol(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invocation.json")
	if err := WriteInvocation(path, Invocation{Symbol: "run", InterfaceDigest: "sha256:" + strings.Repeat("0", 64)}); err == nil {
		t.Fatal("expected a missing Module to be rejected")
	}
	if err := WriteInvocation(path, Invocation{Module: "tasks", InterfaceDigest: "sha256:" + strings.Repeat("0", 64)}); err == nil {
		t.Fatal("expected a missing Symbol to be rejected")
	}
}

// The memory ceiling is the one place the two reference adapters
// genuinely differ (harness.py self-applies RLIMIT_AS; a JS harness
// cannot), so the V8 flag has to actually reach the argv.
func TestCommandAppliesMemoryCeilingAsV8Flag(t *testing.T) {
	got := Command("/usr/bin/node", "/tmp/harness.mjs", 256)
	want := []string{"/usr/bin/node", "--max-old-space-size=256", "/tmp/harness.mjs"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("Command() = %v, want %v", got, want)
	}
	if bare := Command("/usr/bin/node", "/tmp/harness.mjs", 0); len(bare) != 2 {
		t.Errorf("Command() with no ceiling = %v, want the bare two-element argv", bare)
	}
}

// buildFixtureBundle assembles a real task-bundle/v2 archive around one
// small ES module and extracts it with pkg/taskbundlev2, returning the
// extracted root -- the same shape a real dispatch produces from an
// uploaded bundle.
func buildFixtureBundle(t *testing.T, moduleSource string) (extractedRoot, moduleName string) {
	t.Helper()
	placeholderDigest := "sha256:" + strings.Repeat("0", 62) + "aa"
	archive, err := taskbundlev2.Assemble(
		map[string]string{"fixture_task.mjs": moduleSource},
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
	dest := t.TempDir()
	if _, err := taskbundlev2.Extract(archive, dest); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	return dest, "fixture_task"
}

func runFixture(t *testing.T, root, module, symbol string, kwargs map[string]interface{}) (taskharness.Result, string) {
	t.Helper()
	node := nodeBinary(t)

	harnessDir := t.TempDir()
	harnessPath, err := Materialize(harnessDir)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	attemptDir := t.TempDir()
	resultPath := filepath.Join(attemptDir, "result.json")
	invocationPath := filepath.Join(attemptDir, "invocation.json")
	digest := "sha256:" + strings.Repeat("0", 62) + "aa"
	if err := WriteInvocation(invocationPath, Invocation{
		ModuleRoots:     []string{root},
		Module:          module,
		Symbol:          symbol,
		Kwargs:          kwargs,
		InterfaceDigest: digest,
	}); err != nil {
		t.Fatalf("WriteInvocation: %v", err)
	}

	start := taskharness.NewStartFrame(invocationPath, resultPath, filepath.Join(attemptDir, "out"))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := taskharness.Run(ctx, start, taskharness.Options{
		Command: Command(node, harnessPath, 0),
	}, taskharness.Handlers{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res, resultPath
}

func TestOfflineEndToEnd_TaskBundleThroughHarnessToResult(t *testing.T) {
	root, module := buildFixtureBundle(t, "export function run({ x }) {\n  return x * 2;\n}\n")
	res, resultPath := runFixture(t, root, module, "run", map[string]interface{}{"x": float64(21)})
	if res.Failure != nil {
		t.Fatalf("expected success, got failure: %+v", res.Failure)
	}

	resultRaw, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(resultRaw))
	if err != nil {
		t.Fatalf("reparse result for schema validation: %v", err)
	}
	sch, err := jsonschema.NewCompiler().Compile("../../../docs/schema/task-result-v1.json")
	if err != nil {
		t.Fatalf("compile task-result-v1.json: %v", err)
	}
	if err := sch.Validate(inst); err != nil {
		t.Errorf("harness-produced result rejected by task-result-v1.json:\n%v", err)
	}

	var got struct {
		Contract string `json:"contract"`
		Outputs  struct {
			Result struct {
				Kind  string  `json:"kind"`
				Value float64 `json:"value"`
			} `json:"result"`
		} `json:"outputs"`
	}
	if err := json.Unmarshal(resultRaw, &got); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if got.Contract != "brokoli.task-result/v1" {
		t.Errorf("contract = %q, want brokoli.task-result/v1", got.Contract)
	}
	if got.Outputs.Result.Value != 42 {
		t.Errorf("result value = %v, want 42", got.Outputs.Result.Value)
	}
}

// An async task function is the one behavior this adapter must support
// that its Python counterpart has no equivalent for -- harness.mjs
// awaits the result, so a promise must never reach the result manifest.
func TestOfflineEndToEnd_AsyncTaskIsAwaited(t *testing.T) {
	root, module := buildFixtureBundle(t, "export async function run() {\n  return 7;\n}\n")
	res, resultPath := runFixture(t, root, module, "run", nil)
	if res.Failure != nil {
		t.Fatalf("expected success, got failure: %+v", res.Failure)
	}
	raw, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if !strings.Contains(string(raw), `"value":7`) {
		t.Errorf("async task result = %s, want the awaited value 7", raw)
	}
}

func TestOfflineEndToEnd_TaskThrowingReportsUserCodeFailure(t *testing.T) {
	root, module := buildFixtureBundle(t, "export function run() {\n  throw new Error('boom');\n}\n")
	res, _ := runFixture(t, root, module, "run", nil)
	if res.Failure == nil {
		t.Fatal("expected a failure, got success")
	}
	if res.Failure.Category != taskharness.FailureUserCode {
		t.Errorf("category = %q, want %q", res.Failure.Category, taskharness.FailureUserCode)
	}
	if !strings.Contains(res.Failure.Message, "boom") {
		t.Errorf("message = %q, want it to carry the thrown error", res.Failure.Message)
	}
}

// A missing export is the worker handing this harness something wrong,
// not the task failing -- contract_violation, never user_code (ADR-033
// section 14).
func TestOfflineEndToEnd_MissingSymbolIsAContractViolation(t *testing.T) {
	root, module := buildFixtureBundle(t, "export function run() {\n  return 1;\n}\n")
	res, _ := runFixture(t, root, module, "nope", nil)
	if res.Failure == nil {
		t.Fatal("expected a failure, got success")
	}
	if res.Failure.Category != taskharness.FailureContractViolation {
		t.Errorf("category = %q, want %q", res.Failure.Category, taskharness.FailureContractViolation)
	}
}

// An unresolvable module names every path it tried, rather than
// surfacing Node's own resolver error naming only the last attempt.
func TestOfflineEndToEnd_UnresolvableModuleNamesWhatItTried(t *testing.T) {
	root, _ := buildFixtureBundle(t, "export function run() {\n  return 1;\n}\n")
	res, _ := runFixture(t, root, "no_such_module", "run", nil)
	if res.Failure == nil {
		t.Fatal("expected a failure, got success")
	}
	if res.Failure.Category != taskharness.FailureContractViolation {
		t.Errorf("category = %q, want %q", res.Failure.Category, taskharness.FailureContractViolation)
	}
	if !strings.Contains(res.Failure.Message, "no_such_module.mjs") {
		t.Errorf("message = %q, want it to name the paths tried", res.Failure.Message)
	}
}
