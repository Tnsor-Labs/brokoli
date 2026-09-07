package engine

// ADR-033 acceptance gate: the reference adapters must execute tasks
// "without state, secret or descriptor leakage".
//
// These are the leakage half of that gate. Written as behaviour a task
// can actually observe from inside, not as assertions about how the
// worker is configured -- a task reading os.environ is the real
// question, and the only one an attacker would ask.

import (
	"strings"
	"testing"
)

// The gap this closes: task harnesses inherited os.Environ() wholesale,
// so user code could read whatever credentials the engine process held.
// ADR-029 found and fixed exactly this for code nodes
// (pkg/codeexec.WorkerEnv); task nodes reintroduced it.
func TestTaskCannotReadTheEnginesSecrets(t *testing.T) {
	skipIfNoPython3(t)
	t.Setenv("BROKOLI_TEST_DB_PASSWORD", "super-secret-value")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "another-secret")

	e := newTaskEngine(t)
	digest := e.bundle(t, `import os
def run():
    return "|".join(
        "%s=%s" % (k, os.environ[k])
        for k in ("BROKOLI_TEST_DB_PASSWORD", "AWS_SECRET_ACCESS_KEY")
        if k in os.environ
    ) or "<none visible>"
`)
	run, err := e.runPipeline(t, "p-no-secret-leak", digest, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	got, _ := e.firstTaskRow(t, run)["result"].(string)
	if strings.Contains(got, "secret") {
		t.Errorf("task read the engine's credentials: %q", got)
	}
}

// The allowlist has to remain usable: a task still needs PATH, or it
// cannot find the interpreter it was launched with.
func TestTaskStillSeesTheAllowlistedEnvironment(t *testing.T) {
	skipIfNoPython3(t)
	e := newTaskEngine(t)
	digest := e.bundle(t, "import os\ndef run():\n    return 'PATH' in os.environ\n")
	run, err := e.runPipeline(t, "p-allowlist-kept", digest, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := e.firstTaskRow(t, run)["result"]; got != true {
		t.Errorf("PATH not visible to a task (%v); the allowlist is too narrow to run one", got)
	}
}

// Fresh child per attempt (ADR-033's model): a task must not observe
// anything a previous task left behind in the interpreter. Two runs of
// the same bundle, the first setting module state, the second reading
// it.
func TestTaskDoesNotInheritStateFromAnEarlierAttempt(t *testing.T) {
	skipIfNoPython3(t)
	e := newTaskEngine(t)
	digest := e.bundle(t, `import os, tempfile
_marker = os.path.join(tempfile.gettempdir(), "brokoli-task-state-probe")

def run():
    # A module-level global set by a previous run in the SAME
    # interpreter would still be here; a fresh child sees nothing.
    seen = globals().get("_leaked_from_previous_run", "<clean>")
    globals()["_leaked_from_previous_run"] = "contaminated"
    return seen
`)
	for i, id := range []string{"p-state-1", "p-state-2"} {
		run, err := e.runPipeline(t, id, digest, nil)
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if got := e.firstTaskRow(t, run)["result"]; got != "<clean>" {
			t.Errorf("run %d saw state from an earlier attempt: %v", i, got)
		}
	}
}
