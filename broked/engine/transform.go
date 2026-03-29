package engine

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/hc12r/brokolisql-go/pkg/common"
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
	GroupBy   []string   `json:"group_by,omitempty"`   // columns to group by
	AggFields []AggField `json:"agg_fields,omitempty"` // aggregation definitions
}

// AggField defines an aggregation operation on a column.
type AggField struct {
	Column   string `json:"column"`   // source column
	Function string `json:"function"` // sum, count, avg, min, max
	Alias    string `json:"alias"`    // output column name
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
	case "rename_columns":
		return renameColumns(r, ds)
	case "add_column":
		return addColumn(r, ds)
	case "filter_rows":
		return filterRows(r, ds)
	case "apply_function":
		return applyFunction(r, ds)
	case "replace_values":
		return replaceValues(r, ds)
	case "drop_columns":
		return dropColumns(r, ds)
	case "sort":
		return sortRows(r, ds)
	case "deduplicate":
		return deduplicate(r, ds)
	case "aggregate":
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
		if strings.Contains(r.Expression, "+") {
			parts := strings.Split(r.Expression, "+")
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
			row[r.Name] = result
		} else {
			row[r.Name] = r.Expression
		}
	}
	return nil
}

func filterRows(r TransformRule, ds *common.DataSet) error {
	if r.Condition == "" {
		return fmt.Errorf("filter_rows requires condition")
	}

	var kept []common.DataRow
	for _, row := range ds.Rows {
		if matchesCondition(r.Condition, row) {
			kept = append(kept, row)
		}
	}
	ds.Rows = kept
	return nil
}

func matchesCondition(cond string, row common.DataRow) bool {
	// Handle "column in [val1, val2, ...]"
	if strings.Contains(cond, " in ") {
		parts := strings.SplitN(cond, " in ", 2)
		col := strings.TrimSpace(parts[0])
		valuesStr := strings.Trim(strings.TrimSpace(parts[1]), "[]")
		values := strings.Split(valuesStr, ",")
		colVal := fmt.Sprintf("%v", row[col])
		for _, v := range values {
			if strings.Trim(v, " '\"") == colVal {
				return true
			}
		}
		return false
	}

	// Handle "column != value"
	if strings.Contains(cond, "!=") {
		parts := strings.SplitN(cond, "!=", 2)
		col := strings.TrimSpace(parts[0])
		val := strings.Trim(strings.TrimSpace(parts[1]), "'\"")
		return fmt.Sprintf("%v", row[col]) != val
	}

	// Handle "column = value"
	if strings.Contains(cond, "=") {
		parts := strings.SplitN(cond, "=", 2)
		col := strings.TrimSpace(parts[0])
		val := strings.Trim(strings.TrimSpace(parts[1]), "'\"")
		return fmt.Sprintf("%v", row[col]) == val
	}

	// No match = keep row
	return true
}

func applyFunction(r TransformRule, ds *common.DataSet) error {
	if r.Column == "" || r.Function == "" {
		return fmt.Errorf("apply_function requires column and function")
	}
	for _, row := range ds.Rows {
		if val, ok := row[r.Column]; ok {
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
	if len(r.AggFields) == 0 {
		return fmt.Errorf("aggregate requires agg_fields")
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
