package engine

import (
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

func sampleDS() *common.DataSet {
	return &common.DataSet{
		Columns: []string{"id", "name", "email", "amount", "status"},
		Rows: []common.DataRow{
			{"id": "1", "name": "Alice", "email": "ALICE@EXAMPLE.COM", "amount": "150.00", "status": "active"},
			{"id": "2", "name": "Bob", "email": "BOB@EXAMPLE.COM", "amount": "250.50", "status": "active"},
			{"id": "3", "name": "Charlie", "email": "charlie@example.com", "amount": "75.25", "status": "inactive"},
			{"id": "4", "name": "Diana", "email": "diana@example.com", "amount": "-10.00", "status": "active"},
		},
	}
}

func TestRenameColumns(t *testing.T) {
	ds := sampleDS()
	err := ApplyTransforms([]TransformRule{
		{Type: "rename_columns", Mapping: map[string]string{"name": "full_name", "email": "contact_email"}},
	}, ds)
	if err != nil {
		t.Fatal(err)
	}
	if ds.Columns[1] != "full_name" {
		t.Errorf("expected column 'full_name', got %q", ds.Columns[1])
	}
	if ds.Rows[0]["full_name"] != "Alice" {
		t.Errorf("expected row value 'Alice', got %v", ds.Rows[0]["full_name"])
	}
	if _, ok := ds.Rows[0]["name"]; ok {
		t.Error("old column 'name' should be deleted")
	}
}

func TestFilterRows_In(t *testing.T) {
	ds := sampleDS()
	err := ApplyTransforms([]TransformRule{
		{Type: "filter_rows", Condition: "status in [active]"},
	}, ds)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds.Rows) != 3 {
		t.Errorf("expected 3 rows after filter, got %d", len(ds.Rows))
	}
}

func TestFilterRows_NotEqual(t *testing.T) {
	ds := sampleDS()
	err := ApplyTransforms([]TransformRule{
		{Type: "filter_rows", Condition: "status != inactive"},
	}, ds)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds.Rows) != 3 {
		t.Errorf("expected 3 rows, got %d", len(ds.Rows))
	}
}

func TestFilterRows_Equal(t *testing.T) {
	ds := sampleDS()
	err := ApplyTransforms([]TransformRule{
		{Type: "filter_rows", Condition: "name = Alice"},
	}, ds)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds.Rows) != 1 {
		t.Errorf("expected 1 row, got %d", len(ds.Rows))
	}
}

func TestDropColumns(t *testing.T) {
	ds := sampleDS()
	err := ApplyTransforms([]TransformRule{
		{Type: "drop_columns", Columns: []string{"status", "email"}},
	}, ds)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds.Columns) != 3 {
		t.Errorf("expected 3 columns, got %d: %v", len(ds.Columns), ds.Columns)
	}
	if _, ok := ds.Rows[0]["status"]; ok {
		t.Error("status column should be removed from rows")
	}
}

func TestApplyFunction(t *testing.T) {
	ds := sampleDS()
	err := ApplyTransforms([]TransformRule{
		{Type: "apply_function", Column: "email", Function: "lower"},
	}, ds)
	if err != nil {
		t.Fatal(err)
	}
	if ds.Rows[0]["email"] != "alice@example.com" {
		t.Errorf("expected lowercased email, got %v", ds.Rows[0]["email"])
	}
}

func TestApplyFunction_Upper(t *testing.T) {
	ds := sampleDS()
	err := ApplyTransforms([]TransformRule{
		{Type: "apply_function", Column: "name", Function: "upper"},
	}, ds)
	if err != nil {
		t.Fatal(err)
	}
	if ds.Rows[0]["name"] != "ALICE" {
		t.Errorf("expected 'ALICE', got %v", ds.Rows[0]["name"])
	}
}

