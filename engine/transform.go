package engine

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// TransformRule defines a single transformation to apply.
type TransformRule struct {
	Type       string            `json:"type"`
	Name       string            `json:"name,omitempty"`
	Expression string            `json:"expression,omitempty"`
	Column     string            `json:"column,omitempty"`
	Function   string            `json:"function,omitempty"`
	Columns    []string          `json:"columns,omitempty"`
	Mapping    map[string]string `json:"mapping,omitempty"`
	Condition  string            `json:"condition,omitempty"`
	Ascending  bool              `json:"ascending,omitempty"`
	// Aggregate fields
	GroupBy      []string   `json:"group_by,omitempty"`     // columns to group by
	AggFields    []AggField `json:"agg_fields,omitempty"`   // aggregation definitions
	Aggregations []AggField `json:"aggregations,omitempty"` // alias for agg_fields (template compat)
}

// AggField defines an aggregation operation on a column.
type AggField struct {
	Column   string `json:"column"`          // source column
	Function string `json:"function"`        // sum, count, avg, min, max
	Alias    string `json:"alias,omitempty"` // output column name (optional override)
}

// ApplyTransforms runs a sequence of transform rules on a dataset.
func ApplyTransforms(rules []TransformRule, ds *common.DataSet) error {
	for i, rule := range rules {
		if err := applyRule(rule, ds); err != nil {
			return fmt.Errorf("transform #%d (%s): %w", i+1, rule.Type, err)
		}
	}
	return nil
}

func applyRule(r TransformRule, ds *common.DataSet) error {
	switch r.Type {
	case "rename_columns", "rename":
		return renameColumns(r, ds)
	case "add_column":
		return addColumn(r, ds)
	case "filter_rows", "filter":
		return filterRows(r, ds)
	case "apply_function", "function":
		return applyFunction(r, ds)
	case "replace_values", "replace":
		return replaceValues(r, ds)
	case "drop_columns", "drop":
		return dropColumns(r, ds)
	case "sort":
		return sortRows(r, ds)
	case "deduplicate", "dedup":
		return deduplicate(r, ds)
	case "aggregate", "agg":
		return aggregate(r, ds)
	default:
		return fmt.Errorf("unsupported transform type: %s", r.Type)
	}
}

func renameColumns(r TransformRule, ds *common.DataSet) error {
	if len(r.Mapping) == 0 {
		return fmt.Errorf("rename_columns requires mapping")
	}
	for i, col := range ds.Columns {
		if newName, ok := r.Mapping[col]; ok {
			ds.Columns[i] = newName
		}
	}
	for _, row := range ds.Rows {
		for old, new_ := range r.Mapping {
			if val, ok := row[old]; ok {
				row[new_] = val
				delete(row, old)
			}
		}
	}
	return nil
}

func addColumn(r TransformRule, ds *common.DataSet) error {
	if r.Name == "" || r.Expression == "" {
		return fmt.Errorf("add_column requires name and expression")
	}
	ds.Columns = append(ds.Columns, r.Name)
	for _, row := range ds.Rows {
		row[r.Name] = evalAddColumnExpression(r.Expression, row)
	}
	return nil
}

