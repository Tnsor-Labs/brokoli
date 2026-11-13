package plugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A Python or Node task node resolves its interpreter for the
// execution-environment digest and again for the harness, each running
// "<runtime> --version". probeVersion now caches by the resolved binary's
// identity, so the subprocess runs once per distinct binary. See issue
// #1038.
func TestProbeVersionCachesPerBinary(t *testing.T) {
	dir := t.TempDir()
	counter := filepath.Join(dir, "probe-calls")
	if err := os.WriteFile(filepath.Join(dir, "fakeruntime"), []byte("#!/bin/sh\necho call >> "+counter+"\necho '20.11.0'\n"), 0o700); err != nil { // #nosec G306 -- test fixture must be executable
		t.Fatalf("write fixture: %v", err)
	}
	t.Setenv("PATH", dir)

	probeCacheMu.Lock()
	probeCache = map[probeCacheKey]probeCacheValue{}
	probeCacheMu.Unlock()

	path1, major1, minor1, err := probeVersion("fakeruntime", "--version")
	if err != nil {
		t.Fatalf("first probe: %v", err)
	}
	path2, major2, minor2, err := probeVersion("fakeruntime", "--version")
	if err != nil {
		t.Fatalf("second probe: %v", err)
	}
	if major1 != 20 || minor1 != 11 || major2 != major1 || minor2 != minor1 || path1 != path2 {
		t.Fatalf("probes disagree: (%s,%d,%d) vs (%s,%d,%d)", path1, major1, minor1, path2, major2, minor2)
	}

	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatalf("read counter: %v", err)
	}
	if n := len(strings.Fields(strings.TrimSpace(string(data)))); n != 1 {
		t.Fatalf("runtime --version ran %d times across two probes, want 1 (not cached)", n)
	}
}
