package engine

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/proctree"
)

// BashCgroupEnv names the cgroup v2 directory bash nodes create their
// memory cgroups under: absolute, or relative to the cgroup mount. The
// worker's user must be able to write it, and to move processes into it,
// which cgroup v2 allows only with write access up to the common ancestor
// of the worker's own cgroup and this one -- in practice, a subtree
// delegated to the worker (systemd Delegate=yes, or a container with its
// own writable cgroup namespace). Unset, the worker tries its own cgroup,
// which works at a container's cgroup namespace root.
const BashCgroupEnv = "BROKOLI_BASH_CGROUP"

// bashMemory is how one bash attempt's memory ceiling is enforced.
//
// A bash node has no interpreter to enforce memory from inside, as code
// nodes do, so the ceiling is applied from outside, in order of preference:
//
//   - a cgroup v2 memory.max for the whole process tree. It counts memory
//     used, so a JVM or Node the command starts works normally, and the
//     kernel records the kill, so the breach is named.
//   - RLIMIT_AS, only when the node asked for a limit itself
//     (max_memory_mb). Address space is not memory: a JVM or Node reserves
//     far more than it uses and fails under it at sizes it would run fine
//     in, which is why a server-wide default never turns it on.
//   - nothing, with a warning in the node's log saying the limit is not
//     enforced and why.
type bashMemory struct {
	limitMB      int
	explicit     bool
	parent       string
	cgroup       *proctree.MemoryCgroup
	addressSpace bool
}

func (r *Runner) planBashMemory(node models.Node, limitMB int) *bashMemory {
	m := &bashMemory{limitMB: limitMB, explicit: configPositive(node.Config, "max_memory_mb")}
	if limitMB <= 0 {
		return m
	}
	parent, err := proctree.MemoryCgroupParent(os.Getenv(BashCgroupEnv))
	if err == nil {
		m.parent = parent
		m.cgroup, err = proctree.NewMemoryCgroup(parent, uint64(limitMB)*1024*1024)
	}
	if err != nil {
		m.fallBack(r, node, err)
		return m
	}
	r.logBashNotice(node.ID, models.LogLevelInfo, fmt.Sprintf("memory limit %d MiB, enforced by a cgroup", limitMB))
	return m
}

// fallBack drops the cgroup and settles on the next enforcement, saying
// which in the node's log.
func (m *bashMemory) fallBack(r *Runner, node models.Node, reason error) {
	if m.cgroup != nil {
		_ = m.cgroup.Close()
		m.cgroup = nil
	}
	if m.explicit {
		m.addressSpace = true
		r.logBashNotice(node.ID, models.LogLevelWarning, fmt.Sprintf(
			"memory limit %d MiB enforced as address space (RLIMIT_AS), because no cgroup is available (%v); "+
				"a JVM or Node started by this command may fail under it. Set %s to a delegated cgroup to count real memory instead",
			m.limitMB, reason, BashCgroupEnv))
		return
	}
	r.logBashNotice(node.ID, models.LogLevelWarning, fmt.Sprintf(
		"memory limit %d MiB is NOT enforced for this bash command: no cgroup is available (%v). "+
			"Set %s to a delegated cgroup, or max_memory_mb on the node to enforce it as address space",
		m.limitMB, reason, BashCgroupEnv))
}

func (m *bashMemory) place(cmd *exec.Cmd) {
	if m.cgroup != nil {
		m.cgroup.Place(cmd)
	}
}

// close removes the cgroup, killing anything still in it: a process
// that left the process group (setsid) is still in the cgroup.
func (m *bashMemory) close() {
	if m.cgroup != nil {
		_ = m.cgroup.Close()
	}
}

func (r *Runner) logBashNotice(nodeID string, level models.LogLevel, msg string) {
	if r.store == nil || r.run == nil {
		return
	}
	r.log(nodeID, level, "[bash] %s", msg)
}

// bashOutOfMemory recognises an allocation failure under RLIMIT_AS in the
// words the usual programs fail in: bash and coreutils, glibc, Python.
func bashOutOfMemory(stderr string) bool {
	s := strings.ToLower(stderr)
	for _, sign := range []string{"cannot allocate memory", "memory exhausted", "out of memory", "memoryerror", "xmalloc: cannot allocate", "memory allocation"} {
		if strings.Contains(s, sign) {
			return true
		}
	}
	return false
}

func configPositive(config map[string]interface{}, key string) bool {
	switch v := config[key].(type) {
	case float64:
		return v > 0
	case int:
		return v > 0
	}
	return false
}