func TestAddColumn(t *testing.T) {
	ds := sampleDS()
	err := ApplyTransforms([]TransformRule{
		{Type: "add_column", Name: "greeting", Expression: "name + ' - ' + status"},
	}, ds)
	if err != nil {
		t.Fatal(err)
	}
	if ds.Rows[0]["greeting"] != "Alice - active" {
		t.Errorf("expected 'Alice - active', got %v", ds.Rows[0]["greeting"])
	}
}

func TestAddColumn_Literal(t *testing.T) {
	ds := sampleDS()
	err := ApplyTransforms([]TransformRule{
		{Type: "add_column", Name: "source", Expression: "manual_import"},
	}, ds)
	if err != nil {
		t.Fatal(err)
	}
	if ds.Rows[0]["source"] != "manual_import" {
		t.Errorf("expected 'manual_import', got %v", ds.Rows[0]["source"])
	}
}

func TestReplaceValues(t *testing.T) {
	ds := sampleDS()
	err := ApplyTransforms([]TransformRule{
		{Type: "replace_values", Column: "status", Mapping: map[string]string{"active": "enabled", "inactive": "disabled"}},
	}, ds)
	if err != nil {
		t.Fatal(err)
	}
	if ds.Rows[0]["status"] != "enabled" {
		t.Errorf("expected 'enabled', got %v", ds.Rows[0]["status"])
	}
	if ds.Rows[2]["status"] != "disabled" {
		t.Errorf("expected 'disabled', got %v", ds.Rows[2]["status"])
	}
}

func TestSort_Ascending(t *testing.T) {
	ds := sampleDS()
	err := ApplyTransforms([]TransformRule{
		{Type: "sort", Columns: []string{"name"}, Ascending: true},
	}, ds)
	if err != nil {
		t.Fatal(err)
	}
	if ds.Rows[0]["name"] != "Alice" {
		t.Errorf("expected first row 'Alice', got %v", ds.Rows[0]["name"])
	}
	if ds.Rows[3]["name"] != "Diana" {
		t.Errorf("expected last row 'Diana', got %v", ds.Rows[3]["name"])
	}
}

func TestSort_Descending(t *testing.T) {
	ds := sampleDS()
	err := ApplyTransforms([]TransformRule{
		{Type: "sort", Columns: []string{"name"}, Ascending: false},
	}, ds)
	if err != nil {
		t.Fatal(err)
	}
	if ds.Rows[0]["name"] != "Diana" {
		t.Errorf("expected first row 'Diana', got %v", ds.Rows[0]["name"])
	}
}

func TestDeduplicate(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"name", "city"},
		Rows: []common.DataRow{
			{"name": "Alice", "city": "NYC"},
			{"name": "Bob", "city": "LA"},
			{"name": "Alice", "city": "NYC"},
			{"name": "Bob", "city": "LA"},
			{"name": "Charlie", "city": "NYC"},
		},
	}
	err := ApplyTransforms([]TransformRule{
		{Type: "deduplicate", Columns: []string{"name", "city"}},
	}, ds)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds.Rows) != 3 {
		t.Errorf("expected 3 unique rows, got %d", len(ds.Rows))
	}
}

func TestDeduplicate_SingleKey(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"name", "score"},
		Rows: []common.DataRow{
			{"name": "Alice", "score": "100"},
			{"name": "Alice", "score": "200"},
			{"name": "Bob", "score": "150"},
		},
	}
	err := ApplyTransforms([]TransformRule{
		{Type: "deduplicate", Columns: []string{"name"}},
	}, ds)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds.Rows) != 2 {
		t.Errorf("expected 2 rows, got %d", len(ds.Rows))
	}
	// First occurrence kept
	if ds.Rows[0]["score"] != "100" {
		t.Errorf("expected first Alice score '100', got %v", ds.Rows[0]["score"])
	}
}

