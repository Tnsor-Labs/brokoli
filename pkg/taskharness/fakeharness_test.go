package taskharness

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestFakeHarnessHelperProcess is not a real test -- it's the re-exec
// target for fakeHarnessCommand, following the standard library's own
// os/exec test pattern (TestHelperProcess) so these protocol tests don't
// need a real Python interpreter to drive Run's state machine. It emits
// exactly the frames its script says and nothing this package doesn't
// already control, so a test can pin the exact wire bytes Run reacts to.
func TestFakeHarnessHelperProcess(t *testing.T) {
	if os.Getenv("BROKOLI_TASKHARNESS_HELPER") != "1" {
		return
	}
	defer os.Exit(0)
	runFakeHarnessScript(os.Getenv("BROKOLI_TASKHARNESS_SCRIPT"))
}

// fakeHarnessCommand returns an Options.Command/Env pair that re-execs
// this test binary as a fake harness running script.
func fakeHarnessCommand(script string) ([]string, []string) {
	cmd := []string{os.Args[0], "-test.run=TestFakeHarnessHelperProcess"}
	env := append(os.Environ(),
		"BROKOLI_TASKHARNESS_HELPER=1",
		"BROKOLI_TASKHARNESS_SCRIPT="+script,
	)
	return cmd, env
}

// runFakeHarnessScript interprets a tiny '|'-separated directive
// language so each test can pin an exact frame sequence:
//
//	ready                    -- emit a well-formed ready frame
//	log:<level>:<message>    -- emit a log frame
//	completed                -- emit a completed frame
//	failed:<category>:<msg>  -- emit a failed frame
//	raw:<literal>            -- emit the literal line verbatim (malformed-frame tests)
//	sleep:<duration>         -- pause (e.g. to blow a handshake/cancel timeout)
//	exit:<code>              -- exit immediately with code, no further output
//	read_cancel_ack          -- block for one stdin line, then reply cancel_ack
//	spin                     -- CPU-bound busy loop (rlimit tests); killed externally
func runFakeHarnessScript(script string) {
	w := bufio.NewWriter(os.Stdout)
	reader := bufio.NewReader(os.Stdin)
	for _, directive := range strings.Split(script, "|") {
		if directive == "" {
			continue
		}
		name, arg, _ := strings.Cut(directive, ":")
		switch name {
		case "ready":
			fmt.Fprint(w, `{"type":"ready","protocol":"brokoli.task-runtime/v1","adapter":"fake-harness","adapter_version":"1.0.0","capabilities":[]}`+"\n")
		case "log":
			level, msg, _ := strings.Cut(arg, ":")
			fmt.Fprintf(w, "{\"type\":\"log\",\"level\":%q,\"message\":%q}\n", level, msg)
		case "completed":
			fmt.Fprint(w, `{"type":"completed"}`+"\n")
		case "failed":
			category, msg, _ := strings.Cut(arg, ":")
			fmt.Fprintf(w, "{\"type\":\"failed\",\"failure\":{\"category\":%q,\"code\":\"fake\",\"message\":%q,\"retryable\":false}}\n", category, msg)
		case "raw":
			fmt.Fprint(w, arg+"\n")
		case "sleep":
			w.Flush()
			d, err := time.ParseDuration(arg)
			if err == nil {
				time.Sleep(d)
			}
		case "exit":
			w.Flush()
			code, _ := strconv.Atoi(arg)
			os.Exit(code)
		case "read_cancel_ack":
			w.Flush()
			_, _ = reader.ReadString('\n')
			fmt.Fprint(w, `{"type":"cancel_ack"}`+"\n")
		case "spin":
			w.Flush()
			deadline := time.Now().Add(30 * time.Second)
			for time.Now().Before(deadline) {
				// Deliberately CPU-bound; an rlimit test kills this well
				// before the deadline.
			}
		}
		w.Flush()
	}
}
