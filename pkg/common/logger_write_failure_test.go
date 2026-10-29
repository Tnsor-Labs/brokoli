package common

import (
	"errors"
	"os"
	"os/exec"
	"testing"
)

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("no space left on device") }

// A log write that fails must not end the process. It went to
// errors.CheckError, which calls log.Fatalf, so a full disk under the log
// file took the server down at its next log line.
func TestAFailedLogWriteDoesNotExit(t *testing.T) {
	if os.Getenv("BROKOLI_LOGGER_CHILD") == "1" {
		l := NewLoggerWithWriter(failingWriter{}, LogLevelDebug)
		l.Debug("d")
		l.Info("i")
		l.Warning("w")
		l.Error("e")
		l.Progress("p")
		os.Exit(0)
	}
	// Run in a child: an exit would end this test binary, not fail a test.
	cmd := exec.Command(os.Args[0], "-test.run=^TestAFailedLogWriteDoesNotExit$")
	cmd.Env = append(os.Environ(), "BROKOLI_LOGGER_CHILD=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("logging to a failing writer ended the process: %v\n%s", err, out)
	}
}
