package engine

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// bashAllocate makes tail hold n bytes: a single line with no newline is
// kept whole in memory until the input ends.
func bashAllocate(n string) string {
	return "head -c " + n + " /dev/zero | tail -n 1 > /dev/null"
}

// bashTestCgroup points BROKOLI_BASH_CGROUP at a fresh delegated parent
// under BROKOLI_TEST_CGROUP, which must be a cgroup v2 directory this user
// can create children in and move processes into, with the memory
// controller enabled for its children. Skips when unset: most machines,
// and every container without its own writable cgroup, have none.
func bashTestCgroup(t *testing.T) string {
	t.Helper()
	root := os.Getenv("BROKOLI_TEST_CGROUP")
	if root == "" {
		t.Skip("set BROKOLI_TEST_CGROUP to a delegated cgroup v2 directory to test cgroup memory limits")
	}
	parent, err := os.MkdirTemp(root, "t-")
	if err != nil {
		t.Fatalf("create test cgroup: %v", err)
	}
	t.Cleanup(func() {
		for i := 0; i < 100; i++ {
			if os.Remove(parent) == nil {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Errorf("test cgroup %s was left behind", parent)
	})
	t.Setenv(BashCgroupEnv, parent)
	return parent
}

func requireLinuxLimits(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("memory limits are enforced on Linux")
	}
}

// noCgroup makes the cgroup path unavailable, deterministically.
func noCgroup(t *testing.T) {
	t.Helper()
	t.Setenv(BashCgroupEnv, "/sys/fs/cgroup/brokoli-test-does-not-exist")
}

func bashMemoryNode(t *testing.T, command string, config map[string]interface{}) ([]string, error) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("BROKOLI_DATA_DIRS", dir)
	r := newUnitTestRunner(t)
	node := bashTestNode(command, dir)
	for k, v := range config {
		node.Config[k] = v
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, err := r.runBash(ctx, node, nil)
	return bashLogMessages(t, r), err
}

func TestBashMemoryCgroupNamesABreach(t *testing.T) {
	requireLinuxLimits(t)
	parent := bashTestCgroup(t)
	_, err := bashMemoryNode(t, bashAllocate("300000000"), map[string]interface{}{"max_memory_mb": float64(64)})
	if err == nil || !strings.Contains(err.Error(), "bash command exceeded the memory limit (64 MiB)") {
		t.Fatalf("err = %v, want the memory limit named", err)
	}
	if strings.Contains(err.Error(), "address space") {
		t.Fatalf("a cgroup was available, yet: %v", err)
	}
	entries, _ := os.ReadDir(parent)
	for _, e := range entries {
		if e.IsDir() {
			t.Fatalf("the node's cgroup %s was left behind", e.Name())
		}
	}
}

// The same ceiling lets a command that stays under it run, and the log
// says how it was enforced.
func TestBashMemoryCgroupAllowsUseUnderTheLimit(t *testing.T) {
	requireLinuxLimits(t)
	bashTestCgroup(t)
	logs, err := bashMemoryNode(t, bashAllocate("30000000"), map[string]interface{}{"max_memory_mb": float64(256)})
	if err != nil {
		t.Fatalf("a command under the limit failed: %v", err)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "memory limit 256 MiB, enforced by a cgroup") {
		t.Fatalf("the log does not say how memory was limited: %v", logs)
	}
}

// The server default applies through a cgroup too: it counts real memory,
// so it is safe as a default.
func TestBashMemoryCgroupAppliesTheServerDefault(t *testing.T) {
	requireLinuxLimits(t)
	bashTestCgroup(t)
	t.Setenv("BROKOLI_CODE_MEMORY_MB", "64")
	_, err := bashMemoryNode(t, bashAllocate("300000000"), nil)
	if err == nil || !strings.Contains(err.Error(), "exceeded the memory limit (64 MiB)") {
		t.Fatalf("err = %v, want the default limit enforced", err)
	}
}

// A process that leaves the process group (setsid) is still in the
// cgroup, and does not outlive the node.
func TestBashMemoryCgroupStopsEscapedProcesses(t *testing.T) {
	requireLinuxLimits(t)
	bashTestCgroup(t)
	dir := t.TempDir()
	t.Setenv("BROKOLI_DATA_DIRS", dir)
	node := bashTestNode("setsid sh -c 'sleep 1; touch leaked' >/dev/null 2>&1 < /dev/null &", dir)
	node.Config["max_memory_mb"] = float64(256)
	if _, err := (&Runner{}).runBash(context.Background(), node, nil); err != nil {
		t.Fatalf("runBash: %v", err)
	}
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir, "leaked")); err == nil {
		t.Fatal("a process that left the process group outlived the node")
	}
}

