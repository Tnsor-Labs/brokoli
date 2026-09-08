// Package jvmharness is the JVM reference implementation of the
// brokoli.task-runtime/v1 harness side (ADR-036; ADR-033 sections 3
// and 7).
//
// It differs from pyharness and nodeharness in the one way ADR-036 is
// about: the JVM is compiled. Both existing adapters embed harness
// source and hand it to an interpreter the host already has. Java 11's
// JEP 330 would allow the same shape (`java Harness.java`), and
// measurement rejected it -- 1.07-1.16s per launch against 0.07s
// precompiled, on a runtime whose task nodes get a fresh child per
// attempt (ADR-035). So this package embeds .java SOURCE, compiles it
// ONCE per (source, JDK) pair into a cache, and launches the compiled
// classes thereafter.
//
// ADR-026 carries over unchanged: no JDK is ever downloaded. The
// toolchain is resolved, what was resolved is recorded, and a host
// without one is refused BY NAME rather than discovered mid-run.
//
// Phase 1 (this package's current scope) covers toolchain resolution,
// the compile cache, Command, and the invocation descriptor. The
// harness's full frame vocabulary and the non-scalar output kinds are
// phase 2.
package jvmharness

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

//go:embed harness/*.java
var harnessFS embed.FS

// Adapter/AdapterVersion mirror Harness.java's own constants exactly,
// kept independently here (rather than parsed out of the embedded file)
// so a caller resolving an execution environment digest (ADR-033
// section 4) can name the adapter without invoking a JVM first. The two
// must be bumped together -- the same hand-kept arrangement pyharness
// and nodeharness already rely on.
const (
	Adapter        = "brokoli-jvm-taskharness"
	AdapterVersion = "0.1.0"
)

// MinJavaVersion is the floor ADR-036 sets: the oldest release still
// under broad vendor support and the oldest all current build tooling
// targets by default. A resolved JDK below it is refused by name.
const MinJavaVersion = 17

// Toolchain is a resolved JDK: the two binaries and the feature version
// they reported.
//
// Both binaries, not just java. This adapter compiles, so a JRE-only
// host cannot run it -- ADR-036 records that as the real cost of
// choosing compile-once over source-launch, and Resolve is where a host
// finds out, at validation time rather than mid-run.
type Toolchain struct {
	Java    string
	Javac   string
	Version int
	// VersionString is what the toolchain reported verbatim, recorded
	// rather than reconstructed so a resolved-execution record names the
	// exact build.
	VersionString string
}

// Resolve finds a usable JDK on PATH, or explains precisely which part
// is missing.
//
// The three failure modes are distinguished on purpose. "No java at all"
// and "java but no javac" call for different fixes -- installing a
// runtime versus installing a full JDK -- and collapsing them into one
// message would send a JRE-only host looking for something it already
// has.
func Resolve() (*Toolchain, error) {
	javaPath, err := exec.LookPath("java")
	if err != nil {
		return nil, fmt.Errorf("jvmharness: no 'java' on PATH; a JVM task needs a JDK %d or newer (Brokoli never downloads one, see ADR-026)", MinJavaVersion)
	}
	javacPath, err := exec.LookPath("javac")
	if err != nil {
		return nil, fmt.Errorf("jvmharness: found 'java' at %s but no 'javac'; this adapter compiles its harness once and so needs a full JDK, not a JRE (ADR-036)", javaPath)
	}

	versionString, feature, err := javaFeatureVersion(javaPath)
	if err != nil {
		return nil, err
	}
	if feature < MinJavaVersion {
		return nil, fmt.Errorf("jvmharness: resolved java %s (feature version %d) at %s, below the Java %d floor this adapter requires (ADR-036)", versionString, feature, javaPath, MinJavaVersion)
	}
	return &Toolchain{Java: javaPath, Javac: javacPath, Version: feature, VersionString: versionString}, nil
}

// javaVersionRe matches the quoted version in `java -version` output,
// which every vendor prints on the first line as e.g.
//
//	openjdk version "24.0.1" 2025-04-15
var javaVersionRe = regexp.MustCompile(`version "([^"]+)"`)

func javaFeatureVersion(javaPath string) (string, int, error) {
	// -version writes to stderr on every JDK, including modern ones.
	out, err := exec.Command(javaPath, "-version").CombinedOutput() // #nosec G204 -- javaPath came from exec.LookPath, not from user input
	if err != nil {
		return "", 0, fmt.Errorf("jvmharness: %s -version failed: %w", javaPath, err)
	}
	m := javaVersionRe.FindStringSubmatch(string(out))
	if m == nil {
		return "", 0, fmt.Errorf("jvmharness: could not read a version from %s -version output: %q", javaPath, strings.TrimSpace(string(out)))
	}
	raw := m[1]
	// "1.8.0_402" (pre-9 naming) vs "17.0.9"/"24.0.1". The feature
	// version is the second component in the old scheme and the first in
	// the new one; anything in the old scheme is below the floor anyway,
	// so it only has to parse, not be honoured precisely.
	parts := strings.FieldsFunc(raw, func(r rune) bool { return r == '.' || r == '_' || r == '-' || r == '+' })
	if len(parts) == 0 {
		return raw, 0, fmt.Errorf("jvmharness: could not parse java version %q", raw)
	}
	first, err := strconv.Atoi(parts[0])
	if err != nil {
		return raw, 0, fmt.Errorf("jvmharness: could not parse java version %q", raw)
	}
	if first == 1 && len(parts) > 1 {
		second, convErr := strconv.Atoi(parts[1])
		if convErr != nil {
			return raw, 0, fmt.Errorf("jvmharness: could not parse java version %q", raw)
		}
		return raw, second, nil
	}
	return raw, first, nil
}

