// Package codeexec owns the code-node execution contract (ADR-029):
// resource ceilings now, and — in later stages of that ADR's rollout —
// the worker pool, the framed protocol, and the extracted Python
// wrapper. Today it provides the limits model both spawn paths apply.
package codeexec

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Limits bound one code-node subprocess. MemoryMB and CPUSeconds of
// zero mean unlimited; FileSizeMB and OpenFiles fall back to their
// documented defaults when zero. Python enforces memory as RLIMIT_AS;
// TypeScript uses V8's --max-old-space-size because Node's reserved
// address-space cage makes RLIMIT_AS unsuitable (ADR-030).
type Limits struct {
	MemoryMB   int
	CPUSeconds int
	FileSizeMB int
	OpenFiles  int
}

const (
	defaultFileSizeMB = 4096
	defaultOpenFiles  = 256
)

func envInt(name string) int {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// DefaultLimits are the server-operator defaults, from the environment:
// BROKOLI_CODE_MEMORY_MB, BROKOLI_CODE_CPU_SECONDS,
// BROKOLI_CODE_FILE_SIZE_MB, BROKOLI_CODE_OPEN_FILES. Memory and CPU
// default to unlimited so single-user hosts see no behavior change;
// multi-tenant deployments set them (the EE charts do).
func DefaultLimits() Limits {
	l := Limits{
		MemoryMB:   envInt("BROKOLI_CODE_MEMORY_MB"),
		CPUSeconds: envInt("BROKOLI_CODE_CPU_SECONDS"),
		FileSizeMB: envInt("BROKOLI_CODE_FILE_SIZE_MB"),
		OpenFiles:  envInt("BROKOLI_CODE_OPEN_FILES"),
	}
	if l.FileSizeMB == 0 {
		l.FileSizeMB = defaultFileSizeMB
	}
	if l.OpenFiles == 0 {
		l.OpenFiles = defaultOpenFiles
	}
	return l
}

func configInt(config map[string]interface{}, key string) int {
	switch v := config[key].(type) {
	case float64:
		if v > 0 {
			return int(v)
		}
	case int:
		if v > 0 {
			return v
		}
	}
	return 0
}

// Resolve computes the effective limits for one node: the server
// defaults, overridden by the node's own max_memory_mb /
// max_cpu_seconds, with node overrides clamped to the server maxima
// (BROKOLI_CODE_MAX_MEMORY_MB / BROKOLI_CODE_MAX_CPU_SECONDS). The
// maxima clamp only explicit node overrides — an operator who wants a
// ceiling on every node sets the default, not just the max.
func Resolve(nodeConfig map[string]interface{}) Limits {
	l := DefaultLimits()
	maxMem := envInt("BROKOLI_CODE_MAX_MEMORY_MB")
	maxCPU := envInt("BROKOLI_CODE_MAX_CPU_SECONDS")
	if v := configInt(nodeConfig, "max_memory_mb"); v > 0 {
		if maxMem > 0 && v > maxMem {
			v = maxMem
		}
		l.MemoryMB = v
	}
	if v := configInt(nodeConfig, "max_cpu_seconds"); v > 0 {
		if maxCPU > 0 && v > maxCPU {
			v = maxCPU
		}
		l.CPUSeconds = v
	}
	return l
}

// Env renders the limits as the BROKED_LIMIT_* variables the Python
// wrapper's rlimit preamble reads. Unlimited values are omitted.
func (l Limits) Env() []string {
	var env []string
	if l.MemoryMB > 0 {
		env = append(env, fmt.Sprintf("BROKED_LIMIT_MEMORY_MB=%d", l.MemoryMB))
	}
	if l.CPUSeconds > 0 {
		env = append(env, fmt.Sprintf("BROKED_LIMIT_CPU_SECONDS=%d", l.CPUSeconds))
	}
	if l.FileSizeMB > 0 {
		env = append(env, fmt.Sprintf("BROKED_LIMIT_FILE_SIZE_MB=%d", l.FileSizeMB))
	}
	if l.OpenFiles > 0 {
		env = append(env, fmt.Sprintf("BROKED_LIMIT_OPEN_FILES=%d", l.OpenFiles))
	}
	return env
}

// String is the human-readable form used in the per-run audit log line.
func (l Limits) String() string {
	mem, cpu := "unlimited", "unlimited"
	if l.MemoryMB > 0 {
		mem = fmt.Sprintf("%d MiB", l.MemoryMB)
	}
	if l.CPUSeconds > 0 {
		cpu = fmt.Sprintf("%d s", l.CPUSeconds)
	}
	return fmt.Sprintf("memory=%s cpu=%s", mem, cpu)
}
