package engine

import (
	"fmt"
	"strings"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// ADR-019 Milestone 2: streaming hash aggregation. An aggregate rule used
// to be a hard barrier — one `aggregate` anywhere in a transform's rule
// list forced the whole node onto the batch path, materializing its full
// input. But aggregation state is small (one accumulator per group per
// field), so the common ETL shape — row-local filtering/derivation into a
// group-by — streams naturally: the row-local prefix runs per batch, the
// aggregate folds each batch into group state, and everything AFTER the
// aggregate (even sort) runs on the grouped output, which is small by
// construction.

// transformStreamPlan is how a transform node's rule list executes under
// streaming: prefix rules stream per batch; agg (if any) folds batches
// into group state; suffix rules apply to the materialized aggregate
// output.
type transformStreamPlan struct {
	prefix []TransformRule
	agg    *TransformRule
	suffix []TransformRule
}

// planTransformRules classifies a rule list for streaming. ok is false
// when the list cannot stream: a blocking rule (sort/dedup) appearing
// BEFORE any aggregation still needs the whole dataset at once and keeps
// the batch path, exactly as before. Rules after the aggregate may be
// anything — including sort — because they operate on the grouped output.
func planTransformRules(rules []TransformRule) (transformStreamPlan, bool) {
	var plan transformStreamPlan
	for i, rule := range rules {
		switch rule.Type {
		case "rename_columns", "rename",
			"add_column",
			"filter_rows", "filter",
			"apply_function", "function",
			"replace_values", "replace",
			"drop_columns", "drop":
			plan.prefix = append(plan.prefix, rule)
		case "aggregate", "agg":
			r := rule
			plan.agg = &r
			plan.suffix = append(plan.suffix, rules[i+1:]...)
			return plan, true
		default:
			return transformStreamPlan{}, false
		}
	}
	return plan, true
}

// aggAccumulator holds one aggregation function's running state for one
// group. Every branch replicates computeAgg (transform.go) exactly,
// including its edges: count counts ALL rows in the group regardless of
// the column's value; sum/avg/min/max fold only values toAggFloat
// accepts; min/max over zero accepted values return 0.0 (computeAgg's
// `first` flag never clears, leaving the zero value).
type aggAccumulator struct {
	count    int
	sum      float64
	avgSum   float64
	avgCount int
	min      float64
	max      float64
	sawAny   bool
}

func (a *aggAccumulator) add(v interface{}) {
	a.count++
	if f, ok := toAggFloat(v); ok {
		a.sum += f
		a.avgSum += f
		a.avgCount++
		if !a.sawAny || f < a.min {
			a.min = f
		}
		if !a.sawAny || f > a.max {
			a.max = f
		}
		a.sawAny = true
	}
}

func (a *aggAccumulator) result(fn string) interface{} {
	switch strings.ToLower(fn) {
	case "count":
		return a.count
	case "sum":
		return a.sum
	case "avg":
		if a.avgCount == 0 {
			return 0.0
		}
		return a.avgSum / float64(a.avgCount)
	case "min":
		return a.min // 0.0 when no accepted values, matching computeAgg
	case "max":
		return a.max
	default:
		return nil
	}
}

// streamAggState folds batches into per-group accumulators. Group
// identity, first-seen ordering, key-row capture, output column naming
// (alias, else fn_col), and the agg_fields/aggregations compat alias all
// mirror transform.go's aggregate() one for one — the equivalence test
// holds the two implementations to byte-identical results.
type streamAggState struct {
	rule   TransformRule
	fields []AggField
	groups map[string]*streamAggGroup
	order  []string
}

type streamAggGroup struct {
	keyRow common.DataRow
	accs   []aggAccumulator // one per field, same index order as fields
}

func newStreamAggState(rule TransformRule) (*streamAggState, error) {
	if len(rule.GroupBy) == 0 {
		return nil, fmt.Errorf("aggregate requires group_by columns")
	}
	fields := rule.AggFields
	if len(fields) == 0 && len(rule.Aggregations) > 0 {
		fields = rule.Aggregations
	}
	if len(fields) == 0 {
		return nil, fmt.Errorf("aggregate requires at least one aggregation (e.g. sum, count, avg) — add aggregation fields in the transform config")
	}
	return &streamAggState{
		rule:   rule,
		fields: fields,
		groups: make(map[string]*streamAggGroup),
	}, nil
}

func (s *streamAggState) fold(batch *common.DataSet) {
	for _, row := range batch.Rows {
		var parts []string
		for _, col := range s.rule.GroupBy {
			parts = append(parts, fmt.Sprintf("%v", row[col]))
		}
		key := strings.Join(parts, "\x00")
		g, ok := s.groups[key]
		if !ok {
			g = &streamAggGroup{keyRow: row, accs: make([]aggAccumulator, len(s.fields))}
			s.groups[key] = g
			s.order = append(s.order, key)
		}
		for i, af := range s.fields {
			g.accs[i].add(row[af.Column])
		}
	}
}

func (s *streamAggState) finalize() *common.DataSet {
	outCols := append([]string{}, s.rule.GroupBy...)
	for _, af := range s.fields {
		name := af.Alias
		if name == "" {
			name = af.Function + "_" + af.Column
		}
		outCols = append(outCols, name)
	}
	out := &common.DataSet{Columns: outCols}
	for _, key := range s.order {
		g := s.groups[key]
		row := make(common.DataRow)
		for _, col := range s.rule.GroupBy {
			row[col] = g.keyRow[col]
		}
		for i, af := range s.fields {
			name := af.Alias
			if name == "" {
				name = af.Function + "_" + af.Column
			}
			row[name] = g.accs[i].result(af.Function)
		}
		out.Rows = append(out.Rows, row)
	}
	return out
}
