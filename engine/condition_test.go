package engine

import (
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

func makeDataSet(cols []string, rows []common.DataRow) *common.DataSet {
	return &common.DataSet{Columns: cols, Rows: rows}
}

func TestEvaluateCondition_RowCountGt(t *testing.T) {
	ds := makeDataSet([]string{"id"}, []common.DataRow{{"id": 1}, {"id": 2}, {"id": 3}})
	r := EvaluateCondition("row_count > 2", ds)
	if !r.Passed {
		t.Fatalf("expected passed, got reason: %s", r.Reason)
	}
	r = EvaluateCondition("row_count > 5", ds)
	if r.Passed {
		t.Fatalf("expected not passed for row_count > 5 with 3 rows")
	}
}

func TestEvaluateCondition_RowCountEq(t *testing.T) {
	ds := makeDataSet([]string{"id"}, []common.DataRow{{"id": 1}, {"id": 2}})
	r := EvaluateCondition("row_count == 2", ds)
	if !r.Passed {
		t.Fatalf("expected passed, got reason: %s", r.Reason)
	}
	r = EvaluateCondition("row_count == 3", ds)
	if r.Passed {
		t.Fatalf("expected not passed")
	}
}

func TestEvaluateCondition_RowCountLt(t *testing.T) {
	ds := makeDataSet([]string{"id"}, []common.DataRow{{"id": 1}})
	r := EvaluateCondition("row_count < 5", ds)
	if !r.Passed {
		t.Fatalf("expected passed, got reason: %s", r.Reason)
	}
	r = EvaluateCondition("row_count < 1", ds)
	if r.Passed {
		t.Fatalf("expected not passed")
	}
}

func TestEvaluateCondition_ColumnExists(t *testing.T) {
	ds := makeDataSet([]string{"name", "age"}, nil)
	r := EvaluateCondition(`column_exists("name")`, ds)
	if !r.Passed {
		t.Fatalf("expected column 'name' to exist")
	}
}

func TestEvaluateCondition_ColumnNotExists(t *testing.T) {
	ds := makeDataSet([]string{"name", "age"}, nil)
	r := EvaluateCondition(`column_exists("email")`, ds)
	if r.Passed {
		t.Fatalf("expected column 'email' to not exist")
	}
}

func TestEvaluateCondition_NullPct(t *testing.T) {
	ds := makeDataSet([]string{"val"}, []common.DataRow{
		{"val": 1},
		{"val": nil},
		{"val": 3},
		{"val": nil},
	})
	// 50% null
	r := EvaluateCondition(`null_pct("val") < 60`, ds)
	if !r.Passed {
		t.Fatalf("expected passed (50%% < 60), got reason: %s", r.Reason)
	}
	r = EvaluateCondition(`null_pct("val") < 40`, ds)
	if r.Passed {
		t.Fatalf("expected not passed (50%% < 40)")
	}
}

func TestEvaluateCondition_MinMax(t *testing.T) {
	ds := makeDataSet([]string{"score"}, []common.DataRow{
		{"score": 10},
		{"score": 25},
		{"score": 5},
		{"score": 40},
	})
	r := EvaluateCondition(`min("score") > 3`, ds)
	if !r.Passed {
		t.Fatalf("expected min(score)=5 > 3 to pass, reason: %s", r.Reason)
	}
	r = EvaluateCondition(`min("score") > 10`, ds)
	if r.Passed {
		t.Fatalf("expected min(score)=5 > 10 to fail")
	}
	r = EvaluateCondition(`max("score") < 50`, ds)
	if !r.Passed {
		t.Fatalf("expected max(score)=40 < 50 to pass, reason: %s", r.Reason)
	}
	r = EvaluateCondition(`max("score") < 30`, ds)
	if r.Passed {
		t.Fatalf("expected max(score)=40 < 30 to fail")
	}
}

func TestEvaluateCondition_AlwaysTrue(t *testing.T) {
	ds := makeDataSet([]string{}, nil)
	r := EvaluateCondition("always_true", ds)
	if !r.Passed {
		t.Fatal("expected always_true to pass")
	}
}

func TestEvaluateCondition_AlwaysFalse(t *testing.T) {
	ds := makeDataSet([]string{}, nil)
	r := EvaluateCondition("always_false", ds)
	if r.Passed {
		t.Fatal("expected always_false to fail")
	}
}

func TestEvaluateCondition_NilDataSet(t *testing.T) {
	r := EvaluateCondition("row_count > 0", nil)
	if r.Passed {
		t.Fatal("expected nil dataset to fail")
	}
	if r.Reason != "no data" {
		t.Fatalf("expected reason 'no data', got %q", r.Reason)
	}
}

func TestEvaluateCondition_InvalidExpr(t *testing.T) {
	ds := makeDataSet([]string{"x"}, nil)
	r := EvaluateCondition("gobbledygook!!!", ds)
	if r.Passed {
		t.Fatal("expected invalid expression to fail")
	}
	if r.Reason == "" {
		t.Fatal("expected a reason for invalid expression")
	}
}

func TestEvaluateCondition_EmptyDataSet(t *testing.T) {
	ds := makeDataSet([]string{"id"}, []common.DataRow{})
	r := EvaluateCondition("row_count == 0", ds)
	if !r.Passed {
		t.Fatalf("expected row_count == 0 to pass on empty dataset, reason: %s", r.Reason)
	}
	r = EvaluateCondition("row_count > 0", ds)
	if r.Passed {
		t.Fatal("expected row_count > 0 to fail on empty dataset")
	}
}
