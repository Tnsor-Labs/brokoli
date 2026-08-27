package engine

import (
	"math"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/dbdialect"
)

// #363: a carried schema is only worth having if every rule's effect on it
// is true. These tests are the table in applyRuleToSchema, stated as
// assertions -- and, for aggregate, checked against what the engine's own
// aggregation actually returns rather than against what it ought to.

func srcSchema() columnSchema {
	return columnSchema{
		"id":       {Class: dbdialect.TypeInt, Bits: 64, Nullable: false},
		"amount":   {Class: dbdialect.TypeDecimal, Precision: 12, Scale: 2, Nullable: true},
		"city":     {Class: dbdialect.TypeText, Nullable: true},
		"seen_at":  {Class: dbdialect.TypeTimestampTZ, Nullable: true},
		"is_admin": {Class: dbdialect.TypeBool, Nullable: false},
	}
}

func TestRowLevelRulesKeepEveryType(t *testing.T) {
	// Which rows survive says nothing about what the columns are.
	for _, ruleType := range []string{"filter_rows", "filter", "deduplicate", "dedup", "sort"} {
		out := applyRuleToSchema(TransformRule{Type: ruleType, Condition: "id > 3"}, srcSchema())
		if len(out) != len(srcSchema()) {
			t.Errorf("%s: kept %d of %d columns", ruleType, len(out), len(srcSchema()))
		}
		for col, want := range srcSchema() {
			if out[col] != want {
				t.Errorf("%s: %s = %v, want %v", ruleType, col, out[col], want)
			}
		}
	}
}

func TestDropAndRenameFollowTheColumns(t *testing.T) {
	dropped := applyRuleToSchema(
		TransformRule{Type: "drop_columns", Columns: []string{"city", "is_admin"}}, srcSchema())
	if _, still := dropped["city"]; still {
		t.Error("drop_columns left a dropped column in the schema")
	}
	if dropped["id"].Class != dbdialect.TypeInt {
		t.Error("drop_columns disturbed a column it was not asked about")
	}

	renamed := applyRuleToSchema(
		TransformRule{Type: "rename_columns", Mapping: map[string]string{"amount": "total"}}, srcSchema())
	if _, old := renamed["amount"]; old {
		t.Error("rename_columns left the old name behind")
	}
	got := renamed["total"]
	if got.Class != dbdialect.TypeDecimal || got.Precision != 12 || got.Scale != 2 {
		t.Errorf("rename_columns lost the type: total = %v, want decimal(12,2)", got)
	}
}

// The claim that matters most: degradation is per column, not per table.
func TestInvalidatingRulesTouchOnlyTheirOwnColumn(t *testing.T) {
	cases := []struct {
		name       string
		rule       TransformRule
		invalidate string
	}{
		{"add_column", TransformRule{Type: "add_column", Name: "label", Expression: "city + '!'"}, "label"},
		{"apply_function", TransformRule{Type: "apply_function", Column: "city", Function: "upper"}, "city"},
		{"replace_values", TransformRule{Type: "replace_values", Column: "city", Mapping: map[string]string{"a": "b"}}, "city"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := applyRuleToSchema(tc.rule, srcSchema())
			if _, known := out.known(tc.invalidate); known {
				t.Errorf("%s should have invalidated %q", tc.name, tc.invalidate)
			}
			// Everything it did not touch survives with full fidelity.
			for _, col := range []string{"id", "amount", "seen_at"} {
				if col == tc.invalidate {
					continue
				}
				if out[col] != srcSchema()[col] {
					t.Errorf("%s changed %q, which it does not touch: %v", tc.name, col, out[col])
				}
			}
		})
	}
}

// A rule this code has not been taught about could do anything, so the whole
// schema goes. This is the one per-table degradation, and it is deliberate.
func TestAnUnknownRuleInvalidatesEverything(t *testing.T) {
	if out := applyRuleToSchema(TransformRule{Type: "some_future_rule"}, srcSchema()); out != nil {
		t.Errorf("an unrecognised rule must invalidate the schema, got %v", out)
	}
	// And nothing later can bring it back.
	rules := []TransformRule{
		{Type: "some_future_rule"},
		{Type: "filter_rows", Condition: "id > 0"},
	}
	if out := applyRulesToSchema(rules, srcSchema()); out != nil {
		t.Errorf("a pass-through rule after an unknown one must not restore the schema, got %v", out)
	}
}

