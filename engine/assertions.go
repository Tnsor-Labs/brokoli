package engine

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// Assertion defines an expected outcome for a pipeline or node.
type Assertion struct {
	Name     string `json:"name" yaml:"name"`
	NodeID   string `json:"node_id,omitempty" yaml:"node_id,omitempty"` // empty = pipeline-level
	Type     string `json:"type" yaml:"type"`                           // row_count, column_exists, no_nulls, min_rows, max_rows, column_type, unique
	Column   string `json:"column,omitempty" yaml:"column,omitempty"`
	Operator string `json:"operator,omitempty" yaml:"operator,omitempty"` // >, <, ==, >=, <=, !=
	Value    string `json:"value,omitempty" yaml:"value,omitempty"`
}

// AssertionResult records whether an assertion passed or failed.
type AssertionResult struct {
	Name     string `json:"name"`
	Passed   bool   `json:"passed"`
	Message  string `json:"message"`
	Actual   string `json:"actual"`
	Expected string `json:"expected"`
}

// AssertionSuite is a collection of assertions to run against pipeline output.
type AssertionSuite struct {
	PipelineID string      `json:"pipeline_id,omitempty" yaml:"pipeline_id,omitempty"`
	Assertions []Assertion `json:"assertions" yaml:"assertions"`
}

// RunAssertions evaluates all assertions against the given datasets (keyed by node ID).
func RunAssertions(suite AssertionSuite, outputs map[string]*common.DataSet) []AssertionResult {
	results := make([]AssertionResult, 0, len(suite.Assertions))
	for _, a := range suite.Assertions {
		results = append(results, evaluateAssertion(a, outputs))
	}
	return results
}

func evaluateAssertion(a Assertion, outputs map[string]*common.DataSet) AssertionResult {
	// find the relevant dataset
	var ds *common.DataSet
	if a.NodeID != "" {
		ds = outputs[a.NodeID]
	} else {
		// If no node specified, use the last output (any key)
		for _, v := range outputs {
			ds = v
		}
	}

	r := AssertionResult{Name: a.Name}

	if ds == nil {
		r.Message = "no output data available"
		return r
	}

	switch a.Type {
	case "row_count":
		return assertRowCount(a, ds)
	case "min_rows":
		return assertMinRows(a, ds)
	case "max_rows":
		return assertMaxRows(a, ds)
	case "column_exists":
		return assertColumnExists(a, ds)
	case "no_nulls":
		return assertNoNulls(a, ds)
	case "column_type":
		return assertColumnType(a, ds)
	case "unique":
		return assertUnique(a, ds)
	default:
		r.Message = fmt.Sprintf("unknown assertion type: %s", a.Type)
		return r
	}
}

func assertRowCount(a Assertion, ds *common.DataSet) AssertionResult {
	r := AssertionResult{Name: a.Name}
	actual := len(ds.Rows)
	expected, err := strconv.Atoi(a.Value)
	if err != nil {
		r.Message = fmt.Sprintf("invalid value %q: %v", a.Value, err)
		return r
	}

	r.Actual = strconv.Itoa(actual)
	r.Expected = fmt.Sprintf("%s %d", a.Operator, expected)

	switch a.Operator {
	case "==":
		r.Passed = actual == expected
	case "!=":
		r.Passed = actual != expected
	case ">":
		r.Passed = actual > expected
	case ">=":
		r.Passed = actual >= expected
	case "<":
		r.Passed = actual < expected
	case "<=":
		r.Passed = actual <= expected
	default:
		r.Message = fmt.Sprintf("unknown operator: %s", a.Operator)
		return r
	}

	if r.Passed {
		r.Message = "row count assertion passed"
	} else {
		r.Message = fmt.Sprintf("expected row count %s %d, got %d", a.Operator, expected, actual)
	}
	return r
}

func assertMinRows(a Assertion, ds *common.DataSet) AssertionResult {
	r := AssertionResult{Name: a.Name}
	min, err := strconv.Atoi(a.Value)
	if err != nil {
		r.Message = fmt.Sprintf("invalid value %q: %v", a.Value, err)
		return r
	}
	actual := len(ds.Rows)
	r.Actual = strconv.Itoa(actual)
	r.Expected = fmt.Sprintf(">= %d", min)
	r.Passed = actual >= min
	if r.Passed {
		r.Message = "minimum rows assertion passed"
	} else {
		r.Message = fmt.Sprintf("expected at least %d rows, got %d", min, actual)
	}
	return r
}

