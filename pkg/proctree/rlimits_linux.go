//go:build linux

package proctree

import (
	"fmt"

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
