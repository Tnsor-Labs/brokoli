package engine

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// referenceMatch is the matcher as it was before conditions were parsed
// once: every cell rendered with %v and, for an ordered comparison, parsed
// back as a float. The numeric shortcut must give the same answer.
func referenceMatch(t *testing.T, cond string, row common.DataRow) bool {
	t.Helper()
	pc, err := parseCondition(cond)
	if err != nil {
		t.Fatal(err)
	}
	left := fmt.Sprintf("%v", row[pc.Column])
	switch pc.Op {
	case "in":
		for _, v := range pc.Set {
			if v == left {
				return true
			}
		}
		return false
	case "=", "==":
		return left == pc.Target
	case "!=":
		return left != pc.Target
	}
	cmp := strings.Compare(left, pc.Target)
	if lf, lerr := strconv.ParseFloat(left, 64); lerr == nil {
		if rf, rerr := strconv.ParseFloat(pc.Target, 64); rerr == nil {
			switch {
			case lf < rf:
				cmp = -1
			case lf > rf:
				cmp = 1
			default:
				cmp = 0
			}
		}
	}
	switch pc.Op {
	case ">":
		return cmp > 0
	case "<":
		return cmp < 0
	case ">=":
		return cmp >= 0
	}
	return cmp <= 0
}

func TestConditionMatcherAgreesWithTheTextMatcher(t *testing.T) {
	values := []interface{}{
		50.0, 49.99999999999999, 0.1, math.Copysign(0, -1), 1e21, 1e-7, math.NaN(), math.Inf(1), math.Inf(-1),
		int64(50), int64(-3), int64(9007199254740993), int64(math.MaxInt64), 50, -1,
		float32(0.1), float32(50), json.Number("50.0"),
		"50", "50.0", "abc", "", "2026-01-01", nil, true,
	}
	conds := []string{
		"x > 50", "x >= 50", "x < 50", "x <= 50", "x > 0.1", "x < 1e21", "x >= 9007199254740992",
		"x > 9223372036854775807", "x <= -0", "x > NaN", "x < Inf", "x > abc", "x < 2026-06-01",
		"x = 50", "x == 0.1", "x != 50", "x in [50, abc, <nil>]",
	}
	checked := 0
	for _, cond := range conds {
		m, err := newConditionMatcher(cond)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range values {
			row := common.DataRow{"x": v}
			if got, want := m.matches(row), referenceMatch(t, cond, row); got != want {
				t.Errorf("%q on %T(%v): %v, the text matcher says %v", cond, v, v, got, want)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("compared nothing")
	}
}

// The condition is checked before any row, so a malformed one fails even
// on an empty dataset rather than keeping everything (brokoli-sdk#14).
func TestAMalformedConditionFailsOnAnEmptyDataset(t *testing.T) {
	err := filterRows(TransformRule{Type: "filter_rows", Condition: "x ~ 5"}, &common.DataSet{})
	if err == nil {
		t.Fatal("a condition with no known operator was accepted")
	}
}
