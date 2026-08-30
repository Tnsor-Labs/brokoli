//go:build !unix

package plugins

import (
	"os"
	"os/exec"

	"github.com/Tnsor-Labs/brokoli/pkg/proctree"
)

// Same delegation as process_unix.go; pkg/proctree carries the
// platform split now.

func configureProcessGroup(cmd *exec.Cmd) { proctree.ConfigureProcessGroup(cmd) }

func terminateProcessTree(p *os.Process) error { return proctree.TerminateProcessTree(p) }

func killProcessTree(p *os.Process) error { return proctree.KillProcessTree(p) }
