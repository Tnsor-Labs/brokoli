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
