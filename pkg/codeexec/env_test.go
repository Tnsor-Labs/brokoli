//go:build unix

package codeexec

import (
	"context"
	"path/filepath"
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
