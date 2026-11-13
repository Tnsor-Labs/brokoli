//go:build unix

package codeexec

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const envProbeScript = `
import os
output_data = {"columns": ["v"], "rows": [{"v": os.environ.get("BRK_SECRET_SENTINEL", "ABSENT")}]}
`

func execProbe(t *testing.T, p *Pool) string {
	t.Helper()
	req := Request{
		Script:       envProbeScript,
		Interpreter:  "python3",
		InlineRows:   []map[string]interface{}{{"a": float64(1)}},
		InputColumns: []string{"a"},
		OutputNDJSON: filepath.Join(t.TempDir(), "env.ndjson"),
		Timeout:      30 * time.Second,
	}
	res, err := p.Exec(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("probe result wrong: %+v", res)
	}
	return res.Rows[0]["v"].(string)
}

func TestWorkerEnvIsAnAllowlist(t *testing.T) {
	// The host process holds a secret; the worker must not see it.
	t.Setenv("BRK_SECRET_SENTINEL", "leaked")
	p := testPool(t)
	if got := execProbe(t, p); got != "ABSENT" {
		t.Fatalf("host env leaked into the worker: %q", got)
	}
}

func TestWorkerEnvPassThroughEscapeHatch(t *testing.T) {
	t.Setenv("BRK_SECRET_SENTINEL", "deliberate")
	t.Setenv("BROKOLI_CODE_PASS_ENV", "BRK_SECRET_SENTINEL")
	p := testPool(t)
	if got := execProbe(t, p); got != "deliberate" {
		t.Fatalf("pass_env did not pass the variable: %q", got)
	}
}

// The names no allowlist may pass reach no child, whether the operator
// passes the whole environment or names them: with them a script holds
// every tenant's credentials (the encryption key), can mint any session
// (the signing secret) and reach every row (the database URL).
func TestAllowlistedEnvNeverPassesTheServersOwnSecrets(t *testing.T) {
	denied := []string{"BROKOLI_ENCRYPTION_KEY", "BROKOLI_JWT_SECRET", "BROKOLI_DB_URL", "BROKOLI_LICENSE_SIGNING_SECRET"}
	for _, name := range denied {
		t.Setenv(name, "secret-"+name)
	}
	t.Setenv("BRK_ORDINARY", "kept")
	for _, pass := range []string{"*", strings.Join(denied, ",") + ",BRK_ORDINARY"} {
		t.Setenv("BRK_TEST_PASS_ENV", pass)
		got := map[string]string{}
		for _, kv := range AllowlistedEnv("BRK_TEST_PASS_ENV") {
			k, v, _ := strings.Cut(kv, "=")
			got[k] = v
		}
		for _, name := range denied {
			if _, ok := got[name]; ok {
				t.Errorf("pass=%q: %s reached the child", pass, name)
			}
		}
		if got["BRK_ORDINARY"] != "kept" {
			t.Errorf("pass=%q: an allowed variable was dropped", pass)
		}
	}
}