// SourceDigest is the content hash of every embedded harness source,
// and half of the compile cache's key.
//
// Computed over the sorted (name, bytes) pairs so it changes when any
// source changes, when one is added, and when one is removed -- a
// digest over concatenated contents alone would miss a rename.
func SourceDigest() (string, error) {
	names, err := sourceNames()
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, name := range names {
		data, readErr := harnessFS.ReadFile(name)
		if readErr != nil {
			return "", readErr
		}
		h.Write([]byte(name))
		h.Write([]byte{0})
		h.Write(data)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func sourceNames() ([]string, error) {
	entries, err := harnessFS.ReadDir("harness")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".java") {
			names = append(names, "harness/"+e.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, fmt.Errorf("jvmharness: no embedded harness sources")
	}
	return names, nil
}

// Materialize returns a directory of compiled harness classes, building
// it on first use and reusing it forever after.
//
// cacheRoot is a host-level directory (not per-attempt): the whole point
// is that the ~1.6s compile happens once per (harness source, JDK), not
// once per task attempt. The key covers both, so a harness bump or a JDK
// upgrade produces a NEW directory rather than reusing a stale one --
// nothing is ever invalidated in place, which also means two Brokoli
// versions can share a cache root without fighting.
//
// Concurrency-safe by construction: compilation goes to a temporary
// sibling and is published with a single rename, so a half-built
// directory is never visible under the final name and two workers racing
// on a cold cache both end up with a complete one.
func Materialize(cacheRoot string, tc *Toolchain) (string, error) {
	if tc == nil {
		return "", fmt.Errorf("jvmharness: Materialize needs a resolved Toolchain")
	}
	digest, err := SourceDigest()
	if err != nil {
		return "", err
	}
	classDir := filepath.Join(cacheRoot, fmt.Sprintf("jvmharness-%s-jdk%d", digest[:16], tc.Version))
	// A marker file, not the directory's existence: the directory can
	// exist and be empty (an interrupted older build, a partially removed
	// cache), and treating that as "already compiled" would launch a JVM
	// against no classes.
	marker := filepath.Join(classDir, ".complete")
	if _, statErr := os.Stat(marker); statErr == nil {
		return classDir, nil
	}

	if err := os.MkdirAll(cacheRoot, 0o750); err != nil {
		return "", fmt.Errorf("jvmharness: create cache root: %w", err)
	}
	stagingDir, err := os.MkdirTemp(cacheRoot, "jvmharness-build-")
	if err != nil {
		return "", fmt.Errorf("jvmharness: create build dir: %w", err)
	}
	defer os.RemoveAll(stagingDir)

	srcDir := filepath.Join(stagingDir, "src")
	outDir := filepath.Join(stagingDir, "classes")
	for _, dir := range []string{srcDir, outDir} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return "", fmt.Errorf("jvmharness: create build dir: %w", err)
		}
	}

	names, err := sourceNames()
	if err != nil {
		return "", err
	}
	args := []string{"-d", outDir}
	for _, name := range names {
		data, readErr := harnessFS.ReadFile(name)
		if readErr != nil {
			return "", readErr
		}
		path := filepath.Join(srcDir, filepath.Base(name))
		if writeErr := os.WriteFile(path, data, 0o600); writeErr != nil {
			return "", fmt.Errorf("jvmharness: write harness source: %w", writeErr)
		}
		args = append(args, path)
	}

	out, err := exec.Command(tc.Javac, args...).CombinedOutput() // #nosec G204 -- tc.Javac came from exec.LookPath and the sources are this package's own embedded files
	if err != nil {
		return "", fmt.Errorf("jvmharness: compiling the harness failed (javac %s): %w\n%s", tc.VersionString, err, string(out))
	}
	if err := os.WriteFile(filepath.Join(outDir, ".complete"), []byte(digest), 0o600); err != nil {
		return "", fmt.Errorf("jvmharness: mark build complete: %w", err)
	}

	if err := os.Rename(outDir, classDir); err != nil {
		// Lost a race with another worker that published the same key
		// first. Its output is byte-identical by construction (same
		// sources, same JDK), so using it is correct, not a fallback.
		if _, statErr := os.Stat(marker); statErr == nil {
			return classDir, nil
		}
		// Otherwise an INCOMPLETE directory is occupying the name -- an
		// interrupted build, or a cache someone half-removed. Rename onto
		// a non-empty directory fails, so the stale one has to go first.
		// Safe precisely because it has no marker: without one nothing
		// will launch from it, so there is nothing to break.
		if rmErr := os.RemoveAll(classDir); rmErr != nil {
			return "", fmt.Errorf("jvmharness: clear an incomplete cache dir: %w", rmErr)
		}
		if retryErr := os.Rename(outDir, classDir); retryErr != nil {
			// A concurrent worker may have published between the removal
			// and this retry; its output is equivalent, so take it.
			if _, statErr := os.Stat(marker); statErr == nil {
				return classDir, nil
			}
			return "", fmt.Errorf("jvmharness: publish compiled harness: %w", retryErr)
		}
	}
	return classDir, nil
}

