package engine

import (
	"context"
	"database/sql"
	"os"
	"reflect"
	"testing"
)

// Aggregates run both ways and are compared, like the row-wise rules. The Go
// implementation goes through float64 and treats a group with nothing
// convertible in it as zero rather than null, so the generated SQL has to do
// both; anything that cannot be shown to match is refused.
func TestTransformSQLAggregateDifferential(t *testing.T) {
	uri := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if uri == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	db, err := sql.Open("pgx", uri)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	setupDiffTable(t, db)

	const srcQuery = `SELECT id, name, city, amount, note, flag FROM ` + diffSrcTable
	srcCols := []string{"id", "name", "city", "amount", "note", "flag"}

	kinds, err := describeQueryColumns(context.Background(), uri, srcQuery)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name         string
		rules        []TransformRule
		wantCompiled bool
		why          string
	}{
		{
			name: "count by a text column",
			rules: []TransformRule{{Type: "aggregate", GroupBy: []string{"city"},
				AggFields: []AggField{{Function: "count", Column: "id"}}}},
			wantCompiled: true,
		},
		{
			name: "min and max over a numeric column",
			rules: []TransformRule{{Type: "aggregate", GroupBy: []string{"city"},
				AggFields: []AggField{{Function: "min", Column: "amount"}, {Function: "max", Column: "amount"}}}},
			wantCompiled: true,
		},
		{
			name: "aliases are honoured",
			rules: []TransformRule{{Type: "aggregate", GroupBy: []string{"city"},
				AggFields: []AggField{{Function: "count", Column: "id", Alias: "n"}}}},
			wantCompiled: true,
		},
		{
			name: "a filter before the aggregate",
			rules: []TransformRule{
				{Type: "filter_rows", Condition: "name != alice"},
				{Type: "aggregate", GroupBy: []string{"city"}, AggFields: []AggField{{Function: "count", Column: "id"}}},
			},
			wantCompiled: true,
		},
		{
			name: "grouping by a renamed text column",
			rules: []TransformRule{
				{Type: "rename_columns", Mapping: map[string]string{"city": "town"}},
				{Type: "aggregate", GroupBy: []string{"town"}, AggFields: []AggField{{Function: "count", Column: "id"}}},
			},
			wantCompiled: true,
		},
		{
			name: "sum is refused",
			rules: []TransformRule{{Type: "aggregate", GroupBy: []string{"city"},
				AggFields: []AggField{{Function: "sum", Column: "amount"}}}},
			wantCompiled: false,
			why: "float addition is not associative and the database may aggregate in a different order " +
				"than the engine reads rows, so the two can differ in the last bits. The fix is to make " +
				"the engine's summation order-independent, not to assume the orders match.",
		},
		{
			name: "avg is refused",
			rules: []TransformRule{{Type: "aggregate", GroupBy: []string{"city"},
				AggFields: []AggField{{Function: "avg", Column: "amount"}}}},
			wantCompiled: false,
			why:          "same associativity problem as sum",
		},
		{
			name: "grouping by a numeric column is refused",
			rules: []TransformRule{{Type: "aggregate", GroupBy: []string{"amount"},
				AggFields: []AggField{{Function: "count", Column: "id"}}}},
			wantCompiled: false,
			why: "Go groups on the rendered value, so 10.50 and 10.5 are different groups where " +
				"SQL considers them one",
		},
		{
			name: "min over a text column is refused",
			rules: []TransformRule{{Type: "aggregate", GroupBy: []string{"city"},
				AggFields: []AggField{{Function: "min", Column: "name"}}}},
			wantCompiled: false,
			why:          "toAggFloat skips values that do not parse; a SQL cast would error on the first one",
		},
		{
			name: "a rule after the aggregate is refused",
			rules: []TransformRule{
				{Type: "aggregate", GroupBy: []string{"city"}, AggFields: []AggField{{Function: "count", Column: "id"}}},
				{Type: "drop_columns", Columns: []string{"count_id"}},
			},
			wantCompiled: false,
			why:          "it runs against grouped output, whose columns the prefix compiler has not typed",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, ok := planTransformRules(tc.rules)
			if !ok {
				t.Fatalf("planTransformRules refused %v", tc.rules)
			}
			compiled, gotCompiled := compilePlanToSQL(plan, srcQuery, srcCols, "postgres", kinds)
			if gotCompiled != tc.wantCompiled {
				if tc.wantCompiled {
					t.Fatalf("compiler declined a case this test expects it to push down")
				}
				t.Fatalf("compiler pushed down a case that is not proven equivalent (%s)", tc.why)
			}
			if !tc.wantCompiled {
				t.Logf("not pushed down, by design: %s", tc.why)
				return
			}

			goDS, err := QueryDatabase(uri, srcQuery)
			if err != nil {
				t.Fatal(err)
			}
			if err := ApplyTransforms(tc.rules, goDS); err != nil {
				t.Fatalf("go executor: %v", err)
			}
			sqlDS, err := QueryDatabase(uri, compiled.Query)
			if err != nil {
				t.Fatalf("generated SQL failed: %v\n%s", err, compiled.Query)
			}

			if !reflect.DeepEqual(compiled.Columns, goDS.Columns) {
				t.Errorf("column order differs:\n  sql %v\n  go  %v", compiled.Columns, goDS.Columns)
			}
			got := normalize(compiled.Columns, sqlDS.Rows)
			want := normalize(goDS.Columns, goDS.Rows)
			if len(got) != len(want) {
				t.Fatalf("group count differs: sql %d, go %d\n%s\n sql=%v\n go =%v",
					len(got), len(want), compiled.Query, got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("group %d differs:\n  sql %s\n  go  %s\n\n%s", i, got[i], want[i], compiled.Query)
				}
			}
		})
	}
}

// sum and avg are refused because float addition is not associative, and that
// refusal should rest on a measurement rather than on the general principle.
//
// The measurement is not "the two disagree today" -- with a plain sequential
// scan they agree, because the database happens to aggregate in the order the
// engine reads. It is that the answer depends on an order the database is free
// to choose: the same four values summed in a different order give a different
// result, and not in the last bits.
//
// A parallel aggregate, an index scan, or a merge join upstream is enough to
// change that order. Pushing sum down would make the result depend on the
// query plan, which is not a property anyone should have to reason about.
func TestSumIsOrderSensitive(t *testing.T) {
	uri := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if uri == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	db, err := sql.Open("pgx", uri)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for _, s := range []string{
		`DROP TABLE IF EXISTS brokoli_sum_order`,
		`CREATE TABLE brokoli_sum_order (v float8)`,
		`INSERT INTO brokoli_sum_order VALUES (1e16), (1.0), (-1e16), (1.0)`,
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _, _ = db.Exec(`DROP TABLE IF EXISTS brokoli_sum_order`) })

	var asStored, reordered float64
	if err := db.QueryRow(`SELECT SUM(v) FROM brokoli_sum_order`).Scan(&asStored); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT SUM(v) FROM (SELECT v FROM brokoli_sum_order ORDER BY v) t`).Scan(&reordered); err != nil {
		t.Fatal(err)
	}

	if asStored == reordered {
		t.Fatalf("summation is no longer order-sensitive on this server (%v both ways), "+
			"so the refusal of sum in compileAggregateToSQL needs revisiting", asStored)
	}
	t.Logf("same four values, two orders: %v and %v", asStored, reordered)
}
