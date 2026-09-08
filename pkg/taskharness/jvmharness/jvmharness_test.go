package jvmharness

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/Tnsor-Labs/brokoli/pkg/taskharness"
)

// A JDK is resolved, never provisioned (ADR-026), so a host without one
// skips rather than fails -- the same shape skipIfNoNode and python3 use
// for the other two adapters.
func toolchain(t *testing.T) *Toolchain {
	t.Helper()
	tc, err := Resolve()
	if err != nil {
		t.Skipf("no usable JDK: %v", err)
	}
	return tc
}

func TestResolveReportsTheFeatureVersion(t *testing.T) {
	tc := toolchain(t)
	if tc.Version < MinJavaVersion {
		t.Fatalf("Resolve returned a toolchain below its own floor: %d", tc.Version)
	}
	if tc.Java == "" || tc.Javac == "" {
		t.Errorf("Resolve returned an incomplete toolchain: %+v", tc)
	}
	if tc.VersionString == "" {
		t.Error("VersionString is empty; a resolved-execution record could not name the build")
	}
}

// The three refusals are distinguished on purpose -- "no java" and "java
// but no javac" call for different fixes, and a JRE-only host must not
// be sent looking for something it already has.
func TestResolveRefusalsAreDistinct(t *testing.T) {
	empty := t.TempDir()
	t.Setenv("PATH", empty)
	_, err := Resolve()
	if err == nil {
		t.Fatal("Resolve succeeded with an empty PATH")
	}
	if !strings.Contains(err.Error(), "no 'java' on PATH") {
		t.Errorf("err = %v, want it to name the missing java", err)
	}

	// A host with java but no javac: the JRE-only case ADR-036 records as
	// the real cost of compiling rather than source-launching.
	tcReal, resolveErr := Resolve0(t)
	if resolveErr != nil {
		t.Skipf("no usable JDK to build the JRE-only fixture from: %v", resolveErr)
	}
	jreDir := t.TempDir()
	if err := os.Symlink(tcReal.Java, filepath.Join(jreDir, "java")); err != nil {
		t.Skipf("cannot build a JRE-only fixture here: %v", err)
	}
	t.Setenv("PATH", jreDir)
	_, err = Resolve()
	if err == nil {
		t.Fatal("Resolve succeeded on a JRE-only PATH")
	}
	if !strings.Contains(err.Error(), "javac") {
		t.Errorf("err = %v, want it to name the missing javac specifically", err)
	}
}

// Resolve0 resolves against the real PATH before a test rewrites it.
func Resolve0(t *testing.T) (*Toolchain, error) {
	t.Helper()
	return Resolve()
}

func TestParseEntrypoint(t *testing.T) {
	class, method, err := ParseEntrypoint("com.example.tasks.Transforms#dailyRollup")
	if err != nil {
		t.Fatalf("ParseEntrypoint: %v", err)
	}
	if class != "com.example.tasks.Transforms" || method != "dailyRollup" {
		t.Errorf("got (%q, %q)", class, method)
	}
	// '#' is the separator precisely because a dot would collide with
	// package separators -- these must all be refused, not split wrongly.
	for _, bad := range []string{"", "NoSeparator", "a#b#c", "#method", "Class#", "com.example.Class.method"} {
		if _, _, err := ParseEntrypoint(bad); err == nil {
			t.Errorf("ParseEntrypoint(%q) was accepted", bad)
		}
	}
}

func TestCommandAppliesMemoryCeilingAsXmx(t *testing.T) {
	argv := Command("/usr/bin/java", "/classes", 512)
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "-Xmx512m") {
		t.Errorf("argv = %v, want it to carry -Xmx512m", argv)
	}
	if !strings.Contains(joined, "-cp /classes Harness") {
		t.Errorf("argv = %v, want it to launch Harness off the class dir", argv)
	}
	// Zero must leave the JVM's own default alone rather than passing
	// -Xmx0m, which would refuse to start.
	if strings.Contains(strings.Join(Command("/usr/bin/java", "/classes", 0), " "), "-Xmx") {
		t.Error("a zero ceiling still emitted -Xmx")
	}
}