// Command returns the argv to launch the compiled harness.
//
// memoryMB becomes -Xmx, mirroring how nodeharness passes
// --max-old-space-size, and for the same reason: it is not a true
// ceiling (a JVM's metaspace, thread stacks and direct buffers live
// outside the heap) but it lets the JVM fail with an OutOfMemoryError it
// can report through the protocol rather than being killed by the kernel
// with nothing to say. The rlimits pkg/taskharness already applies stay
// the actual bound.
//
// -XX:TieredStopAtLevel=1 is deliberately NOT set: it saved no
// measurable startup time (0.07-0.08s either way) and would cap JIT
// optimization for anything long-running.
func Command(javaPath, classDir string, memoryMB int) []string {
	argv := []string{javaPath}
	if memoryMB > 0 {
		argv = append(argv, fmt.Sprintf("-Xmx%dm", memoryMB))
	}
	return append(argv, "-cp", classDir, "Harness")
}

// Invocation is what this adapter expects to find, as JSON, at a start
// frame's invocation_path -- its own convention, matched by Harness.java.
//
// ClassName/MethodName replace pyharness's Module/Symbol because the JVM
// addresses code by fully-qualified class and method, not by module
// path. ADR-036's bundle form spells the pair as
// "com.example.Tasks#dailyRollup"; it is split here because the harness
// needs the two halves separately and splitting once, on the Go side, is
// better than parsing the same string in every adapter.
type Invocation struct {
	// Classpath entries are resolved bundle-relative paths (jars or
	// class directories). Never URLs or Maven coordinates: ADR-033
	// section 6's rule that references are not locations applies here
	// too, and it is also what stops a task fetching code at run time.
	Classpath []string `json:"classpath,omitempty"`
	// ClassName is fully qualified, e.g. "com.example.tasks.Transforms".
	ClassName string `json:"class_name"`
	// MethodName must be public and static.
	MethodName string `json:"method_name"`
	// Kwargs reach the method as ONE Map argument, since Java has no
	// keyword arguments -- the same forced shape the Node adapter
	// documents, for the same reason.
	Kwargs map[string]interface{} `json:"kwargs,omitempty"`
	// InterfaceDigest is stamped into the candidate task-result-v1
	// manifest.
	InterfaceDigest string `json:"interface_digest"`
	// OutputKind is the port's DECLARED ADR-032 section 6 kind. The
	// engine is authoritative here, never the returned value's runtime
	// shape: a task declaring a dataset that returns a string is a
	// contract violation, not an artifact. Empty means scalar.
	OutputKind string `json:"output_kind,omitempty"`
	// OutputMediaType labels an artifact this task writes (the first
	// media type its output port allows). Ignored for other kinds.
	OutputMediaType string `json:"output_media_type,omitempty"`
	// InputPath names an NDJSON file of rows the worker staged for this
	// attempt, when the node declares an input port. The rows reach the
	// task under the port's own name, so a signature names its input the
	// way the interface does.
	InputPath string `json:"input_path,omitempty"`
	// InputCodec is how InputPath is encoded -- always ndjson/v1 today
	// (ADR-033 section 8's baseline), carried explicitly so a future
	// Arrow IPC input is a value change rather than a format guess.
	InputCodec string `json:"input_codec,omitempty"`
}

// ParseEntrypoint splits ADR-036's "fully.qualified.Class#method" form.
//
// '#' separates rather than ':' or '.' because neither a class name nor
// a method name may contain it, so the split is unambiguous where a dot
// would collide with package separators.
func ParseEntrypoint(entrypoint string) (className, methodName string, err error) {
	parts := strings.Split(entrypoint, "#")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("jvmharness: entrypoint %q must be \"fully.qualified.ClassName#staticMethodName\"", entrypoint)
	}
	return parts[0], parts[1], nil
}

// WriteInvocation writes inv as JSON to path.
func WriteInvocation(path string, inv Invocation) error {
	if inv.ClassName == "" || inv.MethodName == "" {
		return fmt.Errorf("jvmharness: Invocation.ClassName and MethodName must both be set")
	}
	b, err := json.Marshal(inv)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600) // #nosec G306 -- a per-attempt descriptor the trusted worker itself writes and immediately hands to its own child process
}
