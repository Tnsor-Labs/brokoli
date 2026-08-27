package engine

import (
	"strings"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/dbdialect"
)

// Carrying a column's real type across the node boundary, so a sink_db with
// create_table stops guessing it from a sample of values (#363).
//
// #362 fixed this for migrate, which knows both endpoints and can probe the
// source directly. sink_db cannot: by the time rows reach a sink they are a
// common.DataSet, which is column names and rows, so create_table fell back
// to inferColumnType -- rendering each value with %v and trying to parse it.
// That cannot recover what the source declared, with the consequences #360
// documented: a bigint whose sample fits in an int32 becomes INT, an exact
// decimal becomes a float, a timestamp becomes text.
//
// # Where this rides, and what it deliberately is not
//
// A columnSchema is run-scoped state in nodeOutputs, alongside the inline,
// spilled and TableRef output forms. Not on DataSet and not on DatasetRef:
// DataSet would be a pkg/common change, and DatasetRef is serialised, so
// putting types there is a wire-format decision and an ADR-023 change. This
// is neither. Nothing is persisted, so there is nothing to version.
//
// The cost is that a schema does not survive resume-from-artifact and does
// not cross a remote worker boundary. In both cases the sink falls back to
// inference, which is exactly today's behaviour -- nothing breaks, it just
// does not improve.
//
// # Absence is the safe answer
//
// A column with no entry is unknown, and unknown means "infer it", not
// "fail". Every rule below states what it does to a type, and a rule that
// cannot say invalidates the columns it touched rather than guessing.
//
// Degradation is per column, not per table: a dataset where one column went
// through add_column still gets exact types for the other nine.

// columnSchema is what a node's output columns are, by name. A column absent
// from the map has no known type; so does one whose class is TypeUnknown.
type columnSchema map[string]dbdialect.ColumnType

// known reports whether this column has a type worth rendering DDL from.
func (s columnSchema) known(col string) (dbdialect.ColumnType, bool) {
	if s == nil {
		return dbdialect.ColumnType{}, false
	}
	ct, ok := s[col]
	return ct, ok && ct.Class != dbdialect.TypeUnknown
}

// anyKnown reports whether the schema describes at least one of these
// columns. A schema that describes none is worth no more than no schema, and
// the caller keeps the path it had.
func (s columnSchema) anyKnown(columns []string) bool {
	for _, c := range columns {
		if _, ok := s.known(c); ok {
			return true
		}
	}
	return false
}

func (s columnSchema) clone() columnSchema {
	if s == nil {
		return nil
	}
	out := make(columnSchema, len(s))
	for k, v := range s {
		out[k] = v
	}
	return out
}

// applyRuleToSchema returns the schema of a transform rule's output, given
// its input's.
//
// The table:
//
//	filter_rows, deduplicate, sort  pass through unchanged (rows only)
//	drop_columns                    drop the entry
//	rename_columns                  follow the rename
//	add_column                      the new column is unknown
//	apply_function                  the touched column is unknown
//	replace_values                  the touched column is unknown
//	aggregate                       group keys keep their type; see below
//
// An unrecognised rule invalidates everything, because a rule this function
// has not been taught about may do anything. That is the one case where
// degradation is per table rather than per column, and it is deliberate:
// a new transform type must be considered here before it can claim to
// preserve a type.
func applyRuleToSchema(rule TransformRule, in columnSchema) columnSchema {
	switch rule.Type {
	case "filter_rows", "filter", "deduplicate", "dedup", "sort":
		// Row-level operations. Which rows survive does not change what
		// the columns are.
		return in.clone()

	case "drop_columns", "drop":
		out := in.clone()
		for _, c := range rule.Columns {
			delete(out, c)
		}
		return out

	case "rename_columns", "rename":
		out := make(columnSchema, len(in))
		for col, ct := range in {
			if newName, renamed := rule.Mapping[col]; renamed {
				out[newName] = ct
				continue
			}
			out[col] = ct
		}
		// A rename onto a name that is still occupied leaves the dataset
		// with two columns of that name, and which value survives is a
		// map-iteration detail. Rather than carry whichever type won,
		// drop it: the answer is genuinely not knowable.
		for from, to := range rule.Mapping {
			if _, occupied := in[to]; !occupied || to == from {
				continue
			}
			if _, movedAway := rule.Mapping[to]; !movedAway {
				delete(out, to)
			}
		}
		return out

	case "add_column":
		// The expression language has three forms (concatenation,
		// arithmetic, literal) and picks between them per row from the
		// values it finds, so the result type is not a property of the
		// rule. Unknown, and the other columns are untouched.
		out := in.clone()
		delete(out, rule.Name)
		return out

	case "apply_function", "function":
		// Every function today (lower, upper, trim, title) writes a
		// string back, so text would be accurate right now. Unknown is
		// still the answer: the rule is what is being described, not
		// this moment's implementation of it, and a numeric function
		// added later must not silently inherit a claim of text.
		out := in.clone()
		delete(out, rule.Column)
		return out

	case "replace_values", "replace":
		// Mapping is map[string]string, so a replaced value is a string
		// while an unreplaced one keeps its type. The column is mixed,
		// which is exactly what unknown means.
		out := in.clone()
		delete(out, rule.Column)
		return out

	case "aggregate", "agg":
		return aggregateSchema(rule, in)

	default:
		// Not a rule this function knows. Anything could have happened.
		return nil
	}
}

