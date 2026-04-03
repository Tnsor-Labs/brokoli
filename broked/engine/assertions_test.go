package engine

import (
	"testing"

	"github.com/hc12r/brokolisql-go/pkg/common"
)

func testDataSet() *common.DataSet {
	return &common.DataSet{
		Columns: []string{"id", "name", "age"},
		Rows: []common.DataRow{
			{"id": 1, "name": "Alice", "age": 30},
			{"id": 2, "name": "Bob", "age": 25},
			{"id": 3, "name": "Charlie", "age": 35},
		},
	}
}

func outputs(ds *common.DataSet) map[string]*common.DataSet {
	return map[string]*common.DataSet{"node1": ds}
}

func TestAssertRowCount_Equals(t *testing.T) {
	ds := testDataSet()
	a := Assertion{Name: "count==3", NodeID: "node1", Type: "row_count", Operator: "==", Value: "3"}
	r := evaluateAssertion(a, outputs(ds))
	if !r.Passed {
		t.Fatalf("expected pass, got: %s", r.Message)
	}
}

func TestAssertRowCount_GreaterThan(t *testing.T) {
	ds := testDataSet()
	a := Assertion{Name: "count>1", NodeID: "node1", Type: "row_count", Operator: ">", Value: "1"}
	r := evaluateAssertion(a, outputs(ds))
	if !r.Passed {
		t.Fatalf("expected pass, got: %s", r.Message)
	}

	a2 := Assertion{Name: "count>10", NodeID: "node1", Type: "row_count", Operator: ">", Value: "10"}
	r2 := evaluateAssertion(a2, outputs(ds))
	if r2.Passed {
		t.Fatal("expected fail")
	}
}

func TestAssertMinRows_Pass(t *testing.T) {
	ds := testDataSet()
	a := Assertion{Name: "min2", NodeID: "node1", Type: "min_rows", Value: "2"}
	r := evaluateAssertion(a, outputs(ds))
	if !r.Passed {
		t.Fatalf("expected pass, got: %s", r.Message)
	}
}

func TestAssertMinRows_Fail(t *testing.T) {
	ds := testDataSet()
	a := Assertion{Name: "min10", NodeID: "node1", Type: "min_rows", Value: "10"}
	r := evaluateAssertion(a, outputs(ds))
	if r.Passed {
		t.Fatal("expected fail")
	}
}

func TestAssertMaxRows_Pass(t *testing.T) {
	ds := testDataSet()
	a := Assertion{Name: "max5", NodeID: "node1", Type: "max_rows", Value: "5"}
	r := evaluateAssertion(a, outputs(ds))
	if !r.Passed {
		t.Fatalf("expected pass, got: %s", r.Message)
	}
}

func TestAssertColumnExists_Pass(t *testing.T) {
	ds := testDataSet()
	a := Assertion{Name: "has_name", NodeID: "node1", Type: "column_exists", Column: "name"}
	r := evaluateAssertion(a, outputs(ds))
	if !r.Passed {
		t.Fatalf("expected pass, got: %s", r.Message)
	}
}

func TestAssertColumnExists_Fail(t *testing.T) {
	ds := testDataSet()
	a := Assertion{Name: "has_email", NodeID: "node1", Type: "column_exists", Column: "email"}
	r := evaluateAssertion(a, outputs(ds))
	if r.Passed {
		t.Fatal("expected fail")
	}
}

func TestAssertNoNulls_Pass(t *testing.T) {
	ds := testDataSet()
	a := Assertion{Name: "no_null_name", NodeID: "node1", Type: "no_nulls", Column: "name"}
	r := evaluateAssertion(a, outputs(ds))
	if !r.Passed {
		t.Fatalf("expected pass, got: %s", r.Message)
	}
}

func TestAssertNoNulls_Fail(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"id", "name"},
		Rows: []common.DataRow{
			{"id": 1, "name": "Alice"},
			{"id": 2, "name": nil},
		},
	}
	a := Assertion{Name: "no_null_name", NodeID: "node1", Type: "no_nulls", Column: "name"}
	r := evaluateAssertion(a, outputs(ds))
	if r.Passed {
		t.Fatal("expected fail")
	}
}

func TestAssertColumnType_Number(t *testing.T) {
	ds := testDataSet()
	a := Assertion{Name: "age_is_number", NodeID: "node1", Type: "column_type", Column: "age", Value: "number"}
	r := evaluateAssertion(a, outputs(ds))
	if !r.Passed {
		t.Fatalf("expected pass, got: %s", r.Message)
	}
}

func TestAssertColumnType_String(t *testing.T) {
	ds := testDataSet()
	a := Assertion{Name: "name_is_string", NodeID: "node1", Type: "column_type", Column: "name", Value: "string"}
	r := evaluateAssertion(a, outputs(ds))
	if !r.Passed {
		t.Fatalf("expected pass, got: %s", r.Message)
	}
}

func TestAssertUnique_Pass(t *testing.T) {
	ds := testDataSet()
	a := Assertion{Name: "unique_id", NodeID: "node1", Type: "unique", Column: "id"}
	r := evaluateAssertion(a, outputs(ds))
	if !r.Passed {
		t.Fatalf("expected pass, got: %s", r.Message)
	}
}

func TestAssertUnique_Fail(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"id", "name"},
		Rows: []common.DataRow{
			{"id": 1, "name": "Alice"},
			{"id": 1, "name": "Bob"},
		},
	}
	a := Assertion{Name: "unique_id", NodeID: "node1", Type: "unique", Column: "id"}
	r := evaluateAssertion(a, outputs(ds))
	if r.Passed {
		t.Fatal("expected fail")
	}
}

func TestRunAssertions_Suite(t *testing.T) {
	ds := testDataSet()
	suite := AssertionSuite{
		PipelineID: "test-pipe",
		Assertions: []Assertion{
			{Name: "count", NodeID: "node1", Type: "row_count", Operator: "==", Value: "3"},
			{Name: "has_name", NodeID: "node1", Type: "column_exists", Column: "name"},
			{Name: "unique_id", NodeID: "node1", Type: "unique", Column: "id"},
		},
	}
	results := RunAssertions(suite, outputs(ds))
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	for _, r := range results {
		if !r.Passed {
			t.Fatalf("expected all to pass, %s failed: %s", r.Name, r.Message)
		}
	}
}

func TestRunAssertions_NilDataSet(t *testing.T) {
	suite := AssertionSuite{
		Assertions: []Assertion{
			{Name: "count", NodeID: "missing", Type: "row_count", Operator: "==", Value: "0"},
		},
	}
	results := RunAssertions(suite, map[string]*common.DataSet{})
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Passed {
		t.Fatal("expected fail for nil dataset")
	}
	if results[0].Message != "no output data available" {
		t.Fatalf("unexpected message: %s", results[0].Message)
	}
}

func TestRunAssertions_UnknownType(t *testing.T) {
	ds := testDataSet()
	suite := AssertionSuite{
		Assertions: []Assertion{
			{Name: "bad", NodeID: "node1", Type: "nonexistent"},
		},
	}
	results := RunAssertions(suite, outputs(ds))
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Passed {
		t.Fatal("expected fail for unknown type")
	}
	if results[0].Message != "unknown assertion type: nonexistent" {
		t.Fatalf("unexpected message: %s", results[0].Message)
	}
}