// The aggregate row of the table, checked against the engine rather than
// against the documentation.
//
// computeAgg pushes every value through toAggFloat, so sum, avg, min and max
// all return float64 -- min and max included, over an integer column. The
// schema must say float, because declaring "min keeps the column's type"
// would produce BIGINT DDL for a float64 value. This test fails if either
// side of that pairing changes.
func TestAggregateSchemaMatchesWhatAggregationReturns(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"city", "id", "amount"},
		Rows: []common.DataRow{
			{"city": "Lisbon", "id": int64(1), "amount": 10.5},
			{"city": "Lisbon", "id": int64(2), "amount": 20.5},
			{"city": "Porto", "id": int64(3), "amount": 5.0},
		},
	}
	rule := TransformRule{
		Type:    "aggregate",
		GroupBy: []string{"city"},
		AggFields: []AggField{
			{Column: "id", Function: "count"},
			{Column: "amount", Function: "sum"},
			{Column: "amount", Function: "avg"},
			{Column: "id", Function: "min"},
			{Column: "id", Function: "max"},
		},
	}

	schema := applyRuleToSchema(rule, srcSchema())

	// The group key is copied from a representative row, so it keeps its
	// type exactly.
	if schema["city"] != (dbdialect.ColumnType{Class: dbdialect.TypeText, Nullable: true}) {
		t.Errorf("group key lost its type: city = %v", schema["city"])
	}
	if _, carried := schema["id"]; carried {
		t.Error("a column that is only aggregated must not keep its own entry")
	}

	if err := ApplyTransforms([]TransformRule{rule}, ds); err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if len(ds.Rows) == 0 {
		t.Fatal("aggregate produced no rows")
	}

	// Every declared class must match the Go type actually produced.
	for _, col := range []string{"count_id", "sum_amount", "avg_amount", "min_id", "max_id"} {
		declared, known := schema.known(col)
		if !known {
			t.Errorf("%s has no declared type", col)
			continue
		}
		value := ds.Rows[0][col]
		switch value.(type) {
		case int:
			if declared.Class != dbdialect.TypeInt {
				t.Errorf("%s produces a Go int but is declared %s", col, declared)
			}
		case float64:
			if declared.Class != dbdialect.TypeFloat {
				t.Errorf("%s produces a Go float64 but is declared %s -- DDL would not hold the value",
					col, declared)
			}
		default:
			t.Errorf("%s produced %T, which the schema table does not account for", col, value)
		}
	}
}

// An aggregate function the engine does not implement returns nil, and a
// column of nils has no type to declare.
func TestUnsupportedAggregateDeclaresNoType(t *testing.T) {
	schema := aggregateSchema(TransformRule{
		GroupBy:   []string{"city"},
		AggFields: []AggField{{Column: "amount", Function: "median"}},
	}, srcSchema())
	if _, known := schema.known("median_amount"); known {
		t.Error("an unimplemented aggregate must not be given a type")
	}
}

// The alias is what names the output column, so the schema has to follow it.
func TestAggregateAliasNamesTheEntry(t *testing.T) {
	schema := aggregateSchema(TransformRule{
		GroupBy:   []string{"city"},
		AggFields: []AggField{{Column: "amount", Function: "sum", Alias: "revenue"}},
	}, srcSchema())
	if _, known := schema.known("revenue"); !known {
		t.Error("aggregate schema ignored the alias")
	}
	if _, wrong := schema["sum_amount"]; wrong {
		t.Error("aggregate schema used the derived name despite an alias")
	}
}

// `aggregations` is a documented alias for `agg_fields`, and a schema that
// ignored it would silently declare nothing for a whole class of pipelines.
func TestAggregateReadsTheAggregationsAlias(t *testing.T) {
	schema := aggregateSchema(TransformRule{
		GroupBy:      []string{"city"},
		Aggregations: []AggField{{Column: "amount", Function: "sum"}},
	}, srcSchema())
	if _, known := schema.known("sum_amount"); !known {
		t.Error("aggregate schema ignored the `aggregations` alias")
	}
}

func TestKnownRejectsUnknownClassAndNilSchema(t *testing.T) {
	var absent columnSchema
	if _, ok := absent.known("anything"); ok {
		t.Error("a nil schema must know nothing")
	}
	s := columnSchema{"c": {Class: dbdialect.TypeUnknown}}
	if _, ok := s.known("c"); ok {
		t.Error("an explicitly unknown class must not count as known")
	}
	if s.anyKnown([]string{"c"}) {
		t.Error("anyKnown must not count an unknown class")
	}
}

// Two defects #363's tests surfaced in code that was already there. Both
// only bite the create_table path, and both were invisible until a sink
// started creating tables from real column types beside inferred ones.

