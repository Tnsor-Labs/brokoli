package engine

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// evalExpression evaluates the versioned, JSON-compatible expression subset
// used by native project rules. Missing fields and explicit nulls evaluate to
// nil; unsupported operators fail closed rather than silently changing rows.
func evalExpression(expr map[string]interface{}, row common.DataRow) (interface{}, error) {
	op, ok := expr["op"].(string)
	if !ok || strings.TrimSpace(op) == "" {
		return nil, fmt.Errorf("expression requires string op")
	}
	switch op {
	case "column":
		path, ok := expr["path"].([]interface{})
		if !ok || len(path) == 0 {
			return nil, fmt.Errorf("column requires non-empty path")
		}
		var value interface{} = map[string]interface{}(row)
		for _, segment := range path {
			name, ok := segment.(string)
			if !ok || name == "" {
				return nil, fmt.Errorf("column path segments must be non-empty strings")
			}
			object, ok := value.(map[string]interface{})
			if !ok {
				return nil, nil
			}
			value = object[name]
		}
		return value, nil
	case "literal":
		return expr["value"], nil
	case "case_when":
		branches, ok := expr["branches"].([]interface{})
		if !ok || len(branches) == 0 {
			return nil, fmt.Errorf("case_when requires branches")
		}
		for _, raw := range branches {
			branch, ok := raw.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("case_when branch must be object")
			}
			when, ok := branch["when"].(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("case_when branch requires when")
			}
			match, err := evalPredicate(when, row)
			if err != nil {
				return nil, err
			}
			if match {
				thenExpr, ok := branch["then"].(map[string]interface{})
				if !ok {
					return nil, fmt.Errorf("case_when branch requires then")
				}
				return evalExpression(thenExpr, row)
			}
		}
		if otherwise, ok := expr["else"].(map[string]interface{}); ok {
			return evalExpression(otherwise, row)
		}
		return nil, nil
	case "coalesce":
		args, ok := expr["args"].([]interface{})
		if !ok || len(args) == 0 {
			return nil, fmt.Errorf("coalesce requires args")
		}
		for _, raw := range args {
			child, ok := raw.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("coalesce args must be expressions")
			}
			value, err := evalExpression(child, row)
			if err != nil {
				return nil, err
			}
			if value != nil {
				return value, nil
			}
		}
		return nil, nil
	case "add", "subtract", "multiply", "divide", "concat":
		left, right, err := binaryOperands(expr, row)
		if err != nil {
			return nil, err
		}
		if left == nil || right == nil {
			return nil, nil
		}
		if op == "concat" {
			return fmt.Sprintf("%v%v", left, right), nil
		}
		lf, lok := expressionFloat(left)
		rf, rok := expressionFloat(right)
		if !lok || !rok {
			return nil, fmt.Errorf("%s requires numeric operands", op)
		}
		switch op {
		case "add":
			return lf + rf, nil
		case "subtract":
			return lf - rf, nil
		case "multiply":
			return lf * rf, nil
		case "divide":
			if rf == 0 {
				return nil, fmt.Errorf("divide by zero")
			}
			return lf / rf, nil
		}
	}
	return nil, fmt.Errorf("unsupported expression op %q", op)
}

func evalPredicate(expr map[string]interface{}, row common.DataRow) (bool, error) {
	op, ok := expr["op"].(string)
	if !ok || op == "" {
		return false, fmt.Errorf("predicate requires string op")
	}
	if op == "and" || op == "or" {
		args, ok := expr["args"].([]interface{})
		if !ok || len(args) == 0 {
			return false, fmt.Errorf("%s requires args", op)
		}
		result := op == "and"
		for _, raw := range args {
			child, ok := raw.(map[string]interface{})
			if !ok {
				return false, fmt.Errorf("predicate args must be objects")
			}
			value, err := evalPredicate(child, row)
			if err != nil {
				return false, err
			}
			if op == "and" {
				result = result && value
			} else {
				result = result || value
			}
		}
		return result, nil
	}
	if op == "not" || op == "is_null" {
		child, ok := expr["arg"].(map[string]interface{})
		if !ok {
			return false, fmt.Errorf("%s requires arg", op)
		}
		if op == "is_null" {
			value, err := evalExpression(child, row)
			return value == nil, err
		}
		predicate, err := evalPredicate(child, row)
		return !predicate, err
	}
	left, right, err := binaryOperands(expr, row)
	if err != nil {
		return false, err
	}
	if left == nil || right == nil {
		return false, nil
	}
	if lf, lok := expressionFloat(left); lok {
		if rf, rok := expressionFloat(right); rok {
			switch op {
			case "eq":
				return lf == rf, nil
			case "neq":
				return lf != rf, nil
			case "lt":
				return lf < rf, nil
			case "lte":
				return lf <= rf, nil
			case "gt":
				return lf > rf, nil
			case "gte":
				return lf >= rf, nil
			}
		}
	}
	ls, rs := fmt.Sprintf("%v", left), fmt.Sprintf("%v", right)
	switch op {
	case "eq":
		return ls == rs, nil
	case "neq":
		return ls != rs, nil
	case "lt":
		return ls < rs, nil
	case "lte":
		return ls <= rs, nil
	case "gt":
		return ls > rs, nil
	case "gte":
		return ls >= rs, nil
	}
	return false, fmt.Errorf("unsupported predicate op %q", op)
}

func binaryOperands(expr map[string]interface{}, row common.DataRow) (interface{}, interface{}, error) {
	left, ok := expr["left"].(map[string]interface{})
	if !ok {
		return nil, nil, fmt.Errorf("expression requires left operand")
	}
	right, ok := expr["right"].(map[string]interface{})
	if !ok {
		return nil, nil, fmt.Errorf("expression requires right operand")
	}
	lv, err := evalExpression(left, row)
	if err != nil {
		return nil, nil, err
	}
	rv, err := evalExpression(right, row)
	return lv, rv, err
}

func expressionFloat(value interface{}) (float64, bool) {
	switch value := value.(type) {
	case float64:
		return value, true
	case float32:
		return float64(value), true
	case int:
		return float64(value), true
	case int64:
		return float64(value), true
	case jsonNumber:
		f, err := strconv.ParseFloat(string(value), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

// jsonNumber avoids importing encoding/json solely for decoder-specific input;
// tests and direct callers may still supply ordinary Go numeric values.
type jsonNumber string