// The digest is half the cache key, so it must change when the sources
// do -- otherwise a harness bump silently reuses the old classes.
func TestSourceDigestIsStable(t *testing.T) {
	a, err := SourceDigest()
	if err != nil {
		t.Fatalf("SourceDigest: %v", err)
	}
	b, err := SourceDigest()
	if err != nil {
		t.Fatalf("SourceDigest: %v", err)
	}
	if a != b || a == "" {
		t.Fatalf("digest is not stable: %q vs %q", a, b)
	}
}

// The whole reason this adapter compiles instead of source-launching:
// the ~1.6s javac cost must be paid once per (source, JDK), not once per
// attempt. Asserted by timing, because "it caches" is exactly the kind
// of claim that quietly stops being true.
func TestMaterializeCompilesOnceAndReuses(t *testing.T) {
	tc := toolchain(t)
	cacheRoot := t.TempDir()

	start := time.Now()
	first, err := Materialize(cacheRoot, tc)
	if err != nil {
		t.Fatalf("Materialize (cold): %v", err)
	}
	cold := time.Since(start)

	start = time.Now()
	second, err := Materialize(cacheRoot, tc)
	if err != nil {
		t.Fatalf("Materialize (warm): %v", err)
	}
	warm := time.Since(start)

	if first != second {
		t.Fatalf("a second call produced a different class dir: %q vs %q", first, second)
	}
	if _, err := os.Stat(filepath.Join(first, "Harness.class")); err != nil {
		t.Fatalf("compiled harness is missing: %v", err)
	}
	if warm > cold/4 {
		t.Errorf("warm Materialize took %v against a cold %v -- it looks like it recompiled", warm, cold)
	}
}