// A one-row load declared every column INTEGER, whatever was in it.
//
// threshold was int(total * 0.8), which is 0 for a single row, so the first
// test read "intCount >= 0" and matched unconditionally. A text column came
// out INTEGER and then failed to insert its own rows.
func TestInferenceNeedsAtLeastOneMatchingValue(t *testing.T) {
	one := []common.DataRow{{"city": "Lisbon", "n": "42", "f": "1.5", "b": "true"}}
	if got := inferColumnType("city", one); got != "TEXT" {
		t.Errorf("a single text value inferred as %s, want TEXT", got)
	}
	// The cases that should still work from one row.
	if got := inferColumnType("n", one); got != "INTEGER" {
		t.Errorf("a single integer value inferred as %s, want INTEGER", got)
	}
	if got := inferColumnType("f", one); got != "FLOAT" {
		t.Errorf("a single float value inferred as %s, want FLOAT", got)
	}
	if got := inferColumnType("b", one); got != "BOOLEAN" {
		t.Errorf("a single boolean value inferred as %s, want BOOLEAN", got)
	}
	// A column of nothing has nothing to infer from.
	if got := inferColumnType("missing", one); got != "TEXT" {
		t.Errorf("an absent column inferred as %s, want TEXT", got)
	}
	// And the majority rule is untouched where there is a majority.
	mixed := []common.DataRow{
		{"c": "1"}, {"c": "2"}, {"c": "3"}, {"c": "4"}, {"c": "nope"},
	}
	if got := inferColumnType("c", mixed); got != "INTEGER" {
		t.Errorf("four integers in five inferred as %s, want INTEGER", got)
	}
}

// pgx answers Length() with math.MaxInt64 for an unbounded `text` column, and
// the renderer turned that into VARCHAR(9223372036854775807) -- past the
// 10485760 Postgres accepts, so the server rejected the DDL outright. TEXT is
// the faithful unbounded type.
func TestPostgresRendersUnboundedTextAsText(t *testing.T) {
	d, ok := dbdialect.For("postgres")
	if !ok {
		t.Fatal("no postgres dialect")
	}
	renderer := d.(dbdialect.TypeRenderer)

	for _, length := range []int{0, math.MaxInt64, 10485761} {
		got, ok := renderer.DDLType(dbdialect.ColumnType{Class: dbdialect.TypeText, Length: length})
		if !ok {
			t.Errorf("length %d: refused, but TEXT holds it faithfully", length)
			continue
		}
		if got != "TEXT" {
			t.Errorf("length %d rendered as %q, want TEXT", length, got)
		}
	}
	// A length Postgres can actually express still becomes VARCHAR.
	if got, _ := renderer.DDLType(dbdialect.ColumnType{Class: dbdialect.TypeText, Length: 64}); got != "VARCHAR(64)" {
		t.Errorf("a bounded length rendered as %q, want VARCHAR(64)", got)
	}
}

// With no carried types, the DDL must be exactly what it was before this
// existed. That is the whole no-regression claim, so it is asserted against
// the old renderer rather than described.
func TestNoCarriedTypesRendersTheOldDDLExactly(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"id", "city", "amount"},
		Rows: []common.DataRow{
			{"id": "1", "city": "Lisbon", "amount": "10.5"},
			{"id": "2", "city": "Porto", "amount": "3.25"},
		},
	}
	for _, name := range []string{"postgres", "mysql", "sqlite"} {
		d := getDialect(name)
		want := d.createTable("t", ds.Columns, inferTypes(ds.Columns, ds.Rows))

		got, err := createTableDDL(d, SQLGenConfig{Dialect: name, Table: "t"}, ds)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got != want {
			t.Errorf("%s DDL changed with no carried types:\ngot:  %q\nwant: %q", name, got, want)
		}

		// A schema that describes none of these columns is worth no more
		// than no schema at all.
		got, err = createTableDDL(d, SQLGenConfig{
			Dialect: name, Table: "t",
			ColumnTypes: columnSchema{"elsewhere": {Class: dbdialect.TypeInt, Bits: 64}},
		}, ds)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got != want {
			t.Errorf("%s DDL changed for an irrelevant schema:\ngot:  %q\nwant: %q", name, got, want)
		}
	}
}

// The mixing rule, at the DDL level: carried where carried is known,
// inferred where it is not, in one statement.
func TestCarriedAndInferredTypesMixInOneTable(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"id", "note"},
		Rows:    []common.DataRow{{"id": "1", "note": "hello"}},
	}
	got, err := createTableDDL(getDialect("postgres"), SQLGenConfig{
		Dialect: "postgres", Table: "t",
		ColumnTypes: columnSchema{"id": {Class: dbdialect.TypeInt, Bits: 64, Nullable: false}},
	}, ds)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `"id" BIGINT NOT NULL`) {
		t.Errorf("the carried column lost its type:\n%s", got)
	}
	if !strings.Contains(got, `"note" TEXT`) {
		t.Errorf("the uncarried column was not inferred:\n%s", got)
	}
}