func TestAggregate_Count(t *testing.T) {
	ds := sampleDS()
	err := ApplyTransforms([]TransformRule{
		{Type: "aggregate", GroupBy: []string{"status"}, AggFields: []AggField{
			{Column: "id", Function: "count", Alias: "cnt"},
		}},
	}, ds)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds.Rows) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(ds.Rows))
	}
	// Find the active group
	for _, row := range ds.Rows {
		if row["status"] == "active" {
			if row["cnt"] != 3 {
				t.Errorf("expected active count 3, got %v", row["cnt"])
			}
		}
	}
}

func TestAggregate_Sum(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"category", "amount"},
		Rows: []common.DataRow{
			{"category": "A", "amount": "100"},
			{"category": "A", "amount": "200"},
			{"category": "B", "amount": "50"},
		},
	}
	err := ApplyTransforms([]TransformRule{
		{Type: "aggregate", GroupBy: []string{"category"}, AggFields: []AggField{
			{Column: "amount", Function: "sum", Alias: "total"},
			{Column: "amount", Function: "avg", Alias: "average"},
		}},
	}, ds)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds.Rows) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(ds.Rows))
	}
	for _, row := range ds.Rows {
		if row["category"] == "A" {
			if row["total"] != 300.0 {
				t.Errorf("expected sum 300, got %v", row["total"])
			}
			if row["average"] != 150.0 {
				t.Errorf("expected avg 150, got %v", row["average"])
			}
		}
	}
}

func TestAggregate_MinMax(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"group", "val"},
		Rows: []common.DataRow{
			{"group": "X", "val": "10"},
			{"group": "X", "val": "30"},
			{"group": "X", "val": "20"},
		},
	}
	err := ApplyTransforms([]TransformRule{
		{Type: "aggregate", GroupBy: []string{"group"}, AggFields: []AggField{
			{Column: "val", Function: "min", Alias: "min_val"},
			{Column: "val", Function: "max", Alias: "max_val"},
		}},
	}, ds)
	if err != nil {
		t.Fatal(err)
	}
	if ds.Rows[0]["min_val"] != 10.0 {
		t.Errorf("expected min 10, got %v", ds.Rows[0]["min_val"])
	}
	if ds.Rows[0]["max_val"] != 30.0 {
		t.Errorf("expected max 30, got %v", ds.Rows[0]["max_val"])
	}
}

func TestChainedTransforms(t *testing.T) {
	ds := sampleDS()
	err := ApplyTransforms([]TransformRule{
		{Type: "filter_rows", Condition: "status in [active]"},
		{Type: "apply_function", Column: "email", Function: "lower"},
		{Type: "rename_columns", Mapping: map[string]string{"name": "full_name"}},
		{Type: "drop_columns", Columns: []string{"status"}},
		{Type: "sort", Columns: []string{"full_name"}, Ascending: true},
	}, ds)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds.Rows) != 3 {
		t.Errorf("expected 3 rows after filter, got %d", len(ds.Rows))
	}
	if len(ds.Columns) != 4 {
		t.Errorf("expected 4 columns after drop, got %d", len(ds.Columns))
	}
	if ds.Rows[0]["full_name"] != "Alice" {
		t.Errorf("expected sorted first row 'Alice', got %v", ds.Rows[0]["full_name"])
	}
	if ds.Rows[0]["email"] != "alice@example.com" {
		t.Errorf("expected lowered email, got %v", ds.Rows[0]["email"])
	}
}

func TestUnsupportedTransformType(t *testing.T) {
	ds := sampleDS()
	err := ApplyTransforms([]TransformRule{
		{Type: "nonexistent"},
	}, ds)
	if err == nil {
		t.Error("expected error for unsupported type")
	}
}

func TestEmptyRules(t *testing.T) {
	ds := sampleDS()
	err := ApplyTransforms([]TransformRule{}, ds)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds.Rows) != 4 {
		t.Error("empty rules should not change data")
	}
}

