package jvmharness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A JVM task node resolves the toolchain several times for a single run
// (the execution-environment digest and the harness on the first attempt,
// the harness again on each later one). Each resolve ran `java -version`, a
// full JVM boot. Resolve now caches by the java binary's identity, so an
// unchanged JDK is probed once. See issue #1038.
func TestResolveCachesPerJavaBinary(t *testing.T) {
	dir := t.TempDir()
	counter := filepath.Join(dir, "java-version-calls")
	// -version writes to stderr on every JDK; emit a parseable version at
	// or above the Java 17 floor and tally each call.
	writeExecutable(t, filepath.Join(dir, "java"),
		"#!/bin/sh\necho call >> "+counter+"\necho 'openjdk version \"21.0.1\" 2023-10-17' 1>&2\n")
	writeExecutable(t, filepath.Join(dir, "javac"),
		"#!/bin/sh\necho 'javac 21.0.1'\n")
	t.Setenv("PATH", dir)

	toolchainCacheMu.Lock()
	toolchainCache = map[probeKey]Toolchain{}
	toolchainCacheMu.Unlock()

	first, err := Resolve()
	if err != nil {
		t.Fatalf("first Resolve: %v", err)
	}
	second, err := Resolve()
	if err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	if first.Version != 21 || second.Version != 21 || first.VersionString != second.VersionString {
		t.Fatalf("toolchains disagree: %+v vs %+v", first, second)
	}
	if n := callCount(t, counter); n != 1 {
		t.Fatalf("java -version ran %d times across two Resolve calls, want 1 (version probe not cached)", n)
	}
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil { // #nosec G306 -- test fixture must be executable
		t.Fatalf("write %s: %v", path, err)
	}
}

func callCount(t *testing.T, counter string) int {
	t.Helper()
	data, err := os.ReadFile(counter)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("read counter: %v", err)
	}
	return len(strings.Fields(strings.TrimSpace(string(data))))
}
