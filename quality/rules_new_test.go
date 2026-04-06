package quality

import (
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

func TestTypeCheck_Int(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"id"},
		Rows: []common.DataRow{
			{"id": "123"},
			{"id": "456"},
			{"id": "abc"},
		},
	}
	check := Check{Column: "id", Rule: RuleTypeCheck, Params: map[string]interface{}{"expected_type": "int"}}
	result := RunCheck(check, ds)
	if result.Passed {
		t.Error("expected failure: 'abc' is not int")
	}
	if result.Value != 1 {
		t.Errorf("expected 1 violation, got %v", result.Value)
	}
}

func TestTypeCheck_Email(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"email"},
		Rows: []common.DataRow{
			{"email": "user@example.com"},
			{"email": "bad-email"},
			{"email": ""},
		},
	}
	check := Check{Column: "email", Rule: RuleTypeCheck, Params: map[string]interface{}{"expected_type": "email"}}
	result := RunCheck(check, ds)
	if result.Passed {
		t.Error("expected failure: 'bad-email' is not valid email")
	}
}

func TestTypeCheck_Date(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"date"},
		Rows: []common.DataRow{
			{"date": "2024-01-15"},
			{"date": "not-a-date"},
		},
	}
	check := Check{Column: "date", Rule: RuleTypeCheck, Params: map[string]interface{}{"expected_type": "date"}}
	result := RunCheck(check, ds)
	if result.Passed {
		t.Error("expected failure")
	}
}

func TestFreshness_Pass(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"ts"},
		Rows: []common.DataRow{
			{"ts": "2099-01-01"},
		},
	}
	check := Check{Column: "ts", Rule: RuleFreshness, Params: map[string]interface{}{"max_hours": "999999"}}
	result := RunCheck(check, ds)
	if !result.Passed {
		t.Errorf("expected pass for future date: %s", result.Message)
	}
}

func TestFreshness_Fail(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"ts"},
		Rows: []common.DataRow{
			{"ts": "2020-01-01"},
		},
	}
	check := Check{Column: "ts", Rule: RuleFreshness, Params: map[string]interface{}{"max_hours": "24"}}
	result := RunCheck(check, ds)
	if result.Passed {
		t.Error("expected failure: 2020 is definitely stale")
	}
}

func TestNoBlank_Pass(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"name"},
		Rows: []common.DataRow{
			{"name": "Alice"},
			{"name": "Bob"},
		},
	}
	check := Check{Column: "name", Rule: RuleNoBlank}
	result := RunCheck(check, ds)
	if !result.Passed {
		t.Errorf("expected pass: %s", result.Message)
	}
}

func TestNoBlank_Fail(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"name"},
		Rows: []common.DataRow{
			{"name": "Alice"},
			{"name": ""},
			{"name": "   "},
			{"name": nil},
		},
	}
	check := Check{Column: "name", Rule: RuleNoBlank}
	result := RunCheck(check, ds)
	if result.Passed {
		t.Error("expected failure: blanks and nil present")
	}
	if result.Value != 3 {
		t.Errorf("expected 3 blank values, got %v", result.Value)
	}
}