// A directory that exists but holds no classes must not read as "already
// compiled": an interrupted build or a half-removed cache would
// otherwise launch a JVM against nothing.
func TestMaterializeRebuildsWhenTheMarkerIsMissing(t *testing.T) {
	tc := toolchain(t)
	cacheRoot := t.TempDir()
	dir, err := Materialize(cacheRoot, tc)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, ".complete")); err != nil {
		t.Fatalf("remove marker: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "Harness.class")); err != nil {
		t.Fatalf("remove class: %v", err)
	}
	if _, err := Materialize(cacheRoot, tc); err != nil {
		t.Fatalf("Materialize did not recover from an incomplete cache dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "Harness.class")); err != nil {
		t.Fatalf("harness was not rebuilt: %v", err)
	}
}

// buildTaskClass compiles a one-class fixture task and returns its
// classpath root.
func buildTaskClass(t *testing.T, tc *Toolchain, source string) string {
	t.Helper()
	srcDir := t.TempDir()
	outDir := t.TempDir()
	path := filepath.Join(srcDir, "FixtureTask.java")
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	out, err := runCmd(tc.Javac, "-d", outDir, path)
	if err != nil {
		t.Fatalf("compile fixture: %v\n%s", err, out)
	}
	return outDir
}

func runFixture(t *testing.T, tc *Toolchain, classpath, className, methodName string, kwargs map[string]interface{}) (taskharness.Result, string) {
	t.Helper()
	return runFixtureFull(t, tc, classpath, className, methodName, kwargs, Invocation{})
}

// runFixtureFull is runFixture with the declared output kind, media type
// and staged input the engine would supply.
func runFixtureFull(t *testing.T, tc *Toolchain, classpath, className, methodName string, kwargs map[string]interface{}, extra Invocation) (taskharness.Result, string) {
	t.Helper()
	classDir, err := Materialize(t.TempDir(), tc)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	attemptDir := t.TempDir()
	resultPath := filepath.Join(attemptDir, "result.json")
	invocationPath := filepath.Join(attemptDir, "invocation.json")
	digest := "sha256:" + strings.Repeat("0", 62) + "aa"
	if err := WriteInvocation(invocationPath, Invocation{
		Classpath:       []string{classpath},
		ClassName:       className,
		MethodName:      methodName,
		Kwargs:          kwargs,
		InterfaceDigest: digest,
		OutputKind:      extra.OutputKind,
		OutputMediaType: extra.OutputMediaType,
		InputPath:       extra.InputPath,
		InputCodec:      extra.InputCodec,
	}); err != nil {
		t.Fatalf("WriteInvocation: %v", err)
	}

	start := taskharness.NewStartFrame(invocationPath, resultPath, filepath.Join(attemptDir, "out"))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := taskharness.Run(ctx, start, taskharness.Options{
		Command: Command(tc.Java, classDir, 0),
	}, taskharness.Handlers{})
	if err != nil {
		t.Fatalf("harness run: %v", err)
	}
	return res, resultPath
}

// The phase-1 end-to-end proof: a real bundle-shaped class, compiled and
// invoked through the real protocol client, producing a real
// task-result-v1 candidate. Nothing in the engine is involved -- the
// same "prove the mechanism standalone first" discipline phases 0, 1 and
// 2a used.
func TestOfflineEndToEnd_StaticMethodThroughHarnessToResult(t *testing.T) {
	tc := toolchain(t)
	cp := buildTaskClass(t, tc, `
public final class FixtureTask {
    public static Object run(java.util.Map<String, Object> kwargs) {
        return ((Number) kwargs.get("x")).longValue() * 2;
    }
}
`)
	res, resultPath := runFixture(t, tc, cp, "FixtureTask", "run", map[string]interface{}{"x": 21})
	if res.Failure != nil {
		t.Fatalf("expected success, got failure: %+v", res.Failure)
	}

	raw, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	var doc struct {
		Contract        string `json:"contract"`
		InterfaceDigest string `json:"interface_digest"`
		Outputs         map[string]struct {
			Kind  string      `json:"kind"`
			Value interface{} `json:"value"`
		} `json:"outputs"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("result is not valid JSON: %v\n%s", err, raw)
	}
	if doc.Contract != "brokoli.task-result/v1" {
		t.Errorf("contract = %q", doc.Contract)
	}
	if got := doc.Outputs["result"]; got.Kind != "scalar" {
		t.Errorf("output kind = %q, want scalar", got.Kind)
	}
	if !strings.Contains(string(raw), "42") {
		t.Errorf("result does not carry 42: %s", raw)
	}
}

// A 64-bit integer must survive the whole round trip. ADR-036 pins this
// as a hard gate because #479 lost exactly this value in Go and #492
// found the same class live in the Node adapter, which cannot represent
// it at all. Java's long can, and this is what proves it does.
func TestOfflineEndToEnd_SixtyFourBitIntegerSurvives(t *testing.T) {
	tc := toolchain(t)
	cp := buildTaskClass(t, tc, `
public final class FixtureTask {
    public static Object run(java.util.Map<String, Object> kwargs) {
        return kwargs.get("id");
    }
}
`)
	res, resultPath := runFixture(t, tc, cp, "FixtureTask", "run", map[string]interface{}{"id": int64(9007199254740993)})
	if res.Failure != nil {
		t.Fatalf("expected success, got failure: %+v", res.Failure)
	}
	raw, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	// Text, not a decoded number: a numeric comparison is the trap this
	// bug class hides behind (see #492).
	if !strings.Contains(string(raw), "9007199254740993") {
		t.Fatalf("the exact value did not survive: %s", raw)
	}
	if strings.Contains(string(raw), "9007199254740992") {
		t.Fatalf("value altered by one -- the #479 corruption in the jvm adapter: %s", raw)
	}
}

// A missing entrypoint is the worker handing this harness something
// wrong, not the task failing -- contract_violation, never user_code
// (ADR-033 section 14). And it must be refused rather than inferred:
// scanning for "the only public method" would make adding a second one a
// silent behaviour change.
func TestOfflineEndToEnd_MissingMethodIsAContractViolation(t *testing.T) {
	tc := toolchain(t)
	cp := buildTaskClass(t, tc, `
public final class FixtureTask {
    public static Object run(java.util.Map<String, Object> kwargs) { return 1; }
}
`)
	res, _ := runFixture(t, tc, cp, "FixtureTask", "nope", nil)
	if res.Failure == nil {
		t.Fatal("expected a failure, got success")
	}
	if res.Failure.Category != taskharness.FailureContractViolation {
		t.Errorf("category = %q, want %q", res.Failure.Category, taskharness.FailureContractViolation)
	}
}

func TestOfflineEndToEnd_MissingClassNamesTheClasspath(t *testing.T) {
	tc := toolchain(t)
	cp := buildTaskClass(t, tc, `
public final class FixtureTask {
    public static Object run(java.util.Map<String, Object> kwargs) { return 1; }
}
`)
	res, _ := runFixture(t, tc, cp, "com.example.NotThere", "run", nil)
	if res.Failure == nil {
		t.Fatal("expected a failure, got success")
	}
	if res.Failure.Category != taskharness.FailureContractViolation {
		t.Errorf("category = %q, want %q", res.Failure.Category, taskharness.FailureContractViolation)
	}
	if !strings.Contains(res.Failure.Message, "com.example.NotThere") {
		t.Errorf("message = %q, want it to name the class that was not found", res.Failure.Message)
	}
}

// A throwing task is user_code, and the author needs the cause -- not
// the reflection wrapper around it.
func TestOfflineEndToEnd_ThrowingTaskReportsUserCode(t *testing.T) {
	tc := toolchain(t)
	cp := buildTaskClass(t, tc, `
public final class FixtureTask {
    public static Object run(java.util.Map<String, Object> kwargs) {
        throw new IllegalStateException("boom");
    }
}
`)
	res, _ := runFixture(t, tc, cp, "FixtureTask", "run", nil)
	if res.Failure == nil {
		t.Fatal("expected a failure, got success")
	}
	if res.Failure.Category != taskharness.FailureUserCode {
		t.Errorf("category = %q, want %q", res.Failure.Category, taskharness.FailureUserCode)
	}
	if !strings.Contains(res.Failure.Message, "boom") {
		t.Errorf("message = %q, want the thrown cause, not the reflection wrapper", res.Failure.Message)
	}
}

// A task taking nothing needs no ceremony.
func TestOfflineEndToEnd_NoArgMethodIsAccepted(t *testing.T) {
	tc := toolchain(t)
	cp := buildTaskClass(t, tc, `
public final class FixtureTask {
    public static Object run() { return "hello"; }
}
`)
	res, resultPath := runFixture(t, tc, cp, "FixtureTask", "run", nil)
	if res.Failure != nil {
		t.Fatalf("expected success, got failure: %+v", res.Failure)
	}
	raw, _ := os.ReadFile(resultPath)
	if !strings.Contains(string(raw), "hello") {
		t.Errorf("result does not carry the returned value: %s", raw)
	}
}

func runCmd(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput() // #nosec G204 -- test fixture compilation
	return string(out), err
}

// stageInput writes an NDJSON input file and returns its path.
func stageInput(t *testing.T, ndjson string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "input.ndjson")
	if err := os.WriteFile(path, []byte(ndjson), 0o600); err != nil {
		t.Fatalf("stage input: %v", err)
	}
	return path
}

func readResult(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("result is not valid JSON: %v\n%s", err, raw)
	}
	return doc
}

func outputPort(t *testing.T, doc map[string]interface{}) map[string]interface{} {
	t.Helper()
	outs, ok := doc["outputs"].(map[string]interface{})
	if !ok {
		t.Fatalf("result has no outputs object: %#v", doc)
	}
	port, ok := outs["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("result has no 'result' port: %#v", outs)
	}
	return port
}

// Staged rows reach the task under the port's own name, and a 64-bit id
// survives the read -- the difference #492 records between this adapter
// and the Node one, which cannot represent one at all.
func TestOfflineEndToEnd_InputRowsReachTheTask(t *testing.T) {
	tc := toolchain(t)
	cp := buildTaskClass(t, tc, `
import java.util.*;
public final class FixtureTask {
    @SuppressWarnings("unchecked")
    public static Object run(Map<String, Object> kwargs) {
        List<Object> rows = (List<Object>) kwargs.get("input");
        Map<String, Object> first = (Map<String, Object>) rows.get(0);
        return first.get("id");
    }
}
`)
	res, resultPath := runFixtureFull(t, tc, cp, "FixtureTask", "run", nil, Invocation{
		InputPath:  stageInput(t, "{\"id\":9007199254740993}\n"),
		InputCodec: "ndjson/v1",
	})
	if res.Failure != nil {
		t.Fatalf("expected success, got failure: %+v", res.Failure)
	}
	raw, _ := os.ReadFile(resultPath)
	if !strings.Contains(string(raw), "9007199254740993") {
		t.Fatalf("the exact 64-bit id did not survive the input read: %s", raw)
	}
}

func TestOfflineEndToEnd_DatasetOutput(t *testing.T) {
	tc := toolchain(t)
	cp := buildTaskClass(t, tc, `
import java.util.*;
public final class FixtureTask {
    public static Object run() {
        List<Object> rows = new ArrayList<>();
        Map<String, Object> r = new LinkedHashMap<>();
        r.put("id", 7L);
        rows.add(r);
        return rows;
    }
}
`)
	res, resultPath := runFixtureFull(t, tc, cp, "FixtureTask", "run", nil, Invocation{OutputKind: "dataset"})
	if res.Failure != nil {
		t.Fatalf("expected success, got failure: %+v", res.Failure)
	}
	port := outputPort(t, readResult(t, resultPath))
	if port["kind"] != "dataset" {
		t.Fatalf("kind = %v, want dataset", port["kind"])
	}
	if port["codec"] != "ndjson/v1" {
		t.Errorf("codec = %v, want ndjson/v1", port["codec"])
	}
	// Size and checksum must describe the bytes actually written, since
	// the worker verifies against them (ADR-033 section 7 rule 6).
	if sum, _ := port["checksum"].(string); !strings.HasPrefix(sum, "sha256:") {
		t.Errorf("checksum = %v, want a sha256 reference", port["checksum"])
	}
	if n, _ := port["size_bytes"].(float64); n <= 0 {
		t.Errorf("size_bytes = %v, want the real byte count", port["size_bytes"])
	}
}

func TestOfflineEndToEnd_ArtifactOutput(t *testing.T) {
	tc := toolchain(t)
	cp := buildTaskClass(t, tc, `
import java.util.*;
public final class FixtureTask {
    public static Object run() { return "hello artifact"; }
}
`)
	res, resultPath := runFixtureFull(t, tc, cp, "FixtureTask", "run", nil,
		Invocation{OutputKind: "artifact", OutputMediaType: "text/plain"})
	if res.Failure != nil {
		t.Fatalf("expected success, got failure: %+v", res.Failure)
	}
	port := outputPort(t, readResult(t, resultPath))
	if port["kind"] != "artifact" {
		t.Fatalf("kind = %v, want artifact", port["kind"])
	}
	// An artifact states its media type in codec, since the manifest has
	// no media_type field (see the engine's artifactMediaType).
	if port["codec"] != "text/plain" {
		t.Errorf("codec = %v, want the declared media type", port["codec"])
	}
	if n, _ := port["size_bytes"].(float64); int(n) != len("hello artifact") {
		t.Errorf("size_bytes = %v, want %d", port["size_bytes"], len("hello artifact"))
	}
}

func TestOfflineEndToEnd_CollectionOutputCarriesItemKeys(t *testing.T) {
	tc := toolchain(t)
	cp := buildTaskClass(t, tc, `
import java.util.*;
public final class FixtureTask {
    public static Object run() {
        Map<String, Object> items = new LinkedHashMap<>();
        items.put("alpha", 1L);
        items.put("beta", "two");
        return items;
    }
}
`)
	res, resultPath := runFixtureFull(t, tc, cp, "FixtureTask", "run", nil, Invocation{OutputKind: "collection"})
	if res.Failure != nil {
		t.Fatalf("expected success, got failure: %+v", res.Failure)
	}
	port := outputPort(t, readResult(t, resultPath))
	if port["kind"] != "collection" {
		t.Fatalf("kind = %v, want collection", port["kind"])
	}
	items, _ := port["items"].([]interface{})
	if len(items) != 2 {
		t.Fatalf("items = %#v, want two", items)
	}
	// The key is what makes an item separately addressable (ADR-032
	// section 6) -- required, never derived from position, since
	// positional identity is exactly what a key replaces.
	for _, it := range items {
		m, _ := it.(map[string]interface{})
		if k, _ := m["item_key"].(string); k == "" {
			t.Errorf("item has no item_key: %#v", m)
		}
	}
}

// The DECLARED interface is authoritative, never the returned value's
// runtime shape: a task declaring a dataset that returns a String is a
// contract violation, not an artifact.
func TestOfflineEndToEnd_DeclaredKindIsAuthoritative(t *testing.T) {
	tc := toolchain(t)
	cp := buildTaskClass(t, tc, `
public final class FixtureTask {
    public static Object run() { return "not a dataset"; }
}
`)
	res, _ := runFixtureFull(t, tc, cp, "FixtureTask", "run", nil, Invocation{OutputKind: "dataset"})
	if res.Failure == nil {
		t.Fatal("a String was accepted for a declared dataset output")
	}
	if res.Failure.Category != taskharness.FailureContractViolation {
		t.Errorf("category = %q, want %q", res.Failure.Category, taskharness.FailureContractViolation)
	}
	if !strings.Contains(res.Failure.Message, "dataset") {
		t.Errorf("message = %q, want it to name the declared kind", res.Failure.Message)
	}
}

// Every frame this harness emits must validate against the protocol
// schema. Without this the adapter could speak a dialect that happens to
// work with today's client and breaks on the next one.
func TestFramesValidateAgainstTheProtocolSchema(t *testing.T) {
	tc := toolchain(t)
	cp := buildTaskClass(t, tc, `
public final class FixtureTask {
    public static Object run() { return 1L; }
}
`)
	classDir, err := Materialize(t.TempDir(), tc)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	attemptDir := t.TempDir()
	invocationPath := filepath.Join(attemptDir, "invocation.json")
	if err := WriteInvocation(invocationPath, Invocation{
		Classpath: []string{cp}, ClassName: "FixtureTask", MethodName: "run",
		InterfaceDigest: "sha256:" + strings.Repeat("0", 62) + "aa",
	}); err != nil {
		t.Fatalf("WriteInvocation: %v", err)
	}
	start := taskharness.NewStartFrame(invocationPath, filepath.Join(attemptDir, "result.json"), filepath.Join(attemptDir, "out"))
	startJSON, err := json.Marshal(start)
	if err != nil {
		t.Fatal(err)
	}

	// Drive the harness directly so the raw frame bytes are observable,
	// rather than through the client which decodes them away.
	cmd := exec.Command(Command(tc.Java, classDir, 0)[0], Command(tc.Java, classDir, 0)[1:]...) // #nosec G204 -- resolved toolchain
	cmd.Stdin = strings.NewReader(string(startJSON) + "\n")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("harness run: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least ready and a terminal frame, got: %q", out)
	}
	c := jsonschema.NewCompiler()
	schema, err := c.Compile("../../../docs/schema/task-runtime-v1.json")
	if err != nil {
		t.Fatalf("compile task-runtime-v1.json: %v", err)
	}
	for i, line := range lines {
		inst, err := jsonschema.UnmarshalJSON(strings.NewReader(line))
		if err != nil {
			t.Fatalf("frame %d is not valid JSON: %v\n%s", i, err, line)
		}
		if err := schema.Validate(inst); err != nil {
			t.Errorf("frame %d does not validate against task-runtime-v1.json: %v\n%s", i, err, line)
		}
	}
	if !strings.Contains(lines[0], "\"ready\"") {
		t.Errorf("first frame is not ready: %s", lines[0])
	}
	if !strings.Contains(lines[len(lines)-1], "\"completed\"") {
		t.Errorf("last frame is not completed: %s", lines[len(lines)-1])
	}
}

// groovyToolchain finds groovyc and the Groovy runtime jar a compiled
// Groovy class needs at run time, or skips.
func groovyToolchain(t *testing.T) (groovyc, runtimeJar string) {
	t.Helper()
	groovyc, err := exec.LookPath("groovyc")
	if err != nil {
		t.Skip("groovyc not on PATH")
	}
	for _, pattern := range []string{
		"/usr/share/groovy/lib/groovy-*.jar",
		"/usr/share/java/groovy*.jar",
	} {
		if matches, _ := filepath.Glob(pattern); len(matches) > 0 {
			return groovyc, matches[0]
		}
	}
	t.Skip("groovy runtime jar not found")
	return "", ""
}

// The adapter loads BYTECODE, not Java source -- so every JVM language
// is supported by construction, which is most of why supporting the JVM
// is worth doing at all. Asserted with a real second language rather
// than claimed: ADR-036 lists Kotlin and Scala as "should work through
// the same adapter unchanged", and "should" is not "verified".
//
// The case also pins the constraint that makes multi-language bundles
// work: a compiled Groovy class needs groovy/lang/GroovyObject at run
// time, so its runtime jar must travel in the bundle. That is precisely
// what Classpath's derivation from the manifest's own file list
// delivers -- without it, every non-Java JVM language would fail with
// NoClassDefFoundError.
func TestOfflineEndToEnd_AGroovyTaskRunsThroughTheSameAdapter(t *testing.T) {
	tc := toolchain(t)
	groovyc, runtimeJar := groovyToolchain(t)

	srcDir, outDir := t.TempDir(), t.TempDir()
	src := filepath.Join(srcDir, "FixtureTask.groovy")
	if err := os.WriteFile(src, []byte("class FixtureTask {\n    static Object run() { return 9007199254740993L }\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(groovyc, "-d", outDir, src).CombinedOutput(); err != nil { // #nosec G204 -- resolved tool, test fixture
		t.Fatalf("groovyc: %v\n%s", err, out)
	}

	classDir, err := Materialize(t.TempDir(), tc)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	attemptDir := t.TempDir()
	resultPath := filepath.Join(attemptDir, "result.json")
	invocationPath := filepath.Join(attemptDir, "invocation.json")
	if err := WriteInvocation(invocationPath, Invocation{
		// The compiled class AND the language runtime, exactly as
		// Classpath would derive them from a bundle carrying both.
		Classpath:       []string{outDir, runtimeJar},
		ClassName:       "FixtureTask",
		MethodName:      "run",
		InterfaceDigest: "sha256:" + strings.Repeat("0", 62) + "aa",
	}); err != nil {
		t.Fatalf("WriteInvocation: %v", err)
	}

	start := taskharness.NewStartFrame(invocationPath, resultPath, filepath.Join(attemptDir, "out"))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := taskharness.Run(ctx, start, taskharness.Options{
		Command: Command(tc.Java, classDir, 0),
	}, taskharness.Handlers{})
	if err != nil {
		t.Fatalf("harness run: %v", err)
	}
	if res.Failure != nil {
		t.Fatalf("a Groovy task failed: %s: %s", res.Failure.Category, res.Failure.Message)
	}
	raw, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	// And 64-bit fidelity holds for a non-Java JVM language too.
	if !strings.Contains(string(raw), "9007199254740993") {
		t.Fatalf("the exact value did not survive from Groovy: %s", raw)
	}
}

// Without the language's runtime jar on the classpath, the class cannot
// load at all -- named clearly rather than surfacing a raw
// NoClassDefFoundError with no hint about what is missing.
func TestOfflineEndToEnd_AJVMLanguageWithoutItsRuntimeIsAContractViolation(t *testing.T) {
	tc := toolchain(t)
	groovyc, _ := groovyToolchain(t)

	srcDir, outDir := t.TempDir(), t.TempDir()
	src := filepath.Join(srcDir, "FixtureTask.groovy")
	if err := os.WriteFile(src, []byte("class FixtureTask {\n    static Object run() { return 1L }\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(groovyc, "-d", outDir, src).CombinedOutput(); err != nil { // #nosec G204 -- resolved tool, test fixture
		t.Fatalf("groovyc: %v\n%s", err, out)
	}

	res, _ := runFixture(t, tc, outDir, "FixtureTask", "run", nil)
	if res.Failure == nil {
		t.Fatal("a Groovy class loaded without its runtime jar")
	}
	if res.Failure.Category != taskharness.FailureContractViolation {
		t.Errorf("category = %q, want %q", res.Failure.Category, taskharness.FailureContractViolation)
	}
}
