//go:build linux

package proctree

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// cgroupMount is where the cgroup v2 hierarchy is mounted. A variable so
// tests can point it elsewhere.
var cgroupMount = "/sys/fs/cgroup"

// MemoryCgroup is a cgroup v2 leaf with a memory ceiling, created for one
// process tree and removed after it. Unlike RLIMIT_AS it counts memory
// actually used, not address space reserved, so a JVM or Node started
// inside it works normally until it really uses more than the limit; and
// the kernel records the kill in memory.events, so a breach can be named.
type MemoryCgroup struct {
	dir string
	fd  *os.File
}

// MemoryCgroupParent resolves the cgroup to create memory cgroups under,
// and makes sure its children get the memory controller.
//
// configured is an operator-chosen directory (absolute, or relative to the
// cgroup mount). Empty means this process's own cgroup, which works only
// where enabling a controller there is allowed: a container's cgroup
// namespace root, or a cgroup that holds no processes. On an ordinary
// host the process's own cgroup is a leaf that holds processes, and the
// kernel refuses (EBUSY): cgroup v2 has no controllers on a cgroup with
// both processes and children. The error says why, for the run log.
func MemoryCgroupParent(configured string) (string, error) {
	var fs unix.Statfs_t
	if err := unix.Statfs(cgroupMount, &fs); err != nil || fs.Type != unix.CGROUP2_SUPER_MAGIC {
		return "", fmt.Errorf("no cgroup v2 hierarchy at %s", cgroupMount)
	}
	dir := configured
	if dir == "" {
		own, err := ownCgroup()
		if err != nil {
			return "", err
		}
		dir = own
	}
	if !strings.HasPrefix(dir, cgroupMount+"/") && dir != cgroupMount {
		dir = filepath.Join(cgroupMount, dir)
	}
	dir = filepath.Clean(dir)
	if !strings.HasPrefix(dir, cgroupMount) {
		return "", fmt.Errorf("cgroup %q is outside %s", configured, cgroupMount)
	}
	control := filepath.Join(dir, "cgroup.subtree_control")
	enabled, err := os.ReadFile(control) // #nosec G304 -- a path under the cgroup mount, checked above.
	if err != nil {
		return "", fmt.Errorf("read %s: %w", control, err)
	}
	if !hasWord(string(enabled), "memory") {
		if err := os.WriteFile(control, []byte("+memory"), 0); err != nil {
			return "", fmt.Errorf("enable the memory controller in %s: %w", dir, err)
		}
	}
	return dir, nil
}

func ownCgroup() (string, error) {
	f, err := os.Open("/proc/self/cgroup")
	if err != nil {
		return "", err
	}
	defer f.Close() //nolint:errcheck
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if path, ok := strings.CutPrefix(scanner.Text(), "0::"); ok {
			return filepath.Join(cgroupMount, path), nil
		}
	}
	return "", errors.New("this process is not in a cgroup v2 hierarchy")
}

func hasWord(s, word string) bool {
	for _, f := range strings.Fields(s) {
		if f == word {
			return true
		}
	}
	return false
}

// NewMemoryCgroup creates a leaf under parent with memory.max set to
// limitBytes, no swap, and group OOM kill: when the limit is hit, the
// whole tree is killed together rather than one process at random.
func NewMemoryCgroup(parent string, limitBytes uint64) (*MemoryCgroup, error) {
	if limitBytes == 0 {
		return nil, errors.New("a memory cgroup needs a limit")
	}
	dir, err := os.MkdirTemp(parent, "brokoli-")
	if err != nil {
		return nil, fmt.Errorf("create cgroup under %s: %w", parent, err)
	}
	c := &MemoryCgroup{dir: dir}
	write := func(name, value string, required bool) error {
		err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0)
		if err != nil && required {
			return fmt.Errorf("set %s: %w", name, err)
		}
		return nil
	}
	if err := write("memory.max", strconv.FormatUint(limitBytes, 10), true); err != nil {
		_ = os.Remove(dir)
		return nil, err
	}
	// Optional: absent without swap accounting, and absent oom.group on
	// old kernels. Swap left on would let the tree exceed the limit in
	// swap instead of being stopped.
	_ = write("memory.swap.max", "0", false)
	_ = write("memory.oom.group", "1", false)
	fd, err := os.Open(dir) // #nosec G304 -- the directory just created under the cgroup mount.
	if err != nil {
		_ = os.Remove(dir)
		return nil, fmt.Errorf("open cgroup %s: %w", dir, err)
	}
	c.fd = fd
	return c, nil
}

// Place makes cmd start inside the cgroup (clone3 CLONE_INTO_CGROUP), so
// nothing it runs allocates before the limit applies.
func (c *MemoryCgroup) Place(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.UseCgroupFD = true
	cmd.SysProcAttr.CgroupFD = int(c.fd.Fd())
}

// OOMKilled reports whether the kernel killed anything in the cgroup for
// exceeding its memory limit.
func (c *MemoryCgroup) OOMKilled() bool {
	data, err := os.ReadFile(filepath.Join(c.dir, "memory.events"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if n, ok := strings.CutPrefix(line, "oom_kill "); ok {
			count, _ := strconv.ParseUint(strings.TrimSpace(n), 10, 64)
			return count > 0
		}
	}
	return false
}

// Close kills whatever is still in the cgroup, including processes that
// left the process group, waits for it to empty and removes it.
func (c *MemoryCgroup) Close() error {
	if c == nil {
		return nil
	}
	_ = os.WriteFile(filepath.Join(c.dir, "cgroup.kill"), []byte("1"), 0)
	if c.fd != nil {
		_ = c.fd.Close()
	}
	var err error
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if err = os.Remove(c.dir); err == nil || errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("remove cgroup %s: %w", c.dir, err)
		}
	}
}
