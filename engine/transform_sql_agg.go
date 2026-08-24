package engine

import (
	"fmt"
	"strings"
)

// compileAggregateToSQL renders an aggregate rule as a GROUP BY over the
// projection the prefix produced, or reports that it cannot.
//
// The Go implementation aggregates through float64 and treats a group with no
// convertible values as zero rather than null, so the SQL has to do both. What
// it cannot reproduce is summation order: IEEE-754 addition is not
// associative, and Postgres is free to aggregate in a different order than the
// engine reads rows -- a parallel plan or an index scan is enough to change
// it. sum and avg are therefore refused, and the fix for that is to make the
// engine's own summation order-independent, not to pretend the two agree.
//
// count, min, and max are order-independent and exact, so they compile.
func compileAggregateToSQL(rule TransformRule, cols map[string]sqlColumnRef) (selectList []string, groupBy []string, outCols []string, ok bool) {
	if len(rule.GroupBy) == 0 {
		return nil, nil, nil, false
	}
	fields := rule.AggFields
	if len(fields) == 0 {
		fields = rule.Aggregations
	}
	if len(fields) == 0 {
		return nil, nil, nil, false
	}

	for _, g := range rule.GroupBy {
		ref, known := cols[g]
		if !known {
			return nil, nil, nil, false
		}
		// Grouping happens on the fmt.Sprintf("%v") rendering in Go, so it
		// only matches SQL's grouping when rendering is the identity. On a
		// numeric column it is not: 10.50 and 10.5 render differently and
		// land in different groups, where SQL considers them equal.
		if ref.Kind != kindText {
			return nil, nil, nil, false
		}
		selectList = append(selectList, ref.Ident+" AS "+quoteIdentPG(g))
		groupBy = append(groupBy, ref.Ident)
		outCols = append(outCols, g)
	}

	for _, af := range fields {
		name := af.Alias
		if name == "" {
			name = af.Function + "_" + af.Column
		}
		expr, ok := aggExprSQL(af, cols)
		if !ok {
			return nil, nil, nil, false
		}
		selectList = append(selectList, expr+" AS "+quoteIdentPG(name))
		outCols = append(outCols, name)
	}
	return selectList, groupBy, outCols, true
}

func aggExprSQL(af AggField, cols map[string]sqlColumnRef) (string, bool) {
	switch strings.ToLower(af.Function) {
	case "count":
		// The Go implementation returns len(rows): it counts every row in the
		// group and never looks at the column, so this is COUNT(*) and not
		// COUNT(column), which would skip nulls.
		return "COUNT(*)", true

	case "min", "max":
		ref, known := cols[af.Column]
		if !known || ref.Kind != kindNumeric {
			// A text column would have to reproduce toAggFloat, which skips
			// values that do not parse rather than failing, where a SQL cast
			// would error on the first one.
			return "", false
		}
		fn := strings.ToUpper(strings.ToLower(af.Function))
		// COALESCE to zero: a group whose values are all null contributes
		// nothing in Go, which leaves min/max at their zero value rather than
		// returning null.
		return fmt.Sprintf("COALESCE(%s(CAST(%s AS DOUBLE PRECISION)), 0)", fn, ref.Ident), true

	default:
		// sum and avg: see the note on compileAggregateToSQL.
		return "", false
	}
}
