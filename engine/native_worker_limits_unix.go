//go:build unix

package engine

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// applyNativeWorkerLimits sets the child's own resource ceilings before any
// driver is loaded. Soft and hard limits are both set, so code loaded later
// cannot raise them again.
func applyNativeWorkerLimits(limits nativeWorkerLimits) error {
	set := func(resource int, name string, value uint64) error {
		if value == 0 {
			return nil
		}
		if err := unix.Setrlimit(resource, &unix.Rlimit{Cur: value, Max: value}); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		return nil
	}
	if err := set(unix.RLIMIT_AS, "memory", uint64(limits.MemoryMB)<<20); err != nil {
		return err
	}
	if err := set(unix.RLIMIT_CPU, "cpu", uint64(limits.CPUSeconds)); err != nil {
		return err
	}
	return set(unix.RLIMIT_NOFILE, "open files", uint64(limits.OpenFiles))
}
