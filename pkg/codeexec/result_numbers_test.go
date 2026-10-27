//go:build unix

package codeexec

import (
	"context"
	"path/filepath"
	"testing"
)

// The pooled path's result frame was decoded with plain json.Unmarshal, so
// an integer a script returned inline came back as a float64, and one above
// 2^53 changed value. The one-shot path already kept integers exact.
func TestPoolKeepsIntegersInInlineResultsExact(t *testing.T) {
	p := testPool(t)
	script := `output_data = {"columns": ["id", "ratio"], "rows": [{"id": 9007199254740993, "ratio": 0.5}]}`
	res, err := p.Exec(context.Background(), inlineReq(script, []map[string]interface{}{{"a": float64(1)}}, filepath.Join(t.TempDir(), "o.ndjson")))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("rows = %+v, want one inline row", res.Rows)
	}
	if got, ok := res.Rows[0]["id"].(int64); !ok || got != 9007199254740993 {
		t.Fatalf("id = %v (%T), want int64 9007199254740993", res.Rows[0]["id"], res.Rows[0]["id"])
	}
	if got, ok := res.Rows[0]["ratio"].(float64); !ok || got != 0.5 {
		t.Fatalf("ratio = %v (%T), want float64 0.5", res.Rows[0]["ratio"], res.Rows[0]["ratio"])
	}
}
