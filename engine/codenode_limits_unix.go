//go:build unix

package engine

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"

	"github.com/Tnsor-Labs/brokoli/pkg/codeexec"
)

// signalBreachError recognizes the kill signals rlimits deliver.
// RLIMIT_CPU raises SIGXCPU at the soft limit and escalates to SIGKILL
// at the hard limit (we set both to the same value, so either can win
// the race); RLIMIT_FSIZE delivers SIGXFSZ.
func signalBreachError(err error, limits codeexec.Limits) error {
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
			return fmt.Errorf("script exceeded the CPU limit (%d s): raise max_cpu_seconds on the node or the server default", limits.CPUSeconds)
		}
	case syscall.SIGKILL:
		if limits.CPUSeconds > 0 {
			return fmt.Errorf("script was killed, most likely by the CPU limit (%d s)", limits.CPUSeconds)
		}
	case syscall.SIGXFSZ:
		return fmt.Errorf("script exceeded the output file-size limit (%d MiB)", limits.FileSizeMB)
	}
	return nil
}
