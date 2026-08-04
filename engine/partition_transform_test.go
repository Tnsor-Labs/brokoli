package engine

import (
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

func TestExecutePartitionTransform_DatasetMap(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"id", "amount"},
		Rows: []common.DataRow{
			{"id": "1", "amount": "10"},
			{"id": "2", "amount": "20"},
		},
	}
	cfg := map[string]interface{}{
		"function": map[string]interface{}{
			"name": "double_amount",
			"doc":  "Doubles the amount field.",
			"script": `
for r in rows:
    r["amount"] = str(float(r["amount"]) * 2)
output_data = {"columns": columns, "rows": rows}
`,
		},
	}

	result, fnName, _, err := ExecutePartitionTransform(cfg, ds, nil, "dataset_map")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fnName != "double_amount" {
		t.Errorf("fnName = %q, want %q", fnName, "double_amount")
	}
	if len(result.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(result.Rows))
	}
	if result.Rows[0]["amount"] != "20.0" || result.Rows[1]["amount"] != "40.0" {
		t.Errorf("unexpected mapped values: %+v", result.Rows)
	}
}

func TestExecutePartitionTransform_DatasetFilter(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"id", "amount"},
		Rows: []common.DataRow{
			{"id": "1", "amount": "100"},
			{"id": "2", "amount": "-50"},
			{"id": "3", "amount": "200"},
		},
	}
	cfg := map[string]interface{}{
		"function": map[string]interface{}{
			"name": "is_valid",
			"script": `
filtered = [r for r in rows if float(r.get("amount", 0)) > 0]
output_data = {"columns": columns, "rows": filtered}
`,
		},
	}

	result, fnName, _, err := ExecutePartitionTransform(cfg, ds, nil, "dataset_filter")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fnName != "is_valid" {
		t.Errorf("fnName = %q, want %q", fnName, "is_valid")
	}
	if len(result.Rows) != 2 {
		t.Fatalf("expected 2 rows after filter, got %d: %+v", len(result.Rows), result.Rows)
	}
	for _, r := range result.Rows {
		if r["id"] == "2" {
			t.Errorf("row with id=2 (negative amount) should have been filtered out")
		}
	}
}

// TestExecutePartitionTransform_MissingFunction covers the "malformed
// dataset_map/dataset_filter fails clearly" acceptance criterion at the
// execution layer (mirrors the deploy-time check in validate_test.go).
func TestExecutePartitionTransform_MissingFunction(t *testing.T) {
	ds := &common.DataSet{Columns: []string{"id"}, Rows: []common.DataRow{{"id": "1"}}}

	_, _, _, err := ExecutePartitionTransform(map[string]interface{}{}, ds, nil, "dataset_map")
	if err == nil {
		t.Fatal("expected error for missing 'function' reference, got nil")
	}
	if !strings.Contains(err.Error(), "function") {
		t.Errorf("error should mention the missing function reference, got: %v", err)
	}
}

func TestExecutePartitionTransform_InvalidFunctionShape(t *testing.T) {
	ds := &common.DataSet{Columns: []string{"id"}, Rows: []common.DataRow{{"id": "1"}}}

	cfg := map[string]interface{}{"function": "not-an-object"}
	if _, _, _, err := ExecutePartitionTransform(cfg, ds, nil, "dataset_filter"); err == nil {
		t.Fatal("expected error for non-object 'function' config, got nil")
	}

	cfg = map[string]interface{}{"function": map[string]interface{}{}}
	if _, _, _, err := ExecutePartitionTransform(cfg, ds, nil, "dataset_filter"); err == nil {
		t.Fatal("expected error for 'function' object missing 'name', got nil")
	}
}

// TestExecutePartitionTransform_NameOnlyReference is the important
// regression/documentation test: it reproduces EXACTLY the config shape
// brokoli-sdk's DatasetRef.map()/.filter() actually emit today
// (pipeline.py: _func_ref -> {"name": ..., "doc": ...}, no "script") and
// asserts this fails with a clear, specific, actionable error instead of
// either (a) silently passing the input straight through (the pre-fix
// behavior for these node types) or (b) panicking / failing opaquely.
func TestExecutePartitionTransform_NameOnlyReference(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"id"},
		Rows:    []common.DataRow{{"id": "1"}},
	}
	// Exactly what brokoli-sdk's _func_ref(fn) serializes: name + first
	// line of the docstring, nothing runnable.
	cfg := map[string]interface{}{
		"function": map[string]interface{}{
			"name": "normalize",
			"doc":  "Normalize a row.",
		},
	}

	result, fnName, _, err := ExecutePartitionTransform(cfg, ds, nil, "dataset_map")
	if err == nil {
		t.Fatal("expected an execution error for a name-only function reference (no script), got nil — " +
			"this must not silently pass through")
	}
	if result != nil {
		t.Errorf("expected nil result on error, got %+v", result)
	}
	if fnName != "normalize" {
		t.Errorf("fnName = %q, want %q (should still be reported even on failure)", fnName, "normalize")
	}
	if !strings.Contains(err.Error(), "normalize") || !strings.Contains(err.Error(), "script") {
		t.Errorf("error should name the function and mention the missing script, got: %v", err)
	}
}
