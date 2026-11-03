package codeexec

import (
	"context"
	"path/filepath"
	"testing"
)

// A warm worker keeps what the last script left in its process. One
// tenant's script must never run in a worker another tenant's has had, or
// it could leave something behind that reads the next tenant's rows.
func TestPoolKeepsTenantsApart(t *testing.T) {
	p := testPool(t)
	dir := t.TempDir()
	plant := `import builtins
builtins.left_behind = True
output_data = {"columns": columns, "rows": rows}`
	look := `import builtins
output_data = {"columns": ["a"], "rows": [{"a": 1 if hasattr(builtins, "left_behind") else 0}]}`
	rows := []map[string]interface{}{{"a": float64(1)}}
	alpha := WithTenant(context.Background(), "org-a/ws")
	beta := WithTenant(context.Background(), "org-b/ws")

	if _, err := p.Exec(alpha, inlineReq(plant, rows, filepath.Join(dir, "1.ndjson"))); err != nil {
		t.Fatal(err)
	}
	other, err := p.Exec(beta, inlineReq(look, rows, filepath.Join(dir, "2.ndjson")))
	if err != nil {
		t.Fatal(err)
	}
	if other.Meta.Warm || other.Rows[0]["a"] != int64(0) {
		t.Fatalf("another tenant got the first tenant's worker: warm=%v rows=%v", other.Meta.Warm, other.Rows)
	}
	// The same tenant does get its warm worker back, so the separation
	// above is the tenant key and not a pool that never reuses.
	same, err := p.Exec(alpha, inlineReq(look, rows, filepath.Join(dir, "3.ndjson")))
	if err != nil {
		t.Fatal(err)
	}
	if !same.Meta.Warm || same.Rows[0]["a"] != int64(1) {
		t.Fatalf("the same tenant: warm=%v rows=%v; want its warm worker", same.Meta.Warm, same.Rows)
	}
}
