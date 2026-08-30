package engine

import (
	"fmt"
	"strings"

	"github.com/Tnsor-Labs/brokoli/pkg/codeexec"
)

// limitBreachError maps a failed code-node subprocess to an error that
// names the resource ceiling responsible (ADR-029: never a bare
// "signal: killed"). Returns nil when the failure is not attributable
// to a configured limit; callers must only consult it when the context
// itself did not cancel the run, so a user cancel is never misreported
// as a limit breach.
func limitBreachError(err error, stderr string, limits codeexec.Limits) error {
	if err == nil {
		return nil
	}
	// RLIMIT_AS breaches surface inside Python as MemoryError, which
	// reaches us through the traceback on stderr — but native libraries
	// fail in their own words first: OpenBLAS prints "Memory allocation
	// ... failed" and exits, glibc-level failures say "Cannot allocate
	// memory". Any of them under a configured ceiling is the ceiling.
	memorySignature := strings.Contains(stderr, "MemoryError") ||
		strings.Contains(stderr, "Memory allocation") ||
		strings.Contains(stderr, "Cannot allocate memory")
	if limits.MemoryMB > 0 && memorySignature {
		return fmt.Errorf(
			"script exceeded the memory limit (%d MiB, enforced as address space): "+
				"raise max_memory_mb on the node or the server default, or stream with emit() "+
				"instead of materializing the dataset\nstderr: %s",
			limits.MemoryMB, stderr)
	}
	return signalBreachError(err, limits)
}
