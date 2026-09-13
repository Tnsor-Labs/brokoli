package engine

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// The real guard for the call sites.
//
// A test against codeexec.WorkerEnv() proves the filter works and does
// NOT prove any executor uses it -- reverting engine/codenode.go to
// os.Environ() leaves such a test green, which is how the gap arrived in
// the first place. This runs an actual script and asks it what it can
// see.
func TestCodeNodeCannotReadTheControlPlanesSecrets(t *testing.T) {
	// Both executors. The pooled one always filtered, so a test that runs
	// only the default proves nothing about the change that fixed the
	// other two -- reverting engine/codenode.go leaves it green.
	for _, pool := range []string{"1", "0"} {
		t.Run("pool="+pool, func(t *testing.T) {
			t.Setenv("BROKOLI_CODE_POOL", pool)
			assertCodeNodeSeesNoSecrets(t)
		})
	}
}

func assertCodeNodeSeesNoSecrets(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	const canary = "canary-value-must-not-reach-user-code"
	t.Setenv("BROKOLI_JWT_SECRET", canary)
	t.Setenv("BROKOLI_ENCRYPTION_KEY", canary)
	t.Setenv("BROKOLI_DB_URL", canary)
	t.Setenv("BROKOLI_CODE_PASS_ENV", "")

	// The script reports what it found rather than the value, so a
	// failure message never carries a secret even when the secret is a
	// test canary.
	script := `
import os
names = ["BROKOLI_JWT_SECRET", "BROKOLI_ENCRYPTION_KEY", "BROKOLI_DB_URL"]
seen = [n for n in names if os.environ.get(n)]
output_data = {"columns": ["seen"], "rows": [{"seen": ",".join(seen)}]}
`
	out, _, err := ExecuteCodeNode(script, &common.DataSet{Columns: []string{}, Rows: []common.DataRow{}},
		map[string]interface{}{}, nil, nil, 60)
	if err != nil {
		t.Fatalf("code node failed: %v", err)
	}
	if len(out.Rows) != 1 {
		t.Fatalf("expected one row, got %d", len(out.Rows))
	}
	if seen, _ := out.Rows[0]["seen"].(string); seen != "" {
		t.Errorf("a code node read the control plane's secrets: %s", seen)
	}
}

// A script must still see what it needs, or the filter has broken every
// code node rather than secured it.
func TestCodeNodeStillSeesItsOwnEnvironment(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	t.Setenv("BROKOLI_CODE_PASS_ENV", "")
	script := `
import os
output_data = {"columns": ["has_path"], "rows": [{"has_path": bool(os.environ.get("PATH"))}]}
`
	out, _, err := ExecuteCodeNode(script, &common.DataSet{Columns: []string{}, Rows: []common.DataRow{}},
		map[string]interface{}{}, nil, nil, 60)
	if err != nil {
		t.Fatalf("code node failed: %v", err)
	}
	if v, _ := out.Rows[0]["has_path"].(bool); !v {
		t.Error("PATH did not reach the script; the filter is too aggressive")
	}
}

// The opt-in has to work end to end, not just inside WorkerEnv.
func TestCodeNodeSeesAnExplicitlyAllowedVariable(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	// One-shot path: a pooled worker reads BROKOLI_CODE_PASS_ENV when it
	// starts, so a value set here would not reach a pool that is already
	// running. Worth knowing in its own right -- the opt-in is a
	// deploy-time setting for the pooled default, not a per-run one.
	t.Setenv("BROKOLI_CODE_POOL", "0")
	t.Setenv("MY_PIPELINE_SETTING", "opted-in")
	t.Setenv("BROKOLI_CODE_PASS_ENV", "MY_PIPELINE_SETTING")
	script := `
import os
output_data = {"columns": ["v"], "rows": [{"v": os.environ.get("MY_PIPELINE_SETTING", "")}]}
`
	out, _, err := ExecuteCodeNode(script, &common.DataSet{Columns: []string{}, Rows: []common.DataRow{}},
		map[string]interface{}{}, nil, nil, 60)
	if err != nil {
		t.Fatalf("code node failed: %v", err)
	}
	if v, _ := out.Rows[0]["v"].(string); !strings.Contains(v, "opted-in") {
		t.Errorf("an allowed variable did not reach the script, got %q", v)
	}
}
