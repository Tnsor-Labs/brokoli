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
}

// compilePrefixToSQL renders a transform's row-wise prefix as a SELECT over
// srcQuery. ok is false when any rule in the prefix is not compilable, in
// which case the caller runs the whole transform in the engine -- prefixes are
// not split, because a half-pushed transform would need the engine to hold the
// intermediate anyway.
func compilePrefixToSQL(prefix []TransformRule, srcQuery string, srcColumns []string, dialect string) (prefixSQL, bool) {
	if srcQuery == "" || len(srcColumns) == 0 {
		return prefixSQL{}, false
	}
	if dialect != "postgres" {
		// Only Postgres is verified against the Go executor so far. Adding a
		// dialect means adding its differential run, not just its quoting.
		return prefixSQL{}, false
	}

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

	return prefixSQL{
		Query:   fmt.Sprintf("SELECT %s FROM (%s) AS brokoli_src", strings.Join(parts, ", "), srcQuery),
		Columns: out,
	}, true
}

// quoteIdentPG double-quotes an identifier, doubling any embedded quote, so a
// column name cannot terminate the identifier and inject SQL.
func quoteIdentPG(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
