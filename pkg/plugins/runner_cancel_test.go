//go:build unix

package plugins

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The whole file is Unix-only: the fixture is a POSIX shell script and
// the orphan test probes a PID with a signal, neither of which has a
// Windows equivalent. Windows cancellation is deliberately reduced
// (process_other.go) and is not covered here.

// slowPlugin loads the blocking fixture directly from testdata. Unlike
// setupTestPluginDir in manager_test.go there is no copy into a temp
// dir: that exists to exercise plugin *discovery*, and the runner only
// needs a manifest and an executable on disk. The exec bit is carried
// by git.
func slowPlugin(t *testing.T) *Manifest {
	t.Helper()
	man, err := LoadManifest(filepath.Join("testdata", "slow-plugin"))
	if err != nil {
		t.Fatalf("load slow-plugin manifest: %v", err)
	}
	return man
}

func readPIDFile(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read pidfile %s: %v", path, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("parse pid %q: %v", raw, err)
	}
	return pid
}

// TestRunner_Cancel_StopsBlockingPlugin is the baseline: a well-behaved
// plugin blocked in a read is stopped by SIGTERM promptly, well before
// either its own 60s sleep or the runner's timeout would have ended it.
func TestRunner_Cancel_StopsBlockingPlugin(t *testing.T) {
	runner := NewRunner(slowPlugin(t), 30*time.Second)
	runner.terminationGrace = 300 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(200*time.Millisecond, cancel)

	start := time.Now()
	res, err := runner.Read(ctx, Config{}, "blocking", nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("cancelled read returned a nil error")
	}
	// Bounded well under the fixture's sleep and the runner's timeout:
	// proves the process was signalled rather than waited out.
	if elapsed > 3*time.Second {
		t.Errorf("Run took %s to return after cancel, want well under 3s", elapsed)
	}
	// Whatever the plugin produced before it blocked is still returned —
	// Run yields (result, err), never (nil, err).
	if res == nil || len(res.Records) != 1 {
		t.Errorf("want the 1 record emitted before blocking, got %#v", res)
	}
}

// TestRunner_Cancel_KillsPluginThatIgnoresSIGTERM proves the escalation
// exists. Without it this test hangs until the runner's timeout.
func TestRunner_Cancel_KillsPluginThatIgnoresSIGTERM(t *testing.T) {
	const grace = 300 * time.Millisecond

	runner := NewRunner(slowPlugin(t), 30*time.Second)
	runner.terminationGrace = grace

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(200*time.Millisecond, cancel)

	start := time.Now()
	if _, err := runner.Read(ctx, Config{}, "stubborn", nil); err == nil {
		t.Fatal("cancelled read returned a nil error")
	}
	elapsed := time.Since(start)

	// It must have outlasted SIGTERM — this stream ignores it — so a
	// fast return would mean the grace period was never actually
	// applied and the test is proving nothing.
	if elapsed < grace {
		t.Errorf("returned in %s, before the %s grace period elapsed", elapsed, grace)
	}
	// And it must not have waited out the runner's 30s timeout.
	if elapsed > grace+3*time.Second {
		t.Errorf("Run took %s, want roughly the %s grace period", elapsed, grace)
	}
}

// TestRunner_Cancel_KillsOrphanedChild is the process-group test: the
// plugin leaves a background child holding stdout, and cancellation has
// to reach it. Without a process group the child survives and, because
// it holds the write end of the pipe, stdout never reaches EOF.
func TestRunner_Cancel_KillsOrphanedChild(t *testing.T) {
	runner := NewRunner(slowPlugin(t), 30*time.Second)
	runner.terminationGrace = 300 * time.Millisecond

	pidfile := filepath.Join(t.TempDir(), "child.pid")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(500*time.Millisecond, cancel)

	if _, err := runner.Read(ctx, Config{"pidfile": pidfile}, "orphan", nil); err == nil {
		t.Fatal("cancelled read returned a nil error")
	}

	pid := readPIDFile(t, pidfile)

	// The child was never our direct child, so we never reap it: signal
	// 0 checks liveness without sending anything. Poll rather than
	// asserting once — the kill and the OS reaping it are not atomic.
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return // gone
		}
		if err != nil {
			t.Fatalf("probing child %d: %v", pid, err)
		}
		if time.Now().After(deadline) {
			// Don't leak it into the rest of the suite.
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("child %d survived cancellation — the process group was not signalled", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
