package quality

import (
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

func testDataset() *common.DataSet {
	return &common.DataSet{
		Columns: []string{"id", "name", "amount", "email"},
		Rows: []common.DataRow{
			{"id": 1, "name": "Alice", "amount": 100.0, "email": "alice@example.com"},
			{"id": 2, "name": "Bob", "amount": 250.5, "email": "bob@example.com"},
			{"id": 3, "name": "", "amount": -10.0, "email": "invalid"},
			{"id": 4, "name": "Diana", "amount": 500.0, "email": "diana@example.com"},
		},
	}
}

func TestNotNull_Pass(t *testing.T) {
	ds := testDataset()
	check := Check{Column: "id", Rule: RuleNotNull}
	result := RunCheck(check, ds)
	if !result.Passed {
		t.Errorf("expected pass for non-null column, got: %s", result.Message)
	}
}

func TestNotNull_Fail(t *testing.T) {
	ds := testDataset()
	check := Check{Column: "name", Rule: RuleNotNull}
	result := RunCheck(check, ds)
	if result.Passed {
		t.Error("expected fail for column with empty string")
	}
}

func TestUnique_Pass(t *testing.T) {
	ds := testDataset()
	check := Check{Column: "id", Rule: RuleUnique}
	result := RunCheck(check, ds)
	if !result.Passed {
		t.Errorf("expected pass for unique column, got: %s", result.Message)
	}
}

func TestUnique_Fail(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"val"},
		Rows:    []common.DataRow{{"val": "a"}, {"val": "a"}, {"val": "b"}},
	}
	check := Check{Column: "val", Rule: RuleUnique}
	result := RunCheck(check, ds)
	if result.Passed {
		t.Error("expected fail for duplicate values")
	}
}

func TestRange_Pass(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"amount"},
		Rows:    []common.DataRow{{"amount": 50.0}, {"amount": 100.0}},
	}
	check := Check{Column: "amount", Rule: RuleRange, Params: map[string]interface{}{"min": 0.0, "max": 200.0}}
	result := RunCheck(check, ds)
	if !result.Passed {
		t.Errorf("expected pass for values in range, got: %s", result.Message)
	}
}

func TestRange_Fail(t *testing.T) {
	ds := testDataset()
	check := Check{Column: "amount", Rule: RuleRange, Params: map[string]interface{}{"min": 0.0, "max": 1000.0}}
	result := RunCheck(check, ds)
	if result.Passed {
		t.Error("expected fail for negative amount")
	}
}

func TestRegex_Pass(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"email"},
		Rows:    []common.DataRow{{"email": "a@b.com"}, {"email": "c@d.org"}},
	}
	check := Check{Column: "email", Rule: RuleRegex, Params: map[string]interface{}{"pattern": `.+@.+\..+`}}
	result := RunCheck(check, ds)
	if !result.Passed {
		t.Errorf("expected pass for valid emails, got: %s", result.Message)
	}
}

func TestRegex_Fail(t *testing.T) {
	ds := testDataset()
	check := Check{Column: "email", Rule: RuleRegex, Params: map[string]interface{}{"pattern": `.+@.+\..+`}}
	result := RunCheck(check, ds)
	if result.Passed {
		t.Error("expected fail for 'invalid' email")
	}
}

func TestRowCount(t *testing.T) {
	ds := testDataset()

	// Min check
	check := Check{Rule: RuleRowCount, Params: map[string]interface{}{"min": 1.0, "max": 10.0}}
	result := RunCheck(check, ds)
	if !result.Passed {
		t.Errorf("expected pass for row count in range, got: %s", result.Message)
	}

	// Below min
	check = Check{Rule: RuleRowCount, Params: map[string]interface{}{"min": 100.0}}
	result = RunCheck(check, ds)
	if result.Passed {
		t.Error("expected fail for row count below min")
	}
}

func TestChecker_AllPass(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"id", "name"},
		Rows:    []common.DataRow{{"id": 1, "name": "Alice"}, {"id": 2, "name": "Bob"}},
	}

	checker := NewChecker()
	result, err := checker.Run([]Check{
		{Column: "id", Rule: RuleNotNull},
		{Column: "id", Rule: RuleUnique},
		{Column: "name", Rule: RuleNotNull},
	}, ds)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Passed {
		t.Errorf("expected all checks to pass: %s", result.Summary)
	}
	if len(result.Results) != 3 {
		t.Errorf("expected 3 results, got %d", len(result.Results))
	}
}

func TestChecker_ShouldBlock(t *testing.T) {
	ds := testDataset()

	checker := NewChecker()
	result, _ := checker.Run([]Check{
		{Column: "name", Rule: RuleNotNull, OnFailure: "block"},
	}, ds)
	if result.Passed {
		t.Error("expected check to fail")
	}
	if !result.ShouldBlock() {
		t.Error("expected ShouldBlock=true for on_failure=block")
	}
}

func TestChecker_WarnOnly(t *testing.T) {
	ds := testDataset()

	checker := NewChecker()
	result, _ := checker.Run([]Check{
		{Column: "name", Rule: RuleNotNull, OnFailure: "warn"},
	}, ds)
	if result.ShouldBlock() {
		t.Error("expected ShouldBlock=false for on_failure=warn")
	}
}
