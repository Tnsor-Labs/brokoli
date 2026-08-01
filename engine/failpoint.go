package engine

import (
	"flag"
	"os"
)

const (
	crashPointBeforeRunCreate                 = "before_run_create"
	crashPointAfterRunCreate                  = "after_run_create"
	crashPointBeforeNodeAttemptCreate         = "before_node_attempt_create"
	crashPointAfterNodeAttemptCreate          = "after_node_attempt_create"
	crashPointBeforeNodeExecution             = "before_node_execution"
	crashPointAfterNodeExecutionBeforePersist = "after_node_execution_before_persist"
	crashPointAfterNodeCompletionPersist      = "after_node_completion_persist"
	crashPointBeforeRunTerminalPersist        = "before_run_terminal_persist"
	crashPointAfterRunTerminalPersist         = "after_run_terminal_persist"
)

const testCrashExitCode = 86

// crashAt terminates a test subprocess at a named execution boundary.
//
// It is deliberately inert outside `go test`: both opt-in environment
// variables and the test binary's flag set are required. A point may be
// qualified with a node ID, for example
// "after_node_execution_before_persist:sink".
func crashAt(point, nodeID string) {
	if os.Getenv("BROKOLI_ENABLE_TEST_FAILPOINTS") != "1" || flag.Lookup("test.v") == nil {
		return
	}

	configured := os.Getenv("BROKOLI_TEST_CRASH_POINT")
	if configured != point && (nodeID == "" || configured != point+":"+nodeID) {
		return
	}

	os.Exit(testCrashExitCode)
}
