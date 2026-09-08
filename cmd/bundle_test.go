package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/taskbundlev2"
	"github.com/Tnsor-Labs/brokoli/pkg/taskharness/jvmharness"
)

func jdk(t *testing.T) *jvmharness.Toolchain {
	t.Helper()
	tc, err := jvmharness.Resolve()
	if err != nil {
		t.Skipf("no usable JDK: %v", err)
	}
	return tc
}

// compileFixture builds a package-qualified class and returns the
// compiler output root.
func compileFixture(t *testing.T, tc *jvmharness.Toolchain) string {
	t.Helper()
	work := t.TempDir()
	srcDir := filepath.Join(work, "src", "com", "example")
	if err := os.MkdirAll(srcDir, 0o750); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(srcDir, "Rollup.java")
	if err := os.WriteFile(src, []byte(
		"package com.example;\npublic final class Rollup {\n    public static Object run() { return 1L; }\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	classes := filepath.Join(work, "classes")
	if out, err := exec.Command(tc.Javac, "-d", classes, src).CombinedOutput(); err != nil { // #nosec G204 -- resolved toolchain
		t.Fatalf("javac: %v\n%s", err, out)
	}
	return classes
}

// A classloader finds com.example.Rollup at com/example/Rollup.class
// beneath a classpath root, so the package structure under the
// compiler's output directory is exactly what packaging must preserve.
// Flattening it produces a bundle whose classes cannot be found -- and
// the bundle would still be well-formed, so nothing else would catch it.
func TestCollectClassTreePreservesPackageStructure(t *testing.T) {
	tc := jdk(t)
	classes := compileFixture(t, tc)

	files := map[string]string{}
	if err := collectClassTree(classes, files); err != nil {
		t.Fatalf("collectClassTree: %v", err)
	}
	if _, ok := files["com/example/Rollup.class"]; !ok {
		got := make([]string, 0, len(files))
		for k := range files {
			got = append(got, k)
		}
		t.Fatalf("package structure lost; packaged as %v", got)
	}
}

// The archive this command writes has to be one the runtime accepts:
// packaging and running are written in different places and could drift
// while each side's own tests still pass.
func TestJVMBundleIsAValidRunnableBundle(t *testing.T) {
	tc := jdk(t)
	classes := compileFixture(t, tc)

	files := map[string]string{}
	if err := collectClassTree(classes, files); err != nil {
		t.Fatal(err)
	}
	manifest, err := jvmManifest("rollup", "com.example.Rollup", "run", files)
	if err != nil {
		t.Fatalf("jvmManifest: %v", err)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("the manifest this command writes is not valid: %v", err)
	}
	archive, err := taskbundlev2.Assemble(files, manifest)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}

	// Extract it the way the server does, and confirm a payload is
	// selectable for the jvm runtime class.
	dest := t.TempDir()
	extracted, err := taskbundlev2.Extract(archive, dest)
	if err != nil {
		t.Fatalf("the server could not extract this bundle: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dest, "com", "example", "Rollup.class")); statErr != nil {
		t.Fatalf("extracted bundle lost the package structure: %v", statErr)
	}
	payload, err := taskbundlev2.SelectPayload(extracted, []string{taskbundlev2.RuntimeJVM})
	if err != nil {
		t.Fatalf("no jvm payload selectable from this bundle: %v", err)
	}
	if payload.Entrypoint.Module != "com.example.Rollup" || payload.Entrypoint.Symbol != "run" {
		t.Errorf("entrypoint = %q#%q, want com.example.Rollup#run", payload.Entrypoint.Module, payload.Entrypoint.Symbol)
	}
}

// Repackaging identical input must yield an identical digest, or
// content-addressing buys nothing: every rebuild would look like a new
// bundle and re-upload.
func TestJVMBundleDigestIsStableForIdenticalInput(t *testing.T) {
	tc := jdk(t)
	classes := compileFixture(t, tc)

	digestOnce := func() string {
		files := map[string]string{}
		if err := collectClassTree(classes, files); err != nil {
			t.Fatal(err)
		}
		m, err := jvmManifest("rollup", "com.example.Rollup", "run", files)
		if err != nil {
			t.Fatal(err)
		}
		archive, err := taskbundlev2.Assemble(files, m)
		if err != nil {
			t.Fatal(err)
		}
		return taskbundlev2.DigestOf(archive)
	}
	if a, b := digestOnce(), digestOnce(); a != b {
		t.Fatalf("digest is not stable: %s vs %s", a, b)
	}
}

func TestParseEntrypointIsRequiredToBeClassHashMethod(t *testing.T) {
	for _, bad := range []string{"com.example.Rollup", "com.example.Rollup.run", "#run", "Rollup#"} {
		if _, _, err := jvmharness.ParseEntrypoint(bad); err == nil {
			t.Errorf("entrypoint %q was accepted", bad)
		}
	}
}

// A .class symlink pointing outside the classes tree must not be
// packaged. Otherwise `brokoli bundle jvm` is a way to read an arbitrary
// file and ship it to a server -- the bundle would look entirely normal,
// and its digest would faithfully cover the wrong bytes.
func TestCollectClassTreeRefusesASymlinkOutOfTheTree(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "secret.class")
	if err := os.WriteFile(outside, []byte("not really bytecode"), 0o600); err != nil {
		t.Fatal(err)
	}
	classes := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(classes, "Escaped.class")); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}

	files := map[string]string{}
	err := collectClassTree(classes, files)
	if err == nil && len(files) > 0 {
		t.Fatalf("a symlink out of the tree was packaged: %v", files)
	}
	if body, ok := files["Escaped.class"]; ok && body == "not really bytecode" {
		t.Fatal("the file outside the tree was read and packaged")
	}
}