func TestAggregate_AggregationsAlias(t *testing.T) {
	// This is the exact format the "Join + Aggregate" template sends
	ds := &common.DataSet{
		Columns: []string{"product", "total"},
		Rows: []common.DataRow{
			{"product": "Widget", "total": "100"},
			{"product": "Widget", "total": "200"},
			{"product": "Gadget", "total": "50"},
		},
	}
	err := ApplyTransforms([]TransformRule{
		{
			Type:         "aggregate",
			GroupBy:      []string{"product"},
			Aggregations: []AggField{{Column: "total", Function: "sum"}},
		},
	}, ds)
	if err != nil {
		t.Fatalf("aggregate with 'aggregations' alias failed: %v", err)
	}
	if len(ds.Rows) != 2 {
		t.Errorf("expected 2 groups, got %d", len(ds.Rows))
	}
}

func TestTransformAliases(t *testing.T) {
	// "filter" should work as alias for "filter_rows"
	ds := sampleDS()
	err := ApplyTransforms([]TransformRule{
		{Type: "filter", Condition: "status == active"},
	}, ds)
	if err != nil {
		t.Fatalf("filter alias: %v", err)
	}
	if len(ds.Rows) != 3 {
		t.Errorf("filter alias: expected 3 rows, got %d", len(ds.Rows))
	}

	// "rename" alias for "rename_columns"
	ds2 := sampleDS()
	err = ApplyTransforms([]TransformRule{
		{Type: "rename", Mapping: map[string]string{"name": "full_name"}},
	}, ds2)
	if err != nil {
		t.Fatalf("rename alias: %v", err)
	}
	if ds2.Columns[1] != "full_name" {
		t.Errorf("rename alias: expected 'full_name', got %q", ds2.Columns[1])
	}

	// "drop" alias for "drop_columns"
	ds3 := sampleDS()
	err = ApplyTransforms([]TransformRule{
		{Type: "drop", Columns: []string{"email", "status"}},
	}, ds3)
	if err != nil {
		t.Fatalf("drop alias: %v", err)
	}
	if len(ds3.Columns) != 3 {
		t.Errorf("drop alias: expected 3 columns, got %d", len(ds3.Columns))
	}

	// "dedup" alias for "deduplicate"
	ds4 := &common.DataSet{
		Columns: []string{"name"},
		Rows:    []common.DataRow{{"name": "A"}, {"name": "A"}, {"name": "B"}},
	}
	err = ApplyTransforms([]TransformRule{
		{Type: "dedup", Columns: []string{"name"}},
	}, ds4)
	if err != nil {
		t.Fatalf("dedup alias: %v", err)
	}
	if len(ds4.Rows) != 2 {
		t.Errorf("dedup alias: expected 2 rows, got %d", len(ds4.Rows))
	}

	// "function" alias for "apply_function"
	ds5 := sampleDS()
	err = ApplyTransforms([]TransformRule{
		{Type: "function", Column: "name", Function: "upper"},
	}, ds5)
	if err != nil {
		t.Fatalf("function alias: %v", err)
	}
	if ds5.Rows[0]["name"] != "ALICE" {
		t.Errorf("function alias: expected ALICE, got %v", ds5.Rows[0]["name"])
	}
}

// brokoli-sdk#14: comparison operators work, and an unrecognized condition
// fails loudly instead of silently keeping every row.

func TestFilterRows_GreaterThan_Numeric(t *testing.T) {
	ds := sampleDS()
	// amount > 100 keeps 150.00 and 250.50 (2 rows). The pre-fix matcher
	// didn't know ">" and kept all 4; a lexicographic ">" would wrongly
	// keep "75.25" too. Asserting 2 proves numeric comparison.
	if err := ApplyTransforms([]TransformRule{
		{Type: "filter_rows", Condition: "amount > 100"},
	}, ds); err != nil {
		t.Fatal(err)
	}
	if len(ds.Rows) != 2 {
		t.Errorf("amount > 100 should keep 2 rows, got %d", len(ds.Rows))
	}
}

