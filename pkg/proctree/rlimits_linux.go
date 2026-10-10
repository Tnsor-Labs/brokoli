//go:build linux

package proctree

import (
	"fmt"
	"strings"

	"golang.org/x/sys/unix"
)

// Rlimits are ceilings that can be applied safely to every pooled
// runtime. Memory is deliberately absent: Node uses a V8 heap flag and
// must never receive RLIMIT_AS (ADR-030).
type Rlimits struct {
	CPUSeconds    uint64
	FileSizeBytes uint64
	OpenFiles     uint64
}

func ApplyRlimits(pid int, limits Rlimits) error {
	for _, item := range []struct {
		name     string
		resource int
		value    uint64
	}{
		{"CPU", unix.RLIMIT_CPU, limits.CPUSeconds},
		{"file size", unix.RLIMIT_FSIZE, limits.FileSizeBytes},
		{"open files", unix.RLIMIT_NOFILE, limits.OpenFiles},
	} {
		if item.value == 0 {
			continue
		}
		var inherited unix.Rlimit
		if err := unix.Prlimit(pid, item.resource, nil, &inherited); err != nil {
			return fmt.Errorf("read %s rlimit: %w", item.name, err)
		}
		requested := item.value
		if inherited.Cur < requested {
			requested = inherited.Cur
		}
		maximum := item.value
		if inherited.Max < maximum {
			maximum = inherited.Max
		}
		if requested > maximum {
			requested = maximum
		}
		if err := unix.Prlimit(pid, item.resource, &unix.Rlimit{Cur: requested, Max: maximum}, nil); err != nil {
			return fmt.Errorf("apply %s rlimit: %w", item.name, err)
		}
	}
	return nil
}

// ApplyAddressSpaceLimit sets RLIMIT_AS, inherited by everything the
// process starts afterwards. It is kept out of Rlimits on purpose: address
// space is not memory used, and a JVM or Node reserves far more than it
// uses, so this ceiling breaks them at sizes they would run fine in. Only
// a caller that was asked for it explicitly should use it.
func ApplyAddressSpaceLimit(pid int, limitBytes uint64) error {
	if limitBytes == 0 {
		return nil
	}
	var inherited unix.Rlimit
	if err := unix.Prlimit(pid, unix.RLIMIT_AS, nil, &inherited); err != nil {
		return fmt.Errorf("read address-space rlimit: %w", err)
	}
	limit := min(limitBytes, inherited.Max)
	if err := unix.Prlimit(pid, unix.RLIMIT_AS, &unix.Rlimit{Cur: limit, Max: limit}, nil); err != nil {
		return fmt.Errorf("apply address-space rlimit: %w", err)
	}
	return nil
}

// ShellLimitPrelude is a bash prelude that sets limits on the shell itself,
// before it runs anything else, so every process the command starts
// inherits them from its first instruction.
//
// Applying them from outside after the shell has started (ApplyRlimits on
// its PID) leaves a window in which the command can fork: a pipeline's
// children are created at once and keep the values they were forked with.
// The prelude clamps each value to this process's own limits, which the
// shell inherits, exactly as ApplyRlimits clamps to the target's, so an
// unprivileged shell can always apply it. addressSpaceBytes, when not zero,
// also sets RLIMIT_AS (see ApplyAddressSpaceLimit for why that is opt-in).
// The empty string means there is nothing to set.
func ShellLimitPrelude(limits Rlimits, addressSpaceBytes uint64) (string, error) {
	var b strings.Builder
	for _, item := range []struct {
		name     string
		flag     string
		resource int
		value    uint64
		unit     uint64 // bytes per ulimit unit; 1 for counts and seconds
	}{
		{"CPU", "-t", unix.RLIMIT_CPU, limits.CPUSeconds, 1},
		{"file size", "-f", unix.RLIMIT_FSIZE, limits.FileSizeBytes, 1024},
		{"open files", "-n", unix.RLIMIT_NOFILE, limits.OpenFiles, 1},
		{"address space", "-v", unix.RLIMIT_AS, addressSpaceBytes, 1024},
	} {
		if item.value == 0 {
			continue
		}
		var inherited unix.Rlimit
		if err := unix.Getrlimit(item.resource, &inherited); err != nil {
			return "", fmt.Errorf("read %s rlimit: %w", item.name, err)
		}
		soft, hard := min(item.value, inherited.Cur), min(item.value, inherited.Max)
		soft = min(soft, hard)
		// Soft first: lowering the hard limit below the current soft one
		// is refused.
		fmt.Fprintf(&b, "ulimit -S %s %d && ulimit -H %s %d || { echo 'brokoli: could not apply the %s limit' >&2; exit 125; }\n",
			item.flag, soft/item.unit, item.flag, hard/item.unit, item.name)
	}
	if b.Len() == 0 {
		return "", nil
	}
	// POSIX mode counts -f and -v in 512-byte blocks; POSIXLY_CORRECT in
	// the environment would switch it on.
	return "set +o posix\n" + b.String(), nil
}