// evalAddColumnExpression computes an add_column expression for one row.
//
// Three forms, evaluated in order:
//
//  1. "+" means string CONCATENATION over columns and quoted literals —
//     the original, test-pinned behavior, kept exactly (numbers
//     concatenate as their string forms; this is documented behavior,
//     not addition).
//  2. "*", "/", or "-" between operands means ARITHMETIC — but only when
//     EVERY operand resolves to a number (a numeric column value or a
//     numeric literal). Found live: "quantity * unit_price" used to fall
//     through to form 3 and store the literal STRING
//     "quantity * unit_price" in every row, which a downstream aggregate
//     then silently summed to zero — data corruption with no error
//     anywhere. Same-operator chains left-fold; division by zero and any
//     non-numeric operand yield nil (which aggregation correctly skips),
//     never a corrupt string. The all-operands-numeric guard is what
//     keeps this backward-safe: a hyphenated column name or a wordy
//     literal containing "-" fails resolution and falls through to
//     form 3, exactly as before.
//  3. Anything else is a literal: a single quoted token has its quotes
//     stripped (consistent with how form 1 treats quoted parts — the old
//     behavior kept the quotes, which nothing relied on and nobody
//     wanted); a bare word stays a bare-word literal, exactly as the
//     pinned tests require.
func evalAddColumnExpression(expr string, row common.DataRow) interface{} {
	if strings.Contains(expr, "+") {
		parts := strings.Split(expr, "+")
		var result string
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if val, ok := row[part]; ok {
				result += fmt.Sprintf("%v", val)
			} else if len(part) >= 2 && (part[0] == '\'' || part[0] == '"') {
				result += part[1 : len(part)-1]
			} else {
				result += part
			}
		}
		return result
	}
	for _, op := range []string{"*", "/", "-"} {
		if !strings.Contains(expr, op) {
			continue
		}
		parts := strings.Split(expr, op)
		if len(parts) < 2 {
			continue
		}
		operands := make([]float64, 0, len(parts))
		allNumeric := true
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part == "" {
				allNumeric = false
				break
			}
			if val, ok := row[part]; ok {
				if f, fok := toAggFloat(val); fok {
					operands = append(operands, f)
					continue
				}
				allNumeric = false
				break
			}
			if f, err := strconv.ParseFloat(part, 64); err == nil {
				operands = append(operands, f)
				continue
			}
			allNumeric = false
			break
		}
		if !allNumeric {
			// Not arithmetic after all (hyphenated name, wordy literal,
			// or a non-numeric value in this row): the row-independent
			// forms fall through to the literal branch below; a
			// non-numeric VALUE in an otherwise-arithmetic expression
			// yields nil so aggregates skip it rather than folding in a
			// corrupt string.
			if columnRefBroken(parts, row) {
				return nil
			}
			break
		}
		result := operands[0]
		for _, f := range operands[1:] {
			switch op {
			case "*":
				result *= f
			case "/":
				if f == 0 {
					return nil
				}
				result /= f
			case "-":
				result -= f
			}
		}
		return result
	}
	if len(expr) >= 2 && (expr[0] == '\'' || expr[0] == '"') && expr[len(expr)-1] == expr[0] {
		return expr[1 : len(expr)-1]
	}
	return expr
}

// columnRefBroken reports whether an arithmetic-looking expression names a
// real column whose value just isn't numeric IN THIS ROW — the case where
// the expression is genuinely arithmetic and the honest per-row answer is
// nil, as opposed to an expression that was never arithmetic at all.
func columnRefBroken(parts []string, row common.DataRow) bool {
	sawColumn := false
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if _, ok := row[part]; ok {
			sawColumn = true
		} else if _, err := strconv.ParseFloat(part, 64); err != nil {
			return false // a non-column, non-number token: not arithmetic
		}
	}
	return sawColumn
}

func filterRows(r TransformRule, ds *common.DataSet) error {
	if r.Condition == "" {
		return fmt.Errorf("filter_rows requires condition")
	}

	// Validate the condition once, up front, so an unrecognized form fails
	// loudly — even for an empty dataset — instead of silently keeping
	// every row (brokoli-sdk#14). The parse is row-independent, so an empty
	// row exercises exactly the form check.
	if _, err := matchesCondition(r.Condition, common.DataRow{}); err != nil {
		return fmt.Errorf("filter_rows: %w", err)
	}

	var kept []common.DataRow
	for _, row := range ds.Rows {
		match, err := matchesCondition(r.Condition, row)
		if err != nil {
			return fmt.Errorf("filter_rows: %w", err)
		}
		if match {
			kept = append(kept, row)
		}
	}
	ds.Rows = kept
	return nil
}

// comparisonOps are the operators matchesCondition understands beyond the
// “in“ set membership form. Order does not matter here — detection is by
// leftmost position with the longest operator winning at a tie, so ">="
// beats ">" and "!=" beats "=" (below).
var comparisonOps = []string{">=", "<=", "!=", "==", "=", ">", "<"}

// matchesCondition reports whether row satisfies cond. cond is one of:
//
//	column in [a, b, c]           set membership
//	column = value / == value     string equality
//	column != value               string inequality
//	column > / < / >= / <= value  ordered comparison (numeric when both
//	                              sides parse as numbers, else lexicographic)
//
// An unrecognized form is an error, not a silent "keep the row" — the
// silent pass-through was a data-quality footgun (brokoli-sdk#14): a
// condition like "id > 100" against a matcher that didn't know ">" kept
// every row with no error.
// parsedCondition is the shared parse of a filter condition. Both backends —
// the Go matcher below and the SQL compiler in transform_sql.go — read it, so
// they cannot disagree about which column, operator, and target a condition
// names. Only evaluation can differ, and that is what the differential tests
// check.
type parsedCondition struct {
	Column string
	Op     string   // "in", "=", "==", "!=", ">", "<", ">=", "<="
	Target string   // everything except "in"
	Set    []string // "in" only, already trimmed of quotes and spaces
}