func TestFilterRows_LessEqual_And_GreaterEqual(t *testing.T) {
	ds := sampleDS()
	if err := ApplyTransforms([]TransformRule{
		{Type: "filter_rows", Condition: "amount <= 75.25"},
	}, ds); err != nil {
		t.Fatal(err)
	}
	// 75.25 and -10.00
	if len(ds.Rows) != 2 {
		t.Fatalf("amount <= 75.25 should keep 2 rows, got %d", len(ds.Rows))
	}

	ds = sampleDS()
	if err := ApplyTransforms([]TransformRule{
		{Type: "filter_rows", Condition: "id >= 3"},
	}, ds); err != nil {
		t.Fatal(err)
	}
	if len(ds.Rows) != 2 { // ids 3 and 4
		t.Errorf("id >= 3 should keep 2 rows, got %d", len(ds.Rows))
	}
}

func TestFilterRows_UnrecognizedConditionFailsLoudly(t *testing.T) {
	ds := sampleDS()
	err := ApplyTransforms([]TransformRule{
		{Type: "filter_rows", Condition: "amount ~ 100"},
	}, ds)
	if err == nil {
		t.Fatal("an unrecognized condition must error, not silently keep every row")
	}
	if !strings.Contains(err.Error(), "unrecognized condition") {
		t.Errorf("error %q should name the unrecognized condition", err)
	}
}

func TestFilterRows_BadConditionErrorsOnEmptyDataset(t *testing.T) {
	// The up-front validation must fire even when there are no rows to loop.
	ds := &common.DataSet{Columns: []string{"id"}, Rows: nil}
	err := ApplyTransforms([]TransformRule{
		{Type: "filter_rows", Condition: "no operator here"},
	}, ds)
	if err == nil {
		t.Fatal("a bad condition on an empty dataset must still error")
	}
}

// TestAddColumn_Arithmetic covers the fix for the silent-corruption bug
// found live: "quantity * unit_price" used to store the literal STRING
// of the expression in every row, which a downstream aggregate summed to
// zero with no error anywhere.
func TestAddColumn_Arithmetic(t *testing.T) {
	mk := func() *common.DataSet {
		return &common.DataSet{
			Columns: []string{"qty", "price", "weird-name", "label"},
			Rows: []common.DataRow{
				{"qty": float64(4), "price": float64(2.5), "weird-name": float64(9), "label": "x"},
			},
		}
	}

	cases := []struct {
		expr string
		want interface{}
	}{
		{"qty * price", float64(10)},
		{"price / qty", 0.625},
		{"price - qty", -1.5},
		{"qty * price * 2", float64(20)},   // same-op chain left-folds
		{"qty * 1.5", float64(6)},          // numeric literal operand
		{"'seen'", "seen"},                 // quoted literal loses its quotes
		{"manual_import", "manual_import"}, // bare word stays a literal (pinned)
		{"a-b", "a-b"},                     // wordy '-' stays a literal, not arithmetic
	}
	for _, tc := range cases {
		ds := mk()
		if err := ApplyTransforms([]TransformRule{{Type: "add_column", Name: "out", Expression: tc.expr}}, ds); err != nil {
			t.Fatalf("%q: %v", tc.expr, err)
		}
		if got := ds.Rows[0]["out"]; got != tc.want {
			t.Errorf("%q = %v (%T), want %v (%T)", tc.expr, got, got, tc.want, tc.want)
		}
	}

	// Division by zero and a non-numeric value in a real column both yield
	// nil — aggregates skip nil instead of folding in corruption.
	ds := mk()
	ds.Rows[0]["qty"] = float64(0)
	_ = ApplyTransforms([]TransformRule{{Type: "add_column", Name: "out", Expression: "price / qty"}}, ds)
	if ds.Rows[0]["out"] != nil {
		t.Errorf("div-by-zero = %v, want nil", ds.Rows[0]["out"])
	}
	ds = mk()
	_ = ApplyTransforms([]TransformRule{{Type: "add_column", Name: "out", Expression: "qty * label"}}, ds)
	if ds.Rows[0]["out"] != nil {
		t.Errorf("non-numeric column value = %v, want nil", ds.Rows[0]["out"])
	}
}
