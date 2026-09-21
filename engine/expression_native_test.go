package engine

import (
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

func TestEvalPredicateAndCaseWhen(t *testing.T) {
	row := common.DataRow{"age": 21, "name": "Ada", "missing": nil}
	predicate := map[string]interface{}{
		"op": "and",
		"args": []interface{}{
			map[string]interface{}{"op": "gte", "left": map[string]interface{}{"op": "column", "path": []interface{}{"age"}}, "right": map[string]interface{}{"op": "literal", "value": 18}},
			map[string]interface{}{"op": "is_null", "arg": map[string]interface{}{"op": "column", "path": []interface{}{"missing"}}},
		},
	}
	if matched, err := evalPredicate(predicate, row); err != nil || !matched {
		t.Fatalf("evalPredicate() = %v, %v; want true, nil", matched, err)
	}

	expr := map[string]interface{}{
		"op": "case_when",
		"branches": []interface{}{map[string]interface{}{
			"when": map[string]interface{}{"op": "gte", "left": map[string]interface{}{"op": "column", "path": []interface{}{"age"}}, "right": map[string]interface{}{"op": "literal", "value": 18}},
			"then": map[string]interface{}{"op": "literal", "value": "adult"},
		}},
		"else": map[string]interface{}{"op": "literal", "value": "minor"},
	}
	value, err := evalExpression(expr, row)
	if err != nil || value != "adult" {
		t.Fatalf("evalExpression() = %v, %v; want adult, nil", value, err)
	}
}

func TestFilterNative(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"age"},
		Rows:    []common.DataRow{{"age": 17}, {"age": 18}, {"age": 30}},
	}
	err := applyRule(TransformRule{
		Type:              "filter_native",
		ExpressionVersion: 1,
		Predicate:         map[string]interface{}{"op": "gte", "left": map[string]interface{}{"op": "column", "path": []interface{}{"age"}}, "right": map[string]interface{}{"op": "literal", "value": 18}},
	}, ds)
	if err != nil || len(ds.Rows) != 2 {
		t.Fatalf("filterNative() error=%v rows=%d; want nil and 2", err, len(ds.Rows))
	}
}