func parseCondition(cond string) (parsedCondition, error) {
	if strings.Contains(cond, " in ") {
		parts := strings.SplitN(cond, " in ", 2)
		valuesStr := strings.Trim(strings.TrimSpace(parts[1]), "[]")
		raw := strings.Split(valuesStr, ",")
		set := make([]string, 0, len(raw))
		for _, v := range raw {
			set = append(set, strings.Trim(v, " '\""))
		}
		return parsedCondition{Column: strings.TrimSpace(parts[0]), Op: "in", Set: set}, nil
	}

	// Find the leftmost operator; at a tie, the longest wins so a two-char
	// operator isn't shadowed by the single-char one it contains.
	bestIdx, bestOp := -1, ""
	for _, op := range comparisonOps {
		if idx := strings.Index(cond, op); idx >= 0 {
			if bestIdx == -1 || idx < bestIdx || (idx == bestIdx && len(op) > len(bestOp)) {
				bestIdx, bestOp = idx, op
			}
		}
	}
	if bestIdx == -1 {
		return parsedCondition{}, fmt.Errorf(
			"unrecognized condition %q: expected \"column <op> value\" with op one of "+
				"in, =, ==, !=, >, <, >=, <=", cond)
	}
	return parsedCondition{
		Column: strings.TrimSpace(cond[:bestIdx]),
		Op:     bestOp,
		Target: strings.Trim(strings.TrimSpace(cond[bestIdx+len(bestOp):]), "'\""),
	}, nil
}

