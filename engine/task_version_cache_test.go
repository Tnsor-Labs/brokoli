package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// computeExecutionEnvironmentDigest ran "<runtime> --version" on every task
// run to fold the runtime's version into the digest. runtimeVersionString
// now caches by the binary's identity, so the subprocess runs once per
// distinct binary. See issue #1038.
func TestRuntimeVersionStringCachesPerBinary(t *testing.T) {
	dir := t.TempDir()
	counter := filepath.Join(dir, "version-calls")
	bin := filepath.Join(dir, "fakeruntime")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho call >> "+counter+"\necho 'fake runtime 3.1.4'\n"), 0o700); err != nil { // #nosec G306 -- test fixture must be executable
		t.Fatalf("write fixture: %v", err)
	}

	versionCacheMu.Lock()
	versionCache = map[versionKey]string{}
	versionCacheMu.Unlock()

	first, err := runtimeVersionString(bin)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	second, err := runtimeVersionString(bin)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if first != "fake runtime 3.1.4" || second != first {
		t.Fatalf("version strings disagree: %q vs %q", first, second)
	}

	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatalf("read counter: %v", err)
	}
	if n := len(strings.Fields(strings.TrimSpace(string(data)))); n != 1 {
		t.Fatalf("runtime --version ran %d times across two calls, want 1 (not cached)", n)
	}
}
