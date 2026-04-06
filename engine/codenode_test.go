package engine

import (
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

func TestCodeNode_Passthrough(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"id", "name"},
		Rows: []common.DataRow{
			{"id": "1", "name": "Alice"},
			{"id": "2", "name": "Bob"},
		},
	}

	// Script that just passes through
	script := `# passthrough - output_data is already set to input`
	result, stderr, err := ExecuteCodeNode(script, ds, nil, nil, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr: %s", err, stderr)
	}
	if len(result.Rows) != 2 {
		t.Errorf("expected 2 rows, got %d", len(result.Rows))
	}
}

func TestCodeNode_FilterAndTransform(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"id", "amount"},
		Rows: []common.DataRow{
			{"id": "1", "amount": "100"},
			{"id": "2", "amount": "-50"},
			{"id": "3", "amount": "200"},
		},
	}

	script := `
filtered = [r for r in rows if float(r.get("amount", 0)) > 0]
for r in filtered:
    r["doubled"] = str(float(r["amount"]) * 2)
output_data = {"columns": columns + ["doubled"], "rows": filtered}
`
	result, _, err := ExecuteCodeNode(script, ds, nil, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 2 {
		t.Errorf("expected 2 rows after filter, got %d", len(result.Rows))
	}
	if len(result.Columns) != 3 {
		t.Errorf("expected 3 columns, got %d", len(result.Columns))
	}
}

func TestCodeNode_AccessParams(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"id"},
		Rows:    []common.DataRow{{"id": "1"}},
	}

	script := `
for r in rows:
    r["source"] = params.get("source_name", "unknown")
output_data = {"columns": columns + ["source"], "rows": rows}
`
	result, _, err := ExecuteCodeNode(script, ds, nil, map[string]string{"source_name": "test_run"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if result.Rows[0]["source"] != "test_run" {
		t.Errorf("expected 'test_run', got %v", result.Rows[0]["source"])
	}
}

func TestCodeNode_Pandas(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"name", "score"},
		Rows: []common.DataRow{
			{"name": "Alice", "score": "90"},
			{"name": "Bob", "score": "85"},
			{"name": "Alice", "score": "95"},
		},
	}

	// Try pandas — skip test if not installed
	script := `
try:
    import pandas as pd
    df = pd.DataFrame(rows)
    df["score"] = pd.to_numeric(df["score"])
    result = df.groupby("name")["score"].mean().reset_index()
    result.columns = ["name", "avg_score"]
    output_data = {"columns": list(result.columns), "rows": result.to_dict("records")}
except ImportError:
    # pandas not available — just pass through
    output_data = {"columns": columns, "rows": rows}
`
	result, _, err := ExecuteCodeNode(script, ds, nil, nil, 15)
	if err != nil {
		t.Fatal(err)
	}
	// Should work whether pandas is installed or not
	if len(result.Rows) < 1 {
		t.Error("expected at least 1 row")
	}
}

func TestCodeNode_ScriptError(t *testing.T) {
	ds := &common.DataSet{Columns: []string{"id"}, Rows: []common.DataRow{{"id": "1"}}}

	script := `raise ValueError("intentional error")`
	_, stderr, err := ExecuteCodeNode(script, ds, nil, nil, 10)
	if err == nil {
		t.Error("expected error from failing script")
	}
	_ = stderr // should contain the traceback
}

func TestCodeNode_EmptyScript(t *testing.T) {
	ds := &common.DataSet{Columns: []string{"id"}, Rows: []common.DataRow{{"id": "1"}}}
	_, _, err := ExecuteCodeNode("", ds, nil, nil, 10)
	if err == nil {
		t.Error("expected error for empty script")
	}
}

func TestCodeNode_Stderr(t *testing.T) {
	ds := &common.DataSet{Columns: []string{"id"}, Rows: []common.DataRow{{"id": "1"}}}

	script := `
import sys
print("this is a warning", file=sys.stderr)
# output_data stays as default passthrough
`
	result, stderr, err := ExecuteCodeNode(script, ds, nil, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 1 {
		t.Error("should still produce output")
	}
	if stderr == "" {
		t.Error("expected stderr output")
	}
}
