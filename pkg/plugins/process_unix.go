//go:build unix

package plugins

import (
	"os"
	"os/exec"

	"github.com/Tnsor-Labs/brokoli/pkg/proctree"
)

// Thin delegates: the process-group contract moved to pkg/proctree so
// ADR-029's code-node workers share it. See that package for the full
// reasoning (group leadership, negative-PID signalling, the orphan bug
// ADR-013 documents).

func configureProcessGroup(cmd *exec.Cmd) { proctree.ConfigureProcessGroup(cmd) }

func terminateProcessTree(p *os.Process) error { return proctree.TerminateProcessTree(p) }

func killProcessTree(p *os.Process) error { return proctree.KillProcessTree(p) }
