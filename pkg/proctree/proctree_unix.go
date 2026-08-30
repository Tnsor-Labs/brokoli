//go:build unix

package proctree

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// ConfigureProcessGroup makes the child the leader of its own process
// group, so every process it spawns inherits that group and the runner
// can signal the whole tree at once.
//
// Without it, a plugin that is a wrapper - the reference plugins are
// shell scripts, and a Python connector is typically a shim that execs
// the interpreter - loses only the wrapper when signalled, leaving the
// process doing the actual work running and still holding the stdout
// pipe the host is reading to EOF.
//
// Paired with signalTree below: the negative PID there is only
// correct because this ran. Do not separate them.
func ConfigureProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// TerminateProcessTree asks the plugin's process group to stop.
// SIGTERM is catchable, so a plugin can close connections and emit a
// final checkpoint before exiting.
func TerminateProcessTree(p *os.Process) error {
	return signalTree(p, syscall.SIGTERM)
}

// KillProcessTree stops the plugin's process group unconditionally.
// Used to sweep up anything still alive after the grace period.
func KillProcessTree(p *os.Process) error {
	return signalTree(p, syscall.SIGKILL)
}

func signalTree(p *os.Process, sig syscall.Signal) error {
	if p == nil || p.Pid <= 0 {
		return os.ErrProcessDone
	}
	if err := syscall.Kill(-p.Pid, sig); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}
