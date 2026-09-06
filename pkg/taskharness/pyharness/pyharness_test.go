package pyharness

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

func sha256Hex(t *testing.T, content string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// buildGzipTar builds a gzipped tar archive from the given files, keyed
// by relative path -- a minimal, deterministic-enough archive builder
// for tests that don't need pkg/taskbundlev2's own full Assemble
// equivalent (which doesn't exist yet; the SDK/CLI is the production
// builder per ADR-033, this package only ever consumes bundles).
func buildGzipTar(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Size: int64(len(content)), Mode: 0o644}); err != nil {
			t.Fatalf("write header %s: %v", name, err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("write body %s: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}

func python3(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not on PATH")
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
		t.Error("materialized harness.py doesn't look like the reference harness")
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

// buildFixtureBundle constructs a real task-bundle/v2 archive around one
// small Python module, extracts it with pkg/taskbundlev2, and returns
// the extracted root plus the manifest's python payload -- the same
// shape a Phase 2b dispatch path will eventually produce from a real
// uploaded bundle.
func buildFixtureBundle(t *testing.T, moduleSource string) (extractedRoot string, moduleName string) {
	t.Helper()
	digest := "sha256:" + strings.Repeat("0", 62) + "aa"
	fileDigest := sha256Hex(t, moduleSource)
	m := &taskbundlev2.Manifest{
		Format:          taskbundlev2.Format,
		Name:            "fixture-task",
		InterfaceDigest: digest,
		SourceDigest:    digest,
		Payloads: []taskbundlev2.Payload{{
			ID:      "python-any",
			Runtime: taskbundlev2.RuntimePython,
			OS:      "any",
			Arch:    "any",
			Entrypoint: taskbundlev2.Entrypoint{
				Module: "fixture_task",
				Symbol: "run",
			},
			Effects:       taskbundlev2.EffectPure,
			PayloadDigest: digest,
		}},
		Files: []taskbundlev2.FileEntry{
			{Path: "fixture_task.py", Size: int64(len(moduleSource)), SHA256: fileDigest},
		},
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	archive := buildGzipTar(t, map[string]string{
		"manifest.json":   string(raw),
		"fixture_task.py": moduleSource,
	})
	dest := t.TempDir()
	if _, err := taskbundlev2.Extract(archive, dest); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	return dest, "fixture_task"
}

func TestOfflineEndToEnd_TaskBundleThroughHarnessToResult(t *testing.T) {
	py := python3(t)
	root, module := buildFixtureBundle(t, "def run(x):\n    return x * 2\n")

	harnessDir := t.TempDir()
	harnessPath, err := Materialize(harnessDir)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	attemptDir := t.TempDir()
	resultPath := filepath.Join(attemptDir, "result.json")
	outputStagingDir := filepath.Join(attemptDir, "out")
	invocationPath := filepath.Join(attemptDir, "invocation.json")
	digest := "sha256:" + strings.Repeat("0", 62) + "aa"
	if err := WriteInvocation(invocationPath, Invocation{
		SysPath:         []string{root},
		Module:          module,
		Symbol:          "run",
		Kwargs:          map[string]interface{}{"x": float64(21)},
		InterfaceDigest: digest,
	}); err != nil {
		t.Fatalf("WriteInvocation: %v", err)
	}

	start := taskharness.NewStartFrame(invocationPath, resultPath, outputStagingDir)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := taskharness.Run(ctx, start, taskharness.Options{
		Command: Command(py, harnessPath),
	}, taskharness.Handlers{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
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
		Contract        string `json:"contract"`
		InterfaceDigest string `json:"interface_digest"`
		Outputs         struct {
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

func TestOfflineEndToEnd_TaskRaisesReportsUserCodeFailure(t *testing.T) {
	py := python3(t)
	root, module := buildFixtureBundle(t, "def run():\n    raise ValueError('boom')\n")

	harnessDir := t.TempDir()
	harnessPath, err := Materialize(harnessDir)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	attemptDir := t.TempDir()
	digest := "sha256:" + strings.Repeat("0", 62) + "aa"
	invocationPath := filepath.Join(attemptDir, "invocation.json")
	if err := WriteInvocation(invocationPath, Invocation{
		SysPath:         []string{root},
		Module:          module,
		Symbol:          "run",
		InterfaceDigest: digest,
	}); err != nil {
		t.Fatalf("WriteInvocation: %v", err)
	}

	start := taskharness.NewStartFrame(invocationPath, filepath.Join(attemptDir, "result.json"), filepath.Join(attemptDir, "out"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := taskharness.Run(ctx, start, taskharness.Options{
		Command: Command(py, harnessPath),
	}, taskharness.Handlers{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Failure == nil {
		t.Fatal("expected a failure result")
	}
	if res.Failure.Category != taskharness.FailureUserCode {
		t.Errorf("Category = %q, want user_code", res.Failure.Category)
	}
	if !strings.Contains(res.Failure.Message, "boom") {
		t.Errorf("Message = %q, want it to contain the raised exception", res.Failure.Message)
	}
}
