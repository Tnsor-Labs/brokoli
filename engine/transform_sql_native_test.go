package engine

import (
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/dbdialect"
)

func nativeColumn(path string) map[string]interface{} {
	return map[string]interface{}{"op": "column", "path": []interface{}{path}}
}

func nativeLiteralExpr(value interface{}) map[string]interface{} {
	return map[string]interface{}{"op": "literal", "value": value}
}

func TestCompileNativeFilterToSQL(t *testing.T) {
	rule := TransformRule{
		Type:              "filter_native",
		ExpressionVersion: 1,
		Predicate: map[string]interface{}{
			"op": "and",
			"args": []interface{}{
				map[string]interface{}{"op": "gte", "left": nativeColumn("amount"), "right": nativeLiteralExpr(100)},
				map[string]interface{}{"op": "is_null", "arg": nativeColumn("deleted_at")},
			},
		},
	}
	compiled, ok := compilePlanToSQL(
		transformStreamPlan{prefix: []TransformRule{rule}},
		"SELECT amount, deleted_at FROM events",
		[]string{"amount", "deleted_at"},
		"postgres",
		map[string]sqlColumnKind{"amount": kindNumeric, "deleted_at": kindText},
	)
	if !ok {
		t.Fatal("native predicate should compile")
	}
	if !strings.Contains(compiled.Query, "WHERE") || !strings.Contains(compiled.Query, "IS NULL") {
		t.Fatalf("compiled query = %q; want native WHERE and IS NULL", compiled.Query)
	}
}

func TestCompileNativeFilterRefusesUnsafeShapes(t *testing.T) {
	unsafe := []map[string]interface{}{
		{"op": "not", "arg": map[string]interface{}{"op": "eq", "left": nativeColumn("amount"), "right": nativeLiteralExpr(1)}},
		{"op": "eq", "left": nativeColumn("amount"), "right": map[string]interface{}{"op": "column", "path": []interface{}{"other"}}},
	}
	for _, predicate := range unsafe {
		d, ok := dbdialect.For("postgres")
		if !ok {
			t.Fatal("postgres dialect unavailable")
		}
		if _, ok := compileNativePredicateToSQL(predicate, map[string]sqlColumnRef{"amount": {Ident: `"amount"`, Kind: kindNumeric}}, d); ok {
			t.Fatalf("predicate %v compiled unexpectedly", predicate)
		}
	}
}
