package engine

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Tnsor-Labs/brokoli/pkg/dbdialect"
)

// compileNativePredicateToSQL accepts only predicate forms whose SQL WHERE
// behavior is equivalent to the native evaluator. In particular, `not` is
// refused because SQL NULL semantics differ from native false-on-null logic.
func compileNativePredicateToSQL(expr map[string]interface{}, cols map[string]sqlColumnRef, d dbdialect.Dialect) (string, bool) {
	op, ok := expr["op"].(string)
	if !ok {
		return "", false
	}
	switch op {
	case "and", "or":
		rawArgs, ok := expr["args"].([]interface{})
		if !ok || len(rawArgs) == 0 {
			return "", false
		}
		parts := make([]string, 0, len(rawArgs))
		for _, raw := range rawArgs {
			child, ok := raw.(map[string]interface{})
			if !ok {
				return "", false
			}
			part, ok := compileNativePredicateToSQL(child, cols, d)
			if !ok {
				return "", false
			}
			parts = append(parts, "("+part+")")
		}
		return strings.Join(parts, " "+strings.ToUpper(op)+" "), true
	case "is_null":
		arg, ok := expr["arg"].(map[string]interface{})
		if !ok {
			return "", false
		}
		col, ok := nativeColumnRef(arg, cols)
		if !ok {
			return "", false
		}
		return col.Ident + " IS NULL", true
	case "eq", "neq", "lt", "lte", "gt", "gte":
		left, right, ok := nativeBinaryOperands(expr)
		if !ok {
			return "", false
		}
		colExpr, literalExpr, colFirst, ok := nativeColumnLiteral(left, right, cols)
		if !ok {
			return "", false
		}
		if !colFirst {
			colExpr, literalExpr = literalExpr, colExpr
		}
		ref, ok := nativeColumnRef(colExpr, cols)
		if !ok {
			return "", false
		}
		value, ok := nativeLiteral(literalExpr)
		if !ok {
			return "", false
		}
		literalSQL, ok := nativeLiteralSQL(value, d)
		if !ok {
			return "", false
		}
		return nativeComparisonSQL(op, ref, value, literalSQL, d)
	default:
		return "", false
	}
}

func nativeBinaryOperands(expr map[string]interface{}) (map[string]interface{}, map[string]interface{}, bool) {
	left, lok := expr["left"].(map[string]interface{})
	right, rok := expr["right"].(map[string]interface{})
	return left, right, lok && rok
}

func nativeColumnRef(expr map[string]interface{}, cols map[string]sqlColumnRef) (sqlColumnRef, bool) {
	if expr["op"] != "column" {
		return sqlColumnRef{}, false
	}
	path, ok := expr["path"].([]interface{})
	if !ok || len(path) != 1 {
		return sqlColumnRef{}, false
	}
	name, ok := path[0].(string)
	if !ok {
		return sqlColumnRef{}, false
	}
	ref, ok := cols[name]
	return ref, ok && ref.Kind != kindUnclassified
}

func nativeColumnLiteral(left, right map[string]interface{}, cols map[string]sqlColumnRef) (map[string]interface{}, map[string]interface{}, bool, bool) {
	if _, ok := nativeColumnRef(left, cols); ok {
		return left, right, true, true
	}
	if _, ok := nativeColumnRef(right, cols); ok {
		return right, left, false, true
	}
	return nil, nil, false, false
}

func nativeComparisonSQL(op string, ref sqlColumnRef, value interface{}, literalSQL string, d dbdialect.Dialect) (string, bool) {
	operator := map[string]string{"eq": "=", "neq": "<>", "lt": "<", "lte": "<=", "gt": ">", "gte": ">="}[op]
	if operator == "" {
		return "", false
	}
	if ref.Kind == kindNumeric {
		if _, ok := expressionFloat(value); !ok {
			return "", false
		}
		return fmt.Sprintf("%s %s %s", d.CastToFloat(ref.Ident), operator, literalSQL), true
	}
	if ref.Kind == kindText {
		text, ok := value.(string)
		if !ok {
			return "", false
		}
		if _, numeric := parseGoFloat(text); numeric {
			return "", false
		}
		return fmt.Sprintf("%s %s %s", d.ByteOrderedText(ref.Ident), operator, literalSQL), true
	}
	return "", false
}

func nativeLiteral(expr map[string]interface{}) (interface{}, bool) {
	if expr["op"] != "literal" {
		return nil, false
	}
	return expr["value"], true
}

func nativeLiteralSQL(value interface{}, d dbdialect.Dialect) (string, bool) {
	if f, ok := expressionFloat(value); ok {
		return strconv.FormatFloat(f, 'g', -1, 64), true
	}
	if b, ok := value.(bool); ok {
		if b {
			return "TRUE", true
		}
		return "FALSE", true
	}
	if s, ok := value.(string); ok {
		return d.QuoteLiteral(s), true
	}
	return "", false
}
