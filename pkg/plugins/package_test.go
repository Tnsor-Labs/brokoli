package plugins

// Tests for the .bkg package format (ADR-016 M1, #110): selection,
// integrity, runtime resolution, spec drift, and hostile archives.

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func hasPython3(t *testing.T) bool {
	t.Helper()
	_, err := exec.LookPath("python3")
	return err == nil
}

// buildArchive packs entries (relpath -> content) into a .bkg.
func buildArchive(t *testing.T, entries map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plugin.bkg")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for name, content := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// helloPyEntries reads the checked-in sample into archive entries and
// computes the payload hash the manifest must carry.
func helloPyEntries(t *testing.T, mutate func(m *Manifest)) map[string]string {
	t.Helper()
	payloadSrc := filepath.Join("testdata", "hello-py", "payload")
	hash, err := HashPayloadTree(payloadSrc)
	if err != nil {
		t.Fatal(err)
	}
	m := Manifest{
		ProtocolVersion:  1,
		Name:             "hello-py",
		Version:          "0.1.0",
		PackagingVersion: 1,
		NodeTypes: []NodeTypeDecl{
			{Type: "source_hello_py", Kind: KindSource, DisplayName: "Hello Py Source"},
		},
		Payloads: []Payload{{
			Runtime:    RuntimePython,
			OS:         "any",
			Arch:       "any",
			Path:       "payload",
			Entrypoint: "main.py",
			Requires:   map[string]string{"python": ">=3.8"},
			SHA256:     hash,
		}},
	}
	if mutate != nil {
		mutate(&m)
	}
	manifestJSON, err := json.Marshal(&m)
	if err != nil {
		t.Fatal(err)
	}
	entries := map[string]string{"manifest.json": string(manifestJSON)}
	err = filepath.Walk(payloadSrc, func(path string, info os.FileInfo, err error) error {
		if err != nil || !info.Mode().IsRegular() {
			return err
		}
		rel, _ := filepath.Rel(payloadSrc, path)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		entries["payload/"+filepath.ToSlash(rel)] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestInstallArchivePythonEndToEnd(t *testing.T) {
	if !hasPython3(t) {
		t.Skip("python3 not on PATH")
	}
	dest := t.TempDir()
	archive := buildArchive(t, helloPyEntries(t, nil))

	m, err := InstallArchive(archive, dest)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !filepath.IsAbs(m.Binary) || !strings.Contains(m.Binary, "python") {
		t.Fatalf("binary not resolved to an interpreter: %q", m.Binary)
	}
	if len(m.Args) == 0 || m.Args[0] != "main.py" {
		t.Fatalf("entrypoint not in args: %v", m.Args)
	}

	// The installed plugin must actually execute -- including its
	// vendored dependency -- through the normal manager path.
	mgr, err := NewManager(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !mgr.CanHandle("source_hello_py") {
		t.Fatal("installed plugin's node type not registered")
	}
	runner := NewRunner(m, 30*time.Second)
	out, err := runner.Spec(context.Background())
	if err != nil {
		t.Fatalf("spec after install: %v", err)
	}
	if !strings.Contains(string(out), "hello-py") {
		t.Fatalf("unexpected spec output: %s", out)
	}
}

func TestInstallArchiveRejectsTamperedPayload(t *testing.T) {
	entries := helloPyEntries(t, nil)
	entries["payload/vendored/greeting.py"] += "\n# tampered after hashing\n"
	_, err := InstallArchive(buildArchive(t, entries), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "integrity") {
		t.Fatalf("tampered payload installed: %v", err)
	}
}

func TestInstallArchiveRejectsSpecDrift(t *testing.T) {
	if !hasPython3(t) {
		t.Skip("python3 not on PATH")
	}
	entries := helloPyEntries(t, func(m *Manifest) {
		m.Name = "hello-py-imposter"
		// Name must still pass manifest validation; the plugin's own
		// spec says "hello-py", so install must refuse.
	})
	_, err := InstallArchive(buildArchive(t, entries), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "spec drift") {
		t.Fatalf("drifted package installed: %v", err)
	}
}

func TestInstallArchiveNativeWrongPlatformNamesIt(t *testing.T) {
	entries := helloPyEntries(t, func(m *Manifest) {
		m.Payloads = []Payload{{
			Runtime: RuntimeNative, OS: "plan9", Arch: "mips",
			Path: "payload", Entrypoint: "main.py",
			SHA256: m.Payloads[0].SHA256,
		}}
	})
	_, err := InstallArchive(buildArchive(t, entries), t.TempDir())
	if err == nil ||
		!strings.Contains(err.Error(), "plan9/mips") ||
		!strings.Contains(err.Error(), fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)) {
		t.Fatalf("want platform-naming error, got: %v", err)
	}
}

func TestInstallArchivePythonTooOldNamesVersions(t *testing.T) {
	orig := pythonVersionFn
	pythonVersionFn = func() (string, int, int, error) { return "/usr/bin/python3", 3, 6, nil }
	t.Cleanup(func() { pythonVersionFn = orig })

	entries := helloPyEntries(t, nil) // requires >=3.8
	_, err := InstallArchive(buildArchive(t, entries), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "3.6") || !strings.Contains(err.Error(), ">=3.8") {
		t.Fatalf("want versions in error, got: %v", err)
	}
}

func TestInstallArchiveJVMMissingRuntimeNamesIt(t *testing.T) {
	orig := runtimeVersionFns["java"]
	runtimeVersionFns["java"] = func() (string, int, int, error) {
		return "", 0, 0, fmt.Errorf("java not found on PATH")
	}
	t.Cleanup(func() { runtimeVersionFns["java"] = orig })

	entries := helloPyEntries(t, func(m *Manifest) {
		m.Payloads[0].Runtime = RuntimeJVM
		m.Payloads[0].Entrypoint = "plugin.jar"
		m.Payloads[0].Requires = map[string]string{"java": ">=17"}
	})
	_, err := InstallArchive(buildArchive(t, entries), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "java not found") {
		t.Fatalf("want java-not-found error, got: %v", err)
	}
}

func TestInstallArchiveNodeTooOldNamesVersions(t *testing.T) {
	orig := runtimeVersionFns["node"]
	runtimeVersionFns["node"] = func() (string, int, int, error) {
		return "/usr/bin/node", 16, 4, nil
	}
	t.Cleanup(func() { runtimeVersionFns["node"] = orig })

	entries := helloPyEntries(t, func(m *Manifest) {
		m.Payloads[0].Runtime = RuntimeNode
		m.Payloads[0].Entrypoint = "index.js"
		m.Payloads[0].Requires = map[string]string{"node": ">=20.0"}
	})
	_, err := InstallArchive(buildArchive(t, entries), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "16.4") || !strings.Contains(err.Error(), ">=20.0") {
		t.Fatalf("want node version error, got: %v", err)
	}
}

func TestResolveNodeUsesSharedVersionPolicy(t *testing.T) {
	orig := runtimeVersionFns["node"]
	t.Cleanup(func() { runtimeVersionFns["node"] = orig })
	runtimeVersionFns["node"] = func() (string, int, int, error) {
		return "/runtime/node", 20, 11, nil
	}
	if path, reason := ResolveNode(">=20.0"); path != "/runtime/node" || reason != "" {
		t.Fatalf("ResolveNode accepted runtime = %q, %q", path, reason)
	}
	runtimeVersionFns["node"] = func() (string, int, int, error) {
		return "/runtime/node", 19, 9, nil
	}
	if path, reason := ResolveNode(">=20.0"); path != "" || !strings.Contains(reason, "19.9") {
		t.Fatalf("ResolveNode old runtime = %q, %q", path, reason)
	}
}

func TestResolveNodePathEnforcesVersion(t *testing.T) {
	oldNode := filepath.Join(t.TempDir(), "node")
	if err := os.WriteFile(oldNode, []byte("#!/bin/sh\necho v18.19.0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if path, reason := ResolveNodePath(oldNode, ">=20.0"); path != "" || !strings.Contains(reason, "18.19") {
		t.Fatalf("old node_path = %q, %q", path, reason)
	}
	if path, reason := ResolveNodePath(filepath.Join(t.TempDir(), "missing"), ">=20.0"); path != "" || !strings.Contains(reason, "not found") {
		t.Fatalf("missing node_path = %q, %q", path, reason)
	}
}

func TestFirstDottedVersion(t *testing.T) {
	cases := []struct {
		in       string
		maj, min int
		ok       bool
	}{
		{"v20.11.1\n", 20, 11, true},
		{`openjdk version "17.0.2"`, 17, 0, true},
		{"Python 3.12.3", 3, 12, true},
		{"v20", 0, 0, false}, // single number, no dotted major.minor
		{"no version here", 0, 0, false},
	}
	for _, c := range cases {
		maj, min, ok := firstDottedVersion(c.in)
		if ok != c.ok || (ok && (maj != c.maj || min != c.min)) {
			t.Errorf("%q: got %d.%d ok=%v, want %d.%d ok=%v", c.in, maj, min, ok, c.maj, c.min, c.ok)
		}
	}
}

func TestExtractRejectsTraversal(t *testing.T) {
	entries := map[string]string{
		"manifest.json":    "{}",
		"../../escape.txt": "gotcha",
	}
	_, err := InstallArchive(buildArchive(t, entries), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("traversal archive accepted: %v", err)
	}
}

func TestHashPayloadTreeIsOrderAndModeInsensitive(t *testing.T) {
	a := t.TempDir()
	if err := os.WriteFile(filepath.Join(a, "z.txt"), []byte("zz"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(a, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a, "sub", "a.txt"), []byte("aa"), 0o600); err != nil {
		t.Fatal(err)
	}
	h1, err := HashPayloadTree(a)
	if err != nil {
		t.Fatal(err)
	}
	// Same content, different modes elsewhere -> same hash.
	if err := os.Chmod(filepath.Join(a, "z.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	h2, err := HashPayloadTree(a)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatal("hash depends on file modes")
	}
	// Content change -> different hash.
	if err := os.WriteFile(filepath.Join(a, "sub", "a.txt"), []byte("ab"), 0o600); err != nil {
		t.Fatal(err)
	}
	h3, _ := HashPayloadTree(a)
	if h3 == h1 {
		t.Fatal("hash missed a content change")
	}
}