// aggregateSchema is the aggregate rule's effect, which is the only one that
// depends on what the engine's own aggregation actually returns.
//
// Group keys are copied from a representative row, so they keep their type.
// The aggregates do not: computeAgg pushes every value through toAggFloat,
// so sum, avg, min and max all return a float64 -- min and max included,
// even over an integer or an exact decimal column. Declaring that they
// "keep the column's type" would produce DDL the values no longer fit; a
// decimal column reduced to a float64 has already lost its exactness before
// any DDL is written, and the destination should say so.
//
// count returns len(rows), a Go int.
func aggregateSchema(rule TransformRule, in columnSchema) columnSchema {
	fields := rule.AggFields
	if len(fields) == 0 {
		fields = rule.Aggregations
	}

	out := make(columnSchema, len(rule.GroupBy)+len(fields))
	for _, key := range rule.GroupBy {
		if ct, ok := in[key]; ok {
			out[key] = ct
		}
	}
	for _, af := range fields {
		name := af.Alias
		if name == "" {
			name = af.Function + "_" + af.Column
		}
		switch strings.ToLower(af.Function) {
		case "count":
			out[name] = dbdialect.ColumnType{Class: dbdialect.TypeInt, Bits: 64, Nullable: false}
		case "sum", "avg", "min", "max":
			out[name] = dbdialect.ColumnType{Class: dbdialect.TypeFloat, Bits: 64, Nullable: true}
		default:
			// computeAgg returns nil for anything else, and a column of
			// nils has no type to declare.
		}
	}
	return out
}

// applyRulesToSchema folds a node's whole rule list over a schema. A nil
// result at any point stays nil: once the shape is unknown, nothing later
// can make it known again.
func applyRulesToSchema(rules []TransformRule, in columnSchema) columnSchema {
	out := in
	for _, rule := range rules {
		if out == nil {
			return nil
		}
		out = applyRuleToSchema(rule, out)
	}
	return out
}

// schemaCarryWanted reports whether anything in this pipeline would use a
// carried schema.
//
// Only sink_db with create_table does: every other consumer either writes to
// a table that already exists, or never renders DDL at all. Asking keeps the
// feature free when it is not needed -- a source that would have to probe
// its server for types skips the round trip entirely, so the common
// source_db pipeline pays nothing for a facility it does not use.
func (r *Runner) schemaCarryWanted() bool {
	if r.pipe == nil {
		return false
	}
	for _, n := range r.pipe.Nodes {
		if n.Type == models.NodeTypeSinkDB && configBool(n.Config["create_table"]) {
			return true
		}
	}
	return false
}

// countKnown is how many of these columns the schema can actually describe,
// for a log line that says what fidelity the table was created with.
func (s columnSchema) countKnown(columns []string) int {
	n := 0
	for _, c := range columns {
		if _, ok := s.known(c); ok {
			n++
		}
	}
	return n
}