// Without a cgroup, a limit the node asked for itself is enforced as
// address space, and the breach is named as that.
func TestBashMemoryFallsBackToAddressSpaceWhenAsked(t *testing.T) {
	requireLinuxLimits(t)
	noCgroup(t)
	logs, err := bashMemoryNode(t, bashAllocate("300000000"), map[string]interface{}{"max_memory_mb": float64(100)})
	if err == nil || !strings.Contains(err.Error(), "exceeded the memory limit (100 MiB, enforced as address space)") {
		t.Fatalf("err = %v, want the address-space limit named", err)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "enforced as address space (RLIMIT_AS)") {
		t.Fatalf("the log does not warn about the address-space fallback: %v", logs)
	}
}

// The server maximum clamps the node's own request, as for code nodes.
func TestBashMemoryNodeRequestIsClampedToTheServerMaximum(t *testing.T) {
	requireLinuxLimits(t)
	noCgroup(t)
	t.Setenv("BROKOLI_CODE_MAX_MEMORY_MB", "100")
	_, err := bashMemoryNode(t, bashAllocate("300000000"), map[string]interface{}{"max_memory_mb": float64(100000)})
	if err == nil || !strings.Contains(err.Error(), "(100 MiB, enforced as address space)") {
		t.Fatalf("err = %v, want the request clamped to 100 MiB", err)
	}
}

// A server-wide default never turns into RLIMIT_AS: it would break every
// JVM and Node a bash command starts. Without a cgroup it is not enforced,
// and the node's log says so.
func TestBashMemoryServerDefaultIsNeverAddressSpace(t *testing.T) {
	requireLinuxLimits(t)
	noCgroup(t)
	t.Setenv("BROKOLI_CODE_MEMORY_MB", "100")
	logs, err := bashMemoryNode(t, bashAllocate("300000000"), nil)
	if err != nil {
		t.Fatalf("the server default was enforced as address space: %v", err)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "memory limit 100 MiB is NOT enforced for this bash command") {
		t.Fatalf("the log does not say the limit is not enforced: %v", logs)
	}
}

func TestBashMemoryWithoutALimitSaysNothing(t *testing.T) {
	requireLinuxLimits(t)
	noCgroup(t)
	t.Setenv("BROKOLI_CODE_MEMORY_MB", "")
	logs, err := bashMemoryNode(t, "true", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(logs, "\n"), "memory limit") {
		t.Fatalf("no limit was configured, yet: %v", logs)
	}
}

func TestBashOutOfMemorySignatures(t *testing.T) {
	for _, s := range []string{"tail: memory exhausted", "bash: xmalloc: cannot allocate 100 bytes", "MemoryError", "Cannot allocate memory"} {
		if !bashOutOfMemory(s) {
			t.Errorf("%q not recognised", s)
		}
	}
	if bashOutOfMemory("permission denied") {
		t.Error("an unrelated failure was taken for a memory breach")
	}
}

// The limits are on the shell before the command runs (#818). Applied to
// the PID after Start, a pipeline's children could fork first and keep no
// limits at all; one CI run allocated 300 MB under a 100 MiB cap that way.
// A child forked as the command's very first act must see every limit.
func TestBashLimitsAreInheritedByAChildForkedAtOnce(t *testing.T) {
	requireLinuxLimits(t)
	noCgroup(t)
	logs, err := bashMemoryNode(t, "cat /proc/self/limits | cat", map[string]interface{}{
		"max_memory_mb": float64(200), "max_cpu_seconds": float64(7),
	})
	if err != nil {
		t.Fatalf("runBash: %v", err)
	}
	out := strings.Join(logs, "\n")
	for _, want := range []*regexp.Regexp{
		regexp.MustCompile(`Max cpu time\s+7\s+7\s+seconds`),
		regexp.MustCompile(`Max address space\s+209715200\s+209715200\s+bytes`),
	} {
		if !want.MatchString(out) {
			t.Errorf("the forked child's limits do not match %s:\n%s", want, out)
		}
	}
}

// A limit the prelude cannot set stops the node before the command runs,
// and says so rather than reporting the command as failed.
func TestBashLimitPreludeFailureIsReportedAsSuch(t *testing.T) {
	requireLinuxLimits(t)
	noCgroup(t)
	_, err := bashMemoryNode(t, "echo 'brokoli: could not apply the CPU limit' >&2; exit 125", nil)
	if err == nil || !strings.HasPrefix(err.Error(), "apply bash limits: brokoli: could not apply the CPU limit") {
		t.Fatalf("err = %v, want it reported as a limits failure", err)
	}
	// Control: any other failing exit is the command's own.
	_, err = bashMemoryNode(t, "echo nope >&2; exit 3", nil)
	if err == nil || !strings.HasPrefix(err.Error(), "bash command failed") {
		t.Fatalf("err = %v, want the command's own failure", err)
	}
}
