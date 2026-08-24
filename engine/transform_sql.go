package engine

import (
	"fmt"
	"strings"
)

// Compiling a transform's row-wise prefix into SQL lets a source and sink on
// the same server do the work in the database, with no rows crossing the
// engine at all. The same plan already compiles to the Go streaming executor
// (stream_agg.go); this is a second backend for it.
//
// The hazard is that the two backends must agree. Brokoli's transform
// semantics are stringly-typed -- a value is rendered with fmt.Sprintf("%v")
// before it is compared or replaced, nil renders as "<nil>", and an ordered
// comparison is numeric only when BOTH sides parse as floats, byte-wise
// otherwise. SQL agrees with none of that by default: NULL comparisons yield
// NULL rather than comparing against "<nil>", ordering on text follows the
// database collation rather than byte order, and a numeric column compares
// numerically regardless of what the other side looks like.
//
// So a rule is compiled only when the two backends can be shown to produce
// identical results, and everything else falls back to the engine. A rule
// that is merely *probably* equivalent is not compiled: the failure mode is
// output that changes depending on where the planner decided to run it, which
// is the same class of bug as correctness varying with a memory threshold.
//
// Currently compiled: drop_columns and rename_columns, which are structural --
// they move and remove columns without inspecting a value, so there is no
// coercion, collation, or NULL question to get wrong. filter, apply_function,
// replace_values, and add_column are deliberately not compiled yet; see
// TestTransformSQLPrefixDifferential for what each of them would have to
// account for.
type prefixSQL struct {
	// Query is a SELECT wrapping the source query.
	Query string
	// Columns is the output column order, matching what the Go executor
	// would produce.
	Columns []string
	// SourceOf maps each output column back to the source column it came
	// from, so a later stage can still find its type after a rename.
	SourceOf map[string]string
}

// compilePrefixToSQL renders a transform's row-wise prefix as a SELECT over
// srcQuery. ok is false when any rule in the prefix is not compilable, in
// which case the caller runs the whole transform in the engine -- prefixes are
// not split, because a half-pushed transform would need the engine to hold the
// intermediate anyway.
func compilePrefixToSQL(prefix []TransformRule, srcQuery string, srcColumns []string, dialect string) (prefixSQL, bool) {
	return compilePrefixToSQLTyped(prefix, srcQuery, srcColumns, dialect, nil)
}

