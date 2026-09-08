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

// An async generator is the streaming shape a Node task author reaches
// for, and the adapter iterates it into the same NDJSON a plain array
// produces. Proven here rather than through the engine: this is adapter
// behavior, and the engine package already runs close to its CI timeout
// (Tnsor-Labs/brokoli#329), so spending a full pipeline run to assert an
// adapter detail is a cost worth not paying.
func TestOfflineEndToEnd_AsyncGeneratorBecomesADataset(t *testing.T) {
	root, module := buildFixtureBundle(t, "export async function* run() {\n  yield { n: 1 };\n  yield { n: 2 };\n}\n")

	node := nodeBinary(t)
	harnessDir := t.TempDir()
	harnessPath, err := Materialize(harnessDir)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	attemptDir := t.TempDir()
	resultPath := filepath.Join(attemptDir, "result.json")
	stagingDir := filepath.Join(attemptDir, "out")
	invocationPath := filepath.Join(attemptDir, "invocation.json")
	if err := WriteInvocation(invocationPath, Invocation{
		ModuleRoots:     []string{root},
		Module:          module,
		Symbol:          "run",
		InterfaceDigest: "sha256:" + strings.Repeat("0", 62) + "aa",
		OutputKind:      "dataset",
	}); err != nil {
		t.Fatalf("WriteInvocation: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := taskharness.Run(ctx, taskharness.NewStartFrame(invocationPath, resultPath, stagingDir),
		taskharness.Options{Command: Command(node, harnessPath, 0)}, taskharness.Handlers{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Failure != nil {
		t.Fatalf("expected success, got failure: %+v", res.Failure)
	}

	staged, err := os.ReadFile(filepath.Join(stagingDir, "result.ndjson"))
	if err != nil {
		t.Fatalf("read staged dataset: %v", err)
	}
	if got := strings.Count(strings.TrimSpace(string(staged)), "\n") + 1; got != 2 {
		t.Errorf("staged %d NDJSON rows, want 2:\n%s", got, staged)
	}

	raw, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"kind":"dataset"`) || !strings.Contains(string(raw), `"codec":"ndjson/v1"`) {
		t.Errorf("result manifest does not describe a dataset: %s", raw)
	}
}

// runFixtureWithInput is runFixture plus a staged NDJSON input file, so
// the harness's readInputRows actually runs.
func runFixtureWithInput(t *testing.T, root, module, symbol, ndjson string) (taskharness.Result, string) {
	t.Helper()
	node := nodeBinary(t)

	harnessDir := t.TempDir()
	harnessPath, err := Materialize(harnessDir)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	attemptDir := t.TempDir()
	inputPath := filepath.Join(attemptDir, "input.ndjson")
	if err := os.WriteFile(inputPath, []byte(ndjson), 0o600); err != nil {
		t.Fatalf("stage input: %v", err)
	}
	resultPath := filepath.Join(attemptDir, "result.json")
	invocationPath := filepath.Join(attemptDir, "invocation.json")
	digest := "sha256:" + strings.Repeat("0", 62) + "aa"
	if err := WriteInvocation(invocationPath, Invocation{
		ModuleRoots:     []string{root},
		Module:          module,
		Symbol:          symbol,
		InterfaceDigest: digest,
		InputPath:       inputPath,
		InputCodec:      "ndjson/v1",
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
		t.Fatalf("harness run: %v", err)
	}
	return res, resultPath
}

// brokoli#492/#496. JavaScript's number type cannot hold
// 9007199254740993, so JSON.parse silently returns ...992 -- the same
// class of corruption #479 fixed in Go. This adapter first refused such
// input (#494) and now carries it exactly, as a BigInt.
//
// Asserted on the RAW result bytes, never a decoded number: in
// JavaScript `v === 9007199254740993` is true after the value has been
// altered, because the comparison literal rounds identically.
func TestOfflineEndToEnd_SixtyFourBitIntegerSurvivesExactly(t *testing.T) {
	root, module := buildFixtureBundle(t, "export function run({ input }) {\n  return input[0].id;\n}\n")
	res, resultPath := runFixtureWithInput(t, root, module, "run", "{\"id\":9007199254740993}\n")
	if res.Failure != nil {
		t.Fatalf("expected success, got %s: %s", res.Failure.Category, res.Failure.Message)
	}
	raw, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if !strings.Contains(string(raw), "9007199254740993") {
		t.Fatalf("the exact value did not survive: %s", raw)
	}
	if strings.Contains(string(raw), "9007199254740992") {
		t.Fatalf("value altered by one -- the #479 corruption: %s", raw)
	}
}

// The int64 boundary itself, in both directions: these are the extremes
// a 64-bit id can actually take, and they must emit as bare integer
// literals rather than quoted strings or floats.
func TestOfflineEndToEnd_Int64BoundsSurvive(t *testing.T) {
	root, module := buildFixtureBundle(t, "export function run({ input }) {\n  return input[0];\n}\n")
	res, resultPath := runFixtureWithInput(t, root, module, "run",
		"{\"max\":9223372036854775807,\"min\":-9223372036854775808}\n")
	if res.Failure != nil {
		t.Fatalf("expected success, got %s: %s", res.Failure.Category, res.Failure.Message)
	}
	raw, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	for _, want := range []string{"9223372036854775807", "-9223372036854775808"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("%s did not survive: %s", want, raw)
		}
		// A bare literal, not a quoted string: a downstream reader must
		// see a number, or the fix has only moved the corruption.
		if strings.Contains(string(raw), "\""+want+"\"") {
			t.Errorf("%s came back quoted as a string: %s", want, raw)
		}
	}
}

// The refusal must be narrow: ordinary integers, fractions and digits
// inside strings are all still fine, or the guard would break every
// Node task that handles numbers.
func TestOfflineEndToEnd_RepresentableInputIsUnaffected(t *testing.T) {
	root, module := buildFixtureBundle(t, "export function run(rows) {\n  return rows;\n}\n")
	res, _ := runFixtureWithInput(t, root, module, "run",
		"{\"small\":42,\"max_safe\":9007199254740991,\"frac\":0.5,\"exp\":1e300,\"s\":\"9007199254740993\"}\n")
	if res.Failure != nil {
		t.Fatalf("representable input was refused: %s: %s", res.Failure.Category, res.Failure.Message)
	}
}
