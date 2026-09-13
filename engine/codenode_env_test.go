package engine

import (
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/codeexec"
)

// A code node runs pipeline-author code. The pooled executor
// (pkg/codeexec/worker.go) has always filtered the environment it hands
// that code; the one-shot and streamed executors passed os.Environ()
// straight through. So which of two interchangeable executors happened to
// run a script decided whether the script could read the deployment's
// database URL, signing secret and encryption key.
//
// These assert the filter itself, which is the thing all three paths now
// share, rather than launching a subprocess per case.

func TestWorkerEnvWithholdsTheControlPlanesSecrets(t *testing.T) {
	for _, secret := range []string{
		"BROKOLI_ENCRYPTION_KEY", "BROKOLI_JWT_SECRET", "BROKOLI_DB_URL",
	} {
		t.Setenv(secret, "the-actual-secret-value")
	}
	t.Setenv("BROKOLI_CODE_PASS_ENV", "")

	for _, kv := range codeexec.WorkerEnv() {
		if strings.Contains(kv, "the-actual-secret-value") {
			name := strings.SplitN(kv, "=", 2)[0]
			t.Errorf("%s reached a code node's environment", name)
		}
	}
}

// What a script legitimately needs still arrives, or every code node
// breaks.
func TestWorkerEnvKeepsWhatAScriptNeeds(t *testing.T) {
	t.Setenv("BROKOLI_CODE_PASS_ENV", "")
	got := map[string]bool{}
	for _, kv := range codeexec.WorkerEnv() {
		got[strings.SplitN(kv, "=", 2)[0]] = true
	}
	for _, need := range []string{"PATH", "HOME"} {
		if !got[need] {
			t.Errorf("%s is missing from a code node's environment", need)
		}
	}
}

// And an operator can opt a name back in, which is the escape hatch for
// anyone who was relying on the unfiltered behaviour.
func TestWorkerEnvHonoursTheOptIn(t *testing.T) {
	t.Setenv("MY_PIPELINE_SETTING", "opted-in")
	t.Setenv("BROKOLI_CODE_PASS_ENV", "MY_PIPELINE_SETTING")

	found := false
	for _, kv := range codeexec.WorkerEnv() {
		if kv == "MY_PIPELINE_SETTING=opted-in" {
			found = true
		}
	}
	if !found {
		t.Error("an explicitly allowed variable did not reach the code node")
	}
}