func assertMaxRows(a Assertion, ds *common.DataSet) AssertionResult {
	r := AssertionResult{Name: a.Name}
	max, err := strconv.Atoi(a.Value)
	if err != nil {
		r.Message = fmt.Sprintf("invalid value %q: %v", a.Value, err)
		return r
	}
	actual := len(ds.Rows)
	r.Actual = strconv.Itoa(actual)
	r.Expected = fmt.Sprintf("<= %d", max)
	r.Passed = actual <= max
	if r.Passed {
		r.Message = "maximum rows assertion passed"
	} else {
		r.Message = fmt.Sprintf("expected at most %d rows, got %d", max, actual)
	}
	return r
}

func assertColumnExists(a Assertion, ds *common.DataSet) AssertionResult {
	r := AssertionResult{Name: a.Name}
	r.Expected = a.Column
	r.Actual = strings.Join(ds.Columns, ", ")
	for _, col := range ds.Columns {
		if col == a.Column {
			r.Passed = true
			r.Message = fmt.Sprintf("column %q exists", a.Column)
			return r
		}
	}
	r.Message = fmt.Sprintf("column %q not found in [%s]", a.Column, r.Actual)
	return r
}

func assertNoNulls(a Assertion, ds *common.DataSet) AssertionResult {
	r := AssertionResult{Name: a.Name}
	r.Expected = fmt.Sprintf("no nulls in column %q", a.Column)

	// Verify column exists
	found := false
	for _, col := range ds.Columns {
		if col == a.Column {
			found = true
			break
		}
	}
	if !found {
		r.Message = fmt.Sprintf("column %q not found", a.Column)
		return r
	}

	nullCount := 0
	for _, row := range ds.Rows {
		if row[a.Column] == nil {
			nullCount++
		}
	}
	r.Actual = fmt.Sprintf("%d nulls", nullCount)
	r.Passed = nullCount == 0
	if r.Passed {
		r.Message = "no nulls assertion passed"
	} else {
		r.Message = fmt.Sprintf("found %d null values in column %q", nullCount, a.Column)
	}
	return r
}

func assertColumnType(a Assertion, ds *common.DataSet) AssertionResult {
	r := AssertionResult{Name: a.Name}
	r.Expected = fmt.Sprintf("all values in %q are %s", a.Column, a.Value)

	// Verify column exists
	found := false
	for _, col := range ds.Columns {
		if col == a.Column {
			found = true
			break
		}
	}
	if !found {
		r.Message = fmt.Sprintf("column %q not found", a.Column)
		return r
	}

	mismatch := 0
	for _, row := range ds.Rows {
		v := row[a.Column]
		if v == nil {
			continue // skip nulls for type checking
		}
		if !matchesType(v, a.Value) {
			mismatch++
		}
	}

	r.Actual = fmt.Sprintf("%d mismatches", mismatch)
	r.Passed = mismatch == 0
	if r.Passed {
		r.Message = "column type assertion passed"
	} else {
		r.Message = fmt.Sprintf("%d values in column %q do not match type %q", mismatch, a.Column, a.Value)
	}
	return r
}

func matchesType(v interface{}, expectedType string) bool {
	switch expectedType {
	case "number":
		switch v.(type) {
		case int, int8, int16, int32, int64,
			uint, uint8, uint16, uint32, uint64,
			float32, float64:
			return true
		}
		// Also accept string representations of numbers
		if s, ok := v.(string); ok {
			_, err := strconv.ParseFloat(s, 64)
			return err == nil
		}
		return false
	case "string":
		_, ok := v.(string)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	default:
		return false
	}
}

func assertUnique(a Assertion, ds *common.DataSet) AssertionResult {
	r := AssertionResult{Name: a.Name}
	r.Expected = fmt.Sprintf("all values in %q are unique", a.Column)

	// Verify column exists
	found := false
	for _, col := range ds.Columns {
		if col == a.Column {
			found = true
			break
		}
	}
	if !found {
		r.Message = fmt.Sprintf("column %q not found", a.Column)
		return r
	}

	seen := make(map[interface{}]bool)
	dupes := 0
	for _, row := range ds.Rows {
		v := row[a.Column]
		if v == nil {
			continue
		}
		key := fmt.Sprintf("%v", v)
		if seen[key] {
			dupes++
		}
		seen[key] = true
	}

	r.Actual = fmt.Sprintf("%d duplicates", dupes)
	r.Passed = dupes == 0
	if r.Passed {
		r.Message = "uniqueness assertion passed"
	} else {
		r.Message = fmt.Sprintf("found %d duplicate values in column %q", dupes, a.Column)
	}
	return r
}
