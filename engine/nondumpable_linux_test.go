package engine

import (
	"fmt"
	"os"
	"os/exec"
	"testing"
)

// A pipeline's code runs as a child of the engine. It must not be able to
// read the engine's environment, which holds the platform's secrets.
func TestChildCannotReadEngineEnvironment(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root may hold CAP_SYS_PTRACE here, which reads any process")
	}
	path := fmt.Sprintf("/proc/%d/environ", os.Getpid())
	out, err := exec.Command("cat", path).Output()
	if err == nil || len(out) > 0 {
		t.Fatalf("a child read %d bytes of the engine's environment; want it refused", len(out))
	}
}
