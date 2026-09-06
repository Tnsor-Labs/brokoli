//go:build unix

package taskharness

import (
	"errors"
	"os/exec"
	"syscall"

	"github.com/Tnsor-Labs/brokoli/pkg/proctree"
)

// resourceLimitFailure recognizes the kill signals rlimits deliver
// (RLIMIT_CPU raises SIGXCPU then escalates to SIGKILL, RLIMIT_FSIZE
// delivers SIGXFSZ) -- the same approach engine.signalBreachError takes
// for code nodes, so a task killed by its own resource ceiling reports
// resource_exhausted instead of a generic, uninformative
// exited_before_terminal. Returns nil when the exit isn't attributable
// to a configured limit.
func resourceLimitFailure(err error, limits proctree.Rlimits) *Failure {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return nil
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return nil
	}
	switch status.Signal() {
	case syscall.SIGXCPU:
		if limits.CPUSeconds > 0 {
			return &Failure{Category: FailureResourceExhausted, Code: "cpu_limit_exceeded", Message: "harness exceeded its CPU limit"}
		}
	case syscall.SIGKILL:
		if limits.CPUSeconds > 0 {
			return &Failure{Category: FailureResourceExhausted, Code: "cpu_limit_exceeded", Message: "harness was killed, most likely by its CPU limit"}
		}
	case syscall.SIGXFSZ:
		return &Failure{Category: FailureResourceExhausted, Code: "file_size_limit_exceeded", Message: "harness exceeded its output file-size limit"}
	}
	return nil
}