func matchesCondition(cond string, row common.DataRow) (bool, error) {
	pc, err := parseCondition(cond)
	if err != nil {
		return false, err
	}
	if pc.Op == "in" {
		colVal := fmt.Sprintf("%v", row[pc.Column])
		for _, v := range pc.Set {
			if v == colVal {
				return true, nil
			}
		}
		return false, nil
	}

	target := pc.Target
	left := fmt.Sprintf("%v", row[pc.Column])

	switch pc.Op {
	case "=", "==":
		return left == target, nil
	case "!=":
		return left != target, nil
	}

	// Ordered comparison: numeric when both sides parse as numbers,
	// otherwise lexicographic (so dates/strings still order sensibly).
	cmp := strings.Compare(left, target)
	if lf, lerr := strconv.ParseFloat(left, 64); lerr == nil {
		if rf, rerr := strconv.ParseFloat(target, 64); rerr == nil {
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
		return cmp > 0, nil
	case "<":
		return cmp < 0, nil
	case ">=":
		return cmp >= 0, nil
	case "<=":
		return cmp <= 0, nil
	}
	return false, fmt.Errorf("unrecognized operator %q", pc.Op) // unreachable
}

func applyFunction(r TransformRule, ds *common.DataSet) error {
	if r.Column == "" || r.Function == "" {
		return fmt.Errorf("apply_function requires column and function")
	}
	for _, row := range ds.Rows {
		if val, ok := row[r.Column]; ok {
			if val == nil {
				// A NULL has no text to transform. It used to be rendered
				// with %v first, so upper turned a missing value into the
				// literal string "<NIL>" and lower into "<nil>" — a NULL
				// silently replaced by garbage that then travelled
				// downstream as real data. Every SQL engine leaves NULL
				// alone here; so does this now.
				continue
			}
			str, isStr := val.(string)
			if !isStr {
				str = fmt.Sprintf("%v", val)
			}
			switch strings.ToLower(r.Function) {
			case "lower":
				row[r.Column] = strings.ToLower(str)
			case "upper":
				row[r.Column] = strings.ToUpper(str)
			case "trim":
				row[r.Column] = strings.TrimSpace(str)
			case "title":
				row[r.Column] = strings.Title(str)
			default:
				return fmt.Errorf("unsupported function: %s", r.Function)
			}
		}
	}
	return nil
}

func replaceValues(r TransformRule, ds *common.DataSet) error {
	if r.Column == "" || len(r.Mapping) == 0 {
		return fmt.Errorf("replace_values requires column and mapping")
	}
	for _, row := range ds.Rows {
		if val, ok := row[r.Column]; ok {
			s := fmt.Sprintf("%v", val)
			if newVal, ok := r.Mapping[s]; ok {
				row[r.Column] = newVal
			}
		}
	}
	return nil
}

func dropColumns(r TransformRule, ds *common.DataSet) error {
	if len(r.Columns) == 0 {
		return fmt.Errorf("drop_columns requires columns list")
	}
	drop := make(map[string]bool, len(r.Columns))
	for _, c := range r.Columns {
		drop[c] = true
	}
	var kept []string
	for _, c := range ds.Columns {
		if !drop[c] {
			kept = append(kept, c)
		}
	}
	ds.Columns = kept
	for _, row := range ds.Rows {
		for c := range drop {
			delete(row, c)
		}
	}
	return nil
}

func sortRows(r TransformRule, ds *common.DataSet) error {
	if len(r.Columns) == 0 {
		return fmt.Errorf("sort requires columns list")
	}
	sort.SliceStable(ds.Rows, func(i, j int) bool {
		for _, col := range r.Columns {
			vi := fmt.Sprintf("%v", ds.Rows[i][col])
			vj := fmt.Sprintf("%v", ds.Rows[j][col])
			if vi != vj {
				if r.Ascending {
					return vi < vj
				}
				return vi > vj
			}
		}
		return false
	})
	return nil
}

func deduplicate(r TransformRule, ds *common.DataSet) error {
	if len(r.Columns) == 0 {
		return fmt.Errorf("deduplicate requires columns (key columns)")
	}
	seen := make(map[string]bool)
	var kept []common.DataRow
	for _, row := range ds.Rows {
		var parts []string
		for _, col := range r.Columns {
			parts = append(parts, fmt.Sprintf("%v", row[col]))
		}
		key := strings.Join(parts, "\x00")
		if !seen[key] {
			seen[key] = true
			kept = append(kept, row)
		}
	}
	ds.Rows = kept
	return nil
}

func aggregate(r TransformRule, ds *common.DataSet) error {
	if len(r.GroupBy) == 0 {
		return fmt.Errorf("aggregate requires group_by columns")
	}
	// Accept both "agg_fields" and "aggregations" (template compat)
	if len(r.AggFields) == 0 && len(r.Aggregations) > 0 {
		r.AggFields = r.Aggregations
	}
	if len(r.AggFields) == 0 {
		return fmt.Errorf("aggregate requires at least one aggregation (e.g. sum, count, avg) — add aggregation fields in the transform config")
	}

	// Group rows by key
	type group struct {
		keyRow common.DataRow
		rows   []common.DataRow
	}
	groups := make(map[string]*group)
	var order []string

	for _, row := range ds.Rows {
		var parts []string
		for _, col := range r.GroupBy {
			parts = append(parts, fmt.Sprintf("%v", row[col]))
		}
		key := strings.Join(parts, "\x00")
		if _, ok := groups[key]; !ok {
			groups[key] = &group{keyRow: row}
			order = append(order, key)
		}
		groups[key].rows = append(groups[key].rows, row)
	}

	// Build output columns
	var outCols []string
	outCols = append(outCols, r.GroupBy...)
	for _, af := range r.AggFields {
		name := af.Alias
		if name == "" {
			name = af.Function + "_" + af.Column
		}
		outCols = append(outCols, name)
	}

	// Compute aggregations
	var outRows []common.DataRow
	for _, key := range order {
		g := groups[key]
		outRow := make(common.DataRow)
		for _, col := range r.GroupBy {
			outRow[col] = g.keyRow[col]
		}
		for _, af := range r.AggFields {
			name := af.Alias
			if name == "" {
				name = af.Function + "_" + af.Column
			}
			outRow[name] = computeAgg(af.Function, af.Column, g.rows)
		}
		outRows = append(outRows, outRow)
	}

	ds.Columns = outCols
	ds.Rows = outRows
	return nil
}

func computeAgg(fn, col string, rows []common.DataRow) interface{} {
	switch strings.ToLower(fn) {
	case "count":
		return len(rows)
	case "sum":
		var sum float64
		for _, row := range rows {
			if f, ok := toAggFloat(row[col]); ok {
				sum += f
			}
		}
		return sum
	case "avg":
		var sum float64
		var count int
		for _, row := range rows {
			if f, ok := toAggFloat(row[col]); ok {
				sum += f
				count++
			}
		}
		if count == 0 {
			return 0.0
		}
		return sum / float64(count)
	case "min":
		var min float64
		first := true
		for _, row := range rows {
			if f, ok := toAggFloat(row[col]); ok {
				if first || f < min {
					min = f
					first = false
				}
			}
		}
		return min
	case "max":
		var max float64
		first := true
		for _, row := range rows {
			if f, ok := toAggFloat(row[col]); ok {
				if first || f > max {
					max = f
					first = false
				}
			}
		}
		return max
	default:
		return nil
	}
}

func toAggFloat(v interface{}) (float64, bool) {
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
