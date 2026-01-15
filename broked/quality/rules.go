package quality

import (
	"fmt"
	"regexp"
	"strconv"

	"github.com/hc12r/brokolisql-go/pkg/common"
)

// RuleType identifies the kind of quality check.
type RuleType string

const (
	RuleNotNull  RuleType = "not_null"
	RuleUnique   RuleType = "unique"
	RuleMin      RuleType = "min"
	RuleMax      RuleType = "max"
	RuleRange    RuleType = "range"
	RuleRegex    RuleType = "regex"
	RuleRowCount RuleType = "row_count"
)

// Check defines a single data quality assertion.
type Check struct {
	Column    string                 `json:"column"`
	Rule      RuleType               `json:"rule"`
	Params    map[string]interface{} `json:"params"`
	OnFailure string                 `json:"on_failure"` // "block" or "warn"
}

// CheckResult contains the outcome of a single check.
type CheckResult struct {
	Check   Check       `json:"check"`
	Passed  bool        `json:"passed"`
	Message string      `json:"message"`
	Value   interface{} `json:"value,omitempty"` // actual measured value
}

// RunCheck evaluates a single check against a dataset.
func RunCheck(check Check, dataset *common.DataSet) CheckResult {
	switch check.Rule {
	case RuleNotNull:
		return checkNotNull(check, dataset)
	case RuleUnique:
		return checkUnique(check, dataset)
	case RuleMin:
		return checkMin(check, dataset)
	case RuleMax:
		return checkMax(check, dataset)
	case RuleRange:
		return checkRange(check, dataset)
	case RuleRegex:
		return checkRegex(check, dataset)
	case RuleRowCount:
		return checkRowCount(check, dataset)
	default:
		return CheckResult{Check: check, Passed: false, Message: fmt.Sprintf("unknown rule: %s", check.Rule)}
	}
}

func checkNotNull(check Check, ds *common.DataSet) CheckResult {
	nullCount := 0
	for _, row := range ds.Rows {
		v, exists := row[check.Column]
		if !exists || v == nil || v == "" {
			nullCount++
		}
	}
	passed := nullCount == 0
	return CheckResult{
		Check:   check,
		Passed:  passed,
		Message: fmt.Sprintf("column %q: %d null values found", check.Column, nullCount),
		Value:   nullCount,
	}
}

func checkUnique(check Check, ds *common.DataSet) CheckResult {
	seen := make(map[string]int)
	for _, row := range ds.Rows {
		v := fmt.Sprintf("%v", row[check.Column])
		seen[v]++
	}
	dupes := 0
	for _, count := range seen {
		if count > 1 {
			dupes += count - 1
		}
	}
	passed := dupes == 0
	return CheckResult{
		Check:   check,
		Passed:  passed,
		Message: fmt.Sprintf("column %q: %d duplicate values found", check.Column, dupes),
		Value:   dupes,
	}
}

func checkMin(check Check, ds *common.DataSet) CheckResult {
	minVal := getParamFloat(check.Params, "min", 0)
	violations := 0
	for _, row := range ds.Rows {
		if f, ok := toFloat(row[check.Column]); ok && f < minVal {
			violations++
		}
	}
	passed := violations == 0
	return CheckResult{
		Check:   check,
		Passed:  passed,
		Message: fmt.Sprintf("column %q: %d values below min %.2f", check.Column, violations, minVal),
		Value:   violations,
	}
}

func checkMax(check Check, ds *common.DataSet) CheckResult {
	maxVal := getParamFloat(check.Params, "max", 0)
	violations := 0
	for _, row := range ds.Rows {
		if f, ok := toFloat(row[check.Column]); ok && f > maxVal {
			violations++
		}
	}
	passed := violations == 0
	return CheckResult{
		Check:   check,
		Passed:  passed,
		Message: fmt.Sprintf("column %q: %d values above max %.2f", check.Column, violations, maxVal),
		Value:   violations,
	}
}

func checkRange(check Check, ds *common.DataSet) CheckResult {
	minVal := getParamFloat(check.Params, "min", 0)
	maxVal := getParamFloat(check.Params, "max", 0)
	violations := 0
	for _, row := range ds.Rows {
		if f, ok := toFloat(row[check.Column]); ok {
			if f < minVal || f > maxVal {
				violations++
			}
		}
	}
	passed := violations == 0
	return CheckResult{
		Check:   check,
		Passed:  passed,
		Message: fmt.Sprintf("column %q: %d values outside range [%.2f, %.2f]", check.Column, violations, minVal, maxVal),
		Value:   violations,
	}
}

func checkRegex(check Check, ds *common.DataSet) CheckResult {
	pattern, _ := check.Params["pattern"].(string)
	if pattern == "" {
		return CheckResult{Check: check, Passed: false, Message: "regex check requires 'pattern' param"}
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return CheckResult{Check: check, Passed: false, Message: fmt.Sprintf("invalid regex: %v", err)}
	}
	violations := 0
	for _, row := range ds.Rows {
		v := fmt.Sprintf("%v", row[check.Column])
		if !re.MatchString(v) {
			violations++
		}
	}
	passed := violations == 0
	return CheckResult{
		Check:   check,
		Passed:  passed,
		Message: fmt.Sprintf("column %q: %d values don't match pattern %q", check.Column, violations, pattern),
		Value:   violations,
	}
}

func checkRowCount(check Check, ds *common.DataSet) CheckResult {
	count := len(ds.Rows)
	minRows := int(getParamFloat(check.Params, "min", 0))
	maxRows := int(getParamFloat(check.Params, "max", 0))

	passed := true
	msg := fmt.Sprintf("row count: %d", count)
	if minRows > 0 && count < minRows {
		passed = false
		msg = fmt.Sprintf("row count %d below minimum %d", count, minRows)
	}
	if maxRows > 0 && count > maxRows {
		passed = false
		msg = fmt.Sprintf("row count %d exceeds maximum %d", count, maxRows)
	}
	return CheckResult{
		Check:   check,
		Passed:  passed,
		Message: msg,
		Value:   count,
	}
}

// Helpers

func getParamFloat(params map[string]interface{}, key string, defaultVal float64) float64 {
	if v, ok := params[key]; ok {
		if f, ok := toFloat(v); ok {
			return f
		}
	}
	return defaultVal
}

func toFloat(v interface{}) (float64, bool) {
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