// compilePrefixToSQLTyped is compilePrefixToSQL with the source column types
// the caller was able to learn. Rules that need to know whether a column is
// text or numeric are compiled only when they are present; without them the
// compiler behaves as it did before types existed and declines those rules.
func compilePrefixToSQLTyped(prefix []TransformRule, srcQuery string, srcColumns []string, dialect string, kinds map[string]sqlColumnKind) (prefixSQL, bool) {
	if srcQuery == "" || len(srcColumns) == 0 {
		return prefixSQL{}, false
	}
	if dialect != "postgres" {
		// Only Postgres is verified against the Go executor so far. Adding a
		// dialect means adding its differential run, not just its quoting.
		return prefixSQL{}, false
	}

	var wheres []string

	// projection maps output column name -> source column name.
	type projected struct{ out, src string }
	cols := make([]projected, 0, len(srcColumns))
	for _, c := range srcColumns {
		cols = append(cols, projected{out: c, src: c})
	}

	for _, rule := range prefix {
		switch rule.Type {
		case "drop_columns", "drop":
			if len(rule.Columns) == 0 {
				return prefixSQL{}, false
			}
			drop := make(map[string]bool, len(rule.Columns))
			for _, c := range rule.Columns {
				drop[c] = true
			}
			kept := cols[:0:0]
			for _, c := range cols {
				if !drop[c.out] {
					kept = append(kept, c)
				}
			}
			cols = kept

		case "rename_columns", "rename":
			if len(rule.Mapping) == 0 {
				return prefixSQL{}, false
			}
			// The Go executor renames by writing the new key and deleting the
			// old one. If a rename collides with a column that already exists
			// the surviving value depends on map iteration order, so refuse
			// rather than pick one.
			existing := make(map[string]bool, len(cols))
			for _, c := range cols {
				existing[c.out] = true
			}
			for old, new := range rule.Mapping {
				if !existing[old] {
					continue
				}
				if existing[new] && new != old {
					return prefixSQL{}, false
				}
			}
			for i, c := range cols {
				if nn, ok := rule.Mapping[c.out]; ok {
					cols[i].out = nn
				}
			}

		case "filter_rows", "filter":
			if rule.Condition == "" {
				return prefixSQL{}, false
			}
			// The filter reads the columns as they stand at this point in the
			// prefix, so its type lookup has to follow renames and drops
			// rather than using the source names.
			current := make(map[string]sqlColumnRef, len(cols))
			for _, c := range cols {
				if k, ok := kinds[c.src]; ok {
					current[c.out] = sqlColumnRef{Ident: quoteIdentPG(c.src), Kind: k}
				}
			}
			expr, ok := compileFilterToSQL(rule.Condition, current)
			if !ok {
				return prefixSQL{}, false
			}
			wheres = append(wheres, expr)

		default:
			// Not proven equivalent yet.
			return prefixSQL{}, false
		}
	}

	if len(cols) == 0 {
		return prefixSQL{}, false
	}

	parts := make([]string, 0, len(cols))
	out := make([]string, 0, len(cols))
	for _, c := range cols {
		if c.out == c.src {
			parts = append(parts, quoteIdentPG(c.src))
		} else {
			parts = append(parts, quoteIdentPG(c.src)+" AS "+quoteIdentPG(c.out))
		}
		out = append(out, c.out)
	}

	sourceOf := make(map[string]string, len(cols))
	for _, c := range cols {
		sourceOf[c.out] = c.src
	}
	query := fmt.Sprintf("SELECT %s FROM (%s) AS brokoli_src", strings.Join(parts, ", "), srcQuery)
	if len(wheres) > 0 {
		// A filter partway through a prefix still applies to the whole
		// projection: every rule before it is a projection change, none of
		// which can remove a row, so ordering the WHERE last is equivalent.
		query += " WHERE " + strings.Join(wheres, " AND ")
	}
	return prefixSQL{
		Query:    query,
		Columns:  out,
		SourceOf: sourceOf,
	}, true
}

// quoteIdentPG double-quotes an identifier, doubling any embedded quote, so a
// column name cannot terminate the identifier and inject SQL.
func quoteIdentPG(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// compilePlanToSQL compiles a whole transform plan -- the row-wise prefix and
// the aggregate that may follow it -- into one statement.
//
// A plan with rules after the aggregate is refused. Those run against grouped
// output, which is a different set of columns from anything the prefix
// compiler has typed, so pushing the front half down would leave the engine
// holding the aggregated result anyway.
func compilePlanToSQL(plan transformStreamPlan, srcQuery string, srcColumns []string, dialect string, kinds map[string]sqlColumnKind) (prefixSQL, bool) {
	base, ok := compilePrefixToSQLTyped(plan.prefix, srcQuery, srcColumns, dialect, kinds)
	if !ok {
		return prefixSQL{}, false
	}
	if plan.agg == nil {
		return base, true
	}
	if len(plan.suffix) > 0 {
		return prefixSQL{}, false
	}

	// After the prefix, a column is named by its output name and reachable in
	// the outer query by that name, since the aggregate wraps the projection
	// rather than sitting beside it.
	visible := make(map[string]sqlColumnRef, len(base.Columns))
	for _, c := range base.Columns {
		kind := kindUnclassified
		// Resolve through the rename map: the aggregate names a column as the
		// prefix left it, while the types are keyed by what the source called
		// it.
		if src, ok := base.SourceOf[c]; ok {
			if k, ok := kinds[src]; ok {
				kind = k
			}
		}
		visible[c] = sqlColumnRef{Ident: quoteIdentPG(c), Kind: kind}
	}

	selectList, groupBy, outCols, ok := compileAggregateToSQL(*plan.agg, visible)
	if !ok {
		return prefixSQL{}, false
	}

	return prefixSQL{
		Query: fmt.Sprintf("SELECT %s FROM (%s) AS brokoli_agg GROUP BY %s",
			strings.Join(selectList, ", "), base.Query, strings.Join(groupBy, ", ")),
		Columns: outCols,
	}, true
}
