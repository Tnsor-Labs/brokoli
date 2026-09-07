package engine

// ADR-033 acceptance gate: the reference adapters must execute tasks
// "without state, secret or descriptor leakage".
//
// These are the leakage half of that gate. Written as behaviour a task
// can actually observe from inside, not as assertions about how the
// worker is configured -- a task reading os.environ is the real
// question, and the only one an attacker would ask.

import (
	"os"
	"path/filepath"
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

// The descriptor half of ADR-033's "no state, secret or descriptor
// leakage" gate.
//
// Go opens files with O_CLOEXEC and exec.Cmd passes only stdin, stdout,
// stderr plus any explicit ExtraFiles (the harness sets none), so the
// property should hold by construction. "Should hold by construction"
// is exactly the kind of claim worth a test: it is one ExtraFiles line,
// or one file opened by a future dependency without CLOEXEC, away from
// being false, and nothing else would notice.
func TestTaskCannotReachTheEnginesFileDescriptors(t *testing.T) {
	skipIfNoPython3(t)

	// A file the ENGINE holds open for the whole run, standing in for a
	// blob handle, a database socket, or a credentials file.
	secretPath := filepath.Join(t.TempDir(), "engine-held.txt")
	if err := os.WriteFile(secretPath, []byte("engine-only-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	held, err := os.Open(secretPath)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()

	e := newTaskEngine(t)
	// Walk every descriptor a child could plausibly have inherited and
	// report anything readable beyond the three standard streams.
	digest := e.bundle(t, `import os
def run():
    leaked = []
    for fd in range(3, 64):
        try:
            os.fstat(fd)
        except OSError:
            continue
        try:
            with open(fd, "rb", closefd=False) as f:
                f.seek(0)
                head = f.read(64)
            leaked.append("%d:%s" % (fd, head[:32]))
        except Exception as exc:
            leaked.append("%d:<open %s>" % (fd, type(exc).__name__))
    return "|".join(leaked) or "<none>"
`)
	run, rerr := e.runPipeline(t, "p-no-fd-leak", digest, nil)
	if rerr != nil {
		t.Fatalf("run: %v", rerr)
	}
	got, _ := e.firstTaskRow(t, run)["result"].(string)
	// Strictly "<none>", not merely "does not contain the fixture's
	// secret". An earlier version of this asserted only the latter and
	// still PASSED when a descriptor was deliberately leaked into the
	// child -- a leaked database socket or blob handle would not have
	// contained the fixture string either. Any descriptor beyond the
	// three standard streams is the failure.
	if got != "<none>" {
		t.Errorf("task inherited descriptors beyond stdin/stdout/stderr: %s", got)
	}
	// Keep the engine's handle alive until after the task ran, so the
	// test would actually have something to find if inheritance leaked.
	if _, err := held.Stat(); err != nil {
		t.Fatalf("engine handle closed early, so this proved nothing: %v", err)
	}
	t.Logf("descriptors visible to the task: %s", got)
}
