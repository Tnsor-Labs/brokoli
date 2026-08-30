//go:build !unix

package proctree

import (
	"os"
	"os/exec"
)

// Non-Unix platforms get a reduced version of the cancellation
// contract. os.Process supports only Kill there — there is no portable
// SIGTERM, so a plugin gets no chance to shut down cleanly — and
// killing a whole process tree needs Job Objects on Windows, which is
// its own piece of work rather than a variation on this one.
//
// exec.Cmd.WaitDelay still applies on every platform, so Run cannot
// block forever regardless of which file is compiled in.
//
// The constraint is !unix rather than windows so the package still
// builds on plan9, js/wasm and anything else added later.

// ConfigureProcessGroup is a no-op: process groups are not portable.
func ConfigureProcessGroup(cmd *exec.Cmd) {}

// TerminateProcessTree kills the process. There is no graceful signal
// to send here, so this is identical to KillProcessTree.
func TerminateProcessTree(p *os.Process) error {
	if p == nil {
		return os.ErrProcessDone
	}
	return p.Kill()
}

// KillProcessTree kills the process, but not anything it spawned.
func KillProcessTree(p *os.Process) error {
	if p == nil {
		return os.ErrProcessDone
	}
	return p.Kill()
}