// A pipeline with nothing that would use a schema must not pay for one.
func TestSchemaWorkIsSkippedWhenNothingWouldUseIt(t *testing.T) {
	sink := func(createTable bool) models.Node {
		return models.Node{ID: "sink", Type: models.NodeTypeSinkDB, Config: map[string]interface{}{
			"uri": "postgres://x/y", "table": "t", "create_table": createTable,
		}}
	}
	src := models.Node{ID: "src", Type: models.NodeTypeSourceDB}

	r := &Runner{pipe: &models.Pipeline{Nodes: []models.Node{src, sink(false)}}}
	if r.schemaCarryWanted() {
		t.Error("a sink that does not create its table has no use for carried types")
	}
	r = &Runner{pipe: &models.Pipeline{Nodes: []models.Node{src, sink(true)}}}
	if !r.schemaCarryWanted() {
		t.Error("a create_table sink is exactly what carried types are for")
	}
	r = &Runner{}
	if r.schemaCarryWanted() {
		t.Error("a runner with no pipeline must not claim to want a schema")
	}
}

// A rename onto an occupied name leaves two columns sharing it, and which
// value survives is a map-iteration detail. Carrying either type would be
// carrying a coin flip.
func TestRenameOntoAnOccupiedNameInvalidatesIt(t *testing.T) {
	in := columnSchema{
		"a": {Class: dbdialect.TypeInt, Bits: 64},
		"b": {Class: dbdialect.TypeText},
	}
	out := applyRuleToSchema(TransformRule{Type: "rename_columns", Mapping: map[string]string{"a": "b"}}, in)
	if _, known := out.known("b"); known {
		t.Error("a collided name must not keep a type")
	}

	// A swap is not a collision: both names are accounted for.
	swapped := applyRuleToSchema(TransformRule{
		Type: "rename_columns", Mapping: map[string]string{"a": "b", "b": "a"}}, in)
	if got, known := swapped.known("b"); !known || got.Class != dbdialect.TypeInt {
		t.Errorf("a swap lost a type: b = %v", swapped["b"])
	}
	if got, known := swapped.known("a"); !known || got.Class != dbdialect.TypeText {
		t.Errorf("a swap lost a type: a = %v", swapped["a"])
	}
}

// applyRule and applyRuleToSchema are two switches over the same vocabulary,
// and they drift silently: a rule added to the first without the second
// still runs, it just quietly stops carrying types, and the only symptom is
// a create_table sink guessing again. This pins the pairing.
//
// A new transform type belongs in this list, in applyRule, and in
// applyRuleToSchema -- all three, or none.
func TestEveryTransformRuleHasASchemaRule(t *testing.T) {
	rules := map[string]TransformRule{
		"rename_columns": {Type: "rename_columns", Mapping: map[string]string{"city": "town"}},
		"rename":         {Type: "rename", Mapping: map[string]string{"city": "town"}},
		"add_column":     {Type: "add_column", Name: "n", Expression: "'x'"},
		"filter_rows":    {Type: "filter_rows", Condition: "id > 0"},
		"filter":         {Type: "filter", Condition: "id > 0"},
		"apply_function": {Type: "apply_function", Column: "city", Function: "upper"},
		"function":       {Type: "function", Column: "city", Function: "upper"},
		"replace_values": {Type: "replace_values", Column: "city", Mapping: map[string]string{"a": "b"}},
		"replace":        {Type: "replace", Column: "city", Mapping: map[string]string{"a": "b"}},
		"drop_columns":   {Type: "drop_columns", Columns: []string{"city"}},
		"drop":           {Type: "drop", Columns: []string{"city"}},
		"sort":           {Type: "sort", Columns: []string{"id"}, Ascending: true},
		"deduplicate":    {Type: "deduplicate", Columns: []string{"city"}},
		"dedup":          {Type: "dedup", Columns: []string{"city"}},
		"aggregate":      {Type: "aggregate", GroupBy: []string{"city"}, AggFields: []AggField{{Column: "id", Function: "count"}}},
		"agg":            {Type: "agg", GroupBy: []string{"city"}, AggFields: []AggField{{Column: "id", Function: "count"}}},
	}

	for name, rule := range rules {
		t.Run(name, func(t *testing.T) {
			// The engine really does implement it...
			ds := &common.DataSet{
				Columns: []string{"id", "city"},
				Rows:    []common.DataRow{{"id": int64(1), "city": "Lisbon"}},
			}
			if err := applyRule(rule, ds); err != nil {
				t.Fatalf("applyRule rejected %q: %v -- is it still a rule type?", name, err)
			}
			// ...so the schema table must have an answer for it, rather
			// than falling through to "anything could have happened".
			if out := applyRuleToSchema(rule, srcSchema()); out == nil {
				t.Errorf("%q runs but has no schema rule, so it silently stops carrying types", name)
			}
		})
	}

	// The fallthrough itself still has to work, or the guard above proves
	// nothing.
	if applyRuleToSchema(TransformRule{Type: "definitely_not_a_rule"}, srcSchema()) != nil {
		t.Error("an unrecognised rule must invalidate the schema")
	}
}
