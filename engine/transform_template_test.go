package engine

import (
	"encoding/json"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// TestTransformRule_UITemplateShapes locks in the JSON shape for
// every rule type the UI templates produce. The bug that motivated
// this test: a transform template in ui/src/pages/Pipelines.svelte
// shipped a rename rule as
//   { type: "rename", old_name: "hire_date", new_name: "start_date" }
// but the backend TransformRule expects
//   { type: "rename", mapping: { hire_date: "start_date" } }
// So the deserialization silently produced an empty Mapping and the
// rule errored at runtime with "rename_columns requires mapping".
// That template had been broken for however long it had existed
// because nothing exercised the UI-produced JSON against the engine.
//
// This test keeps a copy of the canonical shape for each rule type
// the UI can emit, deserializes it into []TransformRule, and runs
// ApplyTransforms. If someone adds a new transform rule type or
// changes an existing one, they'll get a failure here instead of
// finding out at runtime.
func TestTransformRule_UITemplateShapes(t *testing.T) {
	cases := []struct {
		name       string
		ruleJSON   string
		initCols   []string
		initRow    common.DataRow
		assertCols []string
	}{
		{
			name:       "rename_columns_via_mapping",
			ruleJSON:   `{"type": "rename_columns", "mapping": {"hire_date": "start_date"}}`,
			initCols:   []string{"id", "hire_date"},
			initRow:    common.DataRow{"id": "1", "hire_date": "2024-01-15"},
			assertCols: []string{"id", "start_date"},
		},
		{
			name:       "rename_alias",
			ruleJSON:   `{"type": "rename", "mapping": {"hire_date": "start_date"}}`,
			initCols:   []string{"id", "hire_date"},
			initRow:    common.DataRow{"id": "1", "hire_date": "2024-01-15"},
			assertCols: []string{"id", "start_date"},
		},
		{
			name:       "drop_columns",
			ruleJSON:   `{"type": "drop_columns", "columns": ["internal_notes"]}`,
			initCols:   []string{"id", "name", "internal_notes"},
			initRow:    common.DataRow{"id": "1", "name": "alice", "internal_notes": "x"},
			assertCols: []string{"id", "name"},
		},
		{
			name:       "add_column",
			ruleJSON:   `{"type": "add_column", "name": "full_name", "expression": "first + ' ' + last"}`,
			initCols:   []string{"first", "last"},
			initRow:    common.DataRow{"first": "Alice", "last": "Smith"},
			assertCols: []string{"first", "last", "full_name"},
		},
		{
			name:       "replace_values",
			ruleJSON:   `{"type": "replace_values", "column": "status", "mapping": {"active": "enabled"}}`,
			initCols:   []string{"status"},
			initRow:    common.DataRow{"status": "active"},
			assertCols: []string{"status"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Mimic the exact unmarshal path node_handlers.go:runTransform
			// uses: parse the raw JSON into []TransformRule.
			var rules []TransformRule
			if err := json.Unmarshal([]byte("["+tc.ruleJSON+"]"), &rules); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if len(rules) != 1 {
				t.Fatalf("expected 1 rule, got %d", len(rules))
			}

			ds := &common.DataSet{
				Columns: append([]string{}, tc.initCols...),
				Rows:    []common.DataRow{tc.initRow},
			}
			if err := ApplyTransforms(rules, ds); err != nil {
				t.Fatalf("ApplyTransforms: %v", err)
			}

			if len(ds.Columns) != len(tc.assertCols) {
				t.Fatalf("columns after transform: got %v, want %v", ds.Columns, tc.assertCols)
			}
			for i, col := range tc.assertCols {
				if ds.Columns[i] != col {
					t.Errorf("column[%d]: got %q, want %q", i, ds.Columns[i], col)
				}
			}
		})
	}
}

// TestTransformRule_RenameWithWrongShapeFails is the direct regression
// for the specific bug. The OLD (broken) template shape must produce
// a clear error, not silently pass.
func TestTransformRule_RenameWithWrongShapeFails(t *testing.T) {
	// The pre-fix template shape — old_name/new_name instead of mapping.
	badJSON := `[{"type": "rename", "old_name": "hire_date", "new_name": "start_date"}]`
	var rules []TransformRule
	if err := json.Unmarshal([]byte(badJSON), &rules); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	ds := &common.DataSet{
		Columns: []string{"id", "hire_date"},
		Rows:    []common.DataRow{{"id": "1", "hire_date": "2024-01-15"}},
	}
	err := ApplyTransforms(rules, ds)
	if err == nil {
		t.Fatal("expected rename with old_name/new_name shape to fail, got nil")
	}
	// The error should mention "mapping" so a debugging user can figure
	// out what's missing without reading source.
	if !contains(err.Error(), "mapping") {
		t.Errorf("error should mention 'mapping', got: %v", err)
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
