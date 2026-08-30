package codeexec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWrapperVersionParses(t *testing.T) {
	// The version is parsed from the embedded version.py; drift between
	// the Go and Python sides is impossible only while this parse works.
	if v := WrapperVersion(); v != 2 {
		t.Fatalf("WrapperVersion() = %d, want 2", v)
	}
}

func TestWrapperPathMaterializes(t *testing.T) {
	path, err := WrapperPath()
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want, err := pywrapperFS.ReadFile("pywrapper/wrapper.py")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatal("materialized wrapper differs from the embedded one")
	}
	if !strings.Contains(filepath.Base(filepath.Dir(path)), "brokoli-pywrapper-v2-") {
		t.Fatalf("materialized dir not version-tagged: %s", path)
	}
	// Second call: same path, no error (idempotent).
	again, err := WrapperPath()
	if err != nil || again != path {
		t.Fatalf("second WrapperPath() = %q, %v", again, err)
	}
}

func TestJSWrapperVersionParses(t *testing.T) {
	if v := JSWrapperVersion(); v != 1 {
		t.Fatalf("JSWrapperVersion() = %d, want 1", v)
	}
}

func TestJSWrapperPathMaterializes(t *testing.T) {
	path, err := JSWrapperPath()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(path)
	for _, name := range []string{"worker_main.mjs", "protocol.mjs", "contract.mjs", "version.mjs"} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		want, err := jswrapperFS.ReadFile("jswrapper/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("materialized %s differs from embedded bytes", name)
		}
	}
	if !strings.Contains(filepath.Base(dir), "brokoli-jswrapper-v1-") {
		t.Fatalf("materialized dir not version-tagged: %s", path)
	}
	again, err := JSWrapperPath()
	if err != nil || again != path {
		t.Fatalf("second JSWrapperPath() = %q, %v", again, err)
	}
}
