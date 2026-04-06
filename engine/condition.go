package engine

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// ConditionResult holds the outcome of a condition evaluation.
type ConditionResult struct {
	Passed bool   `json:"passed"`
	Reason string `json:"reason"`
	Value  string `json:"value"` // the evaluated value
}

var (
	reRowCount  = regexp.MustCompile(`^row_count\s*(==|!=|>=|<=|>|<)\s*(\d+)$`)
	reColExists = regexp.MustCompile(`^column_exists\(\s*"([^"]+)"\s*\)$`)
	reNullPct   = regexp.MustCompile(`^null_pct\(\s*"([^"]+)"\s*\)\s*(==|!=|>=|<=|>|<)\s*([\d.]+)$`)
	reMinMax    = regexp.MustCompile(`^(min|max)\(\s*"([^"]+)"\s*\)\s*(==|!=|>=|<=|>|<)\s*([\d.]+)$`)
)

// EvaluateCondition evaluates a condition expression against a dataset.
// Supported expressions:
//
//	row_count > N, row_count < N, row_count == N
//	column_exists("name")
//	null_pct("column") < N
//	min("column") > N, max("column") < N
//	always_true, always_false (for testing)
func EvaluateCondition(expr string, ds *common.DataSet) ConditionResult {
	if ds == nil {
		return ConditionResult{Passed: false, Reason: "no data", Value: ""}
	}

	expr = strings.TrimSpace(expr)

	// always_true / always_false
	if expr == "always_true" {
		return ConditionResult{Passed: true, Reason: "always_true", Value: "true"}
	}
	if expr == "always_false" {
		return ConditionResult{Passed: false, Reason: "always_false", Value: "false"}
	}

	// row_count OP N
	if m := reRowCount.FindStringSubmatch(expr); m != nil {
		op := m[1]
		n, _ := strconv.Atoi(m[2])
		actual := len(ds.Rows)
		passed := compareInts(actual, op, n)
		return ConditionResult{
			Passed: passed,
			Reason: fmt.Sprintf("row_count=%d %s %d", actual, op, n),
			Value:  strconv.Itoa(actual),
		}
	}

	// column_exists("name")
	if m := reColExists.FindStringSubmatch(expr); m != nil {
		colName := m[1]
		found := false
		for _, c := range ds.Columns {
			if c == colName {
				found = true
				break
			}
		}
		reason := fmt.Sprintf("column %q exists=%v", colName, found)
		return ConditionResult{Passed: found, Reason: reason, Value: fmt.Sprintf("%v", found)}
	}

	// null_pct("col") OP N
	if m := reNullPct.FindStringSubmatch(expr); m != nil {
		colName := m[1]
		op := m[2]
		threshold, _ := strconv.ParseFloat(m[3], 64)
		pct := computeNullPct(colName, ds)
		passed := compareFloats(pct, op, threshold)
		return ConditionResult{
			Passed: passed,
			Reason: fmt.Sprintf("null_pct(%q)=%.2f %s %.2f", colName, pct, op, threshold),
			Value:  fmt.Sprintf("%.2f", pct),
		}
	}

	// min("col") OP N / max("col") OP N
	if m := reMinMax.FindStringSubmatch(expr); m != nil {
		fn := m[1]
		colName := m[2]
		op := m[3]
		threshold, _ := strconv.ParseFloat(m[4], 64)
		val := computeMinMax(fn, colName, ds)
		passed := compareFloats(val, op, threshold)
		return ConditionResult{
			Passed: passed,
			Reason: fmt.Sprintf("%s(%q)=%.2f %s %.2f", fn, colName, val, op, threshold),
			Value:  fmt.Sprintf("%.2f", val),
		}
	}

	return ConditionResult{Passed: false, Reason: fmt.Sprintf("unsupported expression: %s", expr), Value: ""}
}

func compareInts(a int, op string, b int) bool {
	switch op {
	case ">":
		return a > b
	case "<":
		return a < b
	case ">=":
		return a >= b
	case "<=":
		return a <= b
	case "==":
		return a == b
	case "!=":
		return a != b
	default:
		return false
	}
}

func compareFloats(a float64, op string, b float64) bool {
	switch op {
	case ">":
		return a > b
	case "<":
		return a < b
	case ">=":
		return a >= b
	case "<=":
		return a <= b
	case "==":
		return a == b
	case "!=":
		return a != b
	default:
		return false
	}
}

func computeNullPct(col string, ds *common.DataSet) float64 {
	if len(ds.Rows) == 0 {
		return 0
	}
	nulls := 0
	for _, row := range ds.Rows {
		v, ok := row[col]
		if !ok || v == nil || fmt.Sprintf("%v", v) == "" {
			nulls++
		}
	}
	return float64(nulls) / float64(len(ds.Rows)) * 100
}

func computeMinMax(fn, col string, ds *common.DataSet) float64 {
	first := true
	var result float64
	for _, row := range ds.Rows {
		v, ok := row[col]
		if !ok || v == nil {
			continue
		}
		f, ok := toFloat(v)
		if !ok {
			continue
		}
		if first {
			result = f
			first = false
			continue
		}
		if fn == "min" && f < result {
			result = f
		}
		if fn == "max" && f > result {
			result = f
		}
	}
	return result
}

func toFloat(v interface{}) (float64, bool) {
	if v == nil {
		return 0, false
	}
	switch val := v.(type) {
	case float64:
		return val, true
	case float32:
		return float64(val), true
	case int:
		return float64(val), true
	case int64:
		return float64(val), true
	case string:
		f, err := strconv.ParseFloat(val, 64)
		return f, err == nil
	default:
		return 0, false
	}
}
