package quality

import (
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

func rangeDataset(values ...float64) *common.DataSet {
	rows := make([]common.DataRow, 0, len(values))
	for _, v := range values {
		rows = append(rows, common.DataRow{"amount": v})
	}
	return &common.DataSet{Columns: []string{"amount"}, Rows: rows}
}

// A lower bound on its own means "at least this", not "exactly this".
// It used to read as [min, 0] and flag every value above zero.
func TestRangeWithOnlyMinIsUnboundedAbove(t *testing.T) {
	check := Check{Column: "amount", Rule: RuleRange, Params: map[string]interface{}{"min": 0.0}}
	res := RunCheck(check, rangeDataset(0, 12.5, 4000, 1e9))
	if !res.Passed {
		t.Fatalf("expected all non-negative values to pass, got: %s", res.Message)
	}
	if v, ok := res.Value.(int); !ok || v != 0 {
		t.Fatalf("expected zero violations, got %v", res.Value)
	}
}

// The lower bound is still enforced.
func TestRangeWithOnlyMinCatchesValuesBelow(t *testing.T) {
	check := Check{Column: "amount", Rule: RuleRange, Params: map[string]interface{}{"min": 10.0}}
	res := RunCheck(check, rangeDataset(9.99, 10, 11))
	if res.Passed {
		t.Fatal("expected the value below the minimum to be flagged")
	}
	if v, _ := res.Value.(int); v != 1 {
		t.Fatalf("expected exactly 1 violation, got %v", res.Value)
	}
}

// An upper bound on its own means "at most this", including negatives.
func TestRangeWithOnlyMaxIsUnboundedBelow(t *testing.T) {
	check := Check{Column: "amount", Rule: RuleRange, Params: map[string]interface{}{"max": 100.0}}
	res := RunCheck(check, rangeDataset(-500, 0, 100))
	if !res.Passed {
		t.Fatalf("expected values at or below the maximum to pass, got: %s", res.Message)
	}
}

// Both bounds behave as before.
func TestRangeWithBothBounds(t *testing.T) {
	check := Check{Column: "amount", Rule: RuleRange, Params: map[string]interface{}{"min": 1.0, "max": 5.0}}
	res := RunCheck(check, rangeDataset(0.5, 1, 5, 5.5))
	if res.Passed {
		t.Fatal("expected the out-of-range values to be flagged")
	}
	if v, _ := res.Value.(int); v != 2 {
		t.Fatalf("expected 2 violations, got %v", res.Value)
	}
}

// A range check with no bounds can never fail, which is a
// misconfiguration — reporting it as a pass would hide a gate that is
// not actually checking anything.
func TestRangeWithNoBoundsIsReportedAsMisconfigured(t *testing.T) {
	check := Check{Column: "amount", Rule: RuleRange, Params: map[string]interface{}{}}
	res := RunCheck(check, rangeDataset(1, 2, 3))
	if res.Passed {
		t.Fatal("expected a bound-less range check to be reported as failed")
	}
	if res.Message == "" {
		t.Fatal("expected a message explaining the misconfiguration")
	}
}

// The message names the bound that applies rather than printing an infinity.
func TestRangeMessageDescribesOpenBounds(t *testing.T) {
	res := RunCheck(Check{Column: "amount", Rule: RuleRange, Params: map[string]interface{}{"min": 0.0}}, rangeDataset(-1))
	if got := res.Message; got == "" || !contains(got, ">= 0.00") {
		t.Fatalf("expected an open lower-bound description, got %q", got)
	}
	res = RunCheck(Check{Column: "amount", Rule: RuleRange, Params: map[string]interface{}{"max": 7.0}}, rangeDataset(9))
	if got := res.Message; !contains(got, "<= 7.00") {
		t.Fatalf("expected an open upper-bound description, got %q", got)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
