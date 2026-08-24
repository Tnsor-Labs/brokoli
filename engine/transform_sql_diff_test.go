package engine

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// The transform plan has two backends: the Go streaming executor and, for the
// row-wise prefix, generated SQL. A pipeline must produce the same rows
// whichever one the planner picks, or output starts depending on where the
// work happened to run -- the same class of bug as correctness varying with a
// memory threshold.
//
// So every rule is run BOTH ways against a real Postgres and compared. Rules
// the compiler declines are asserted to be declined, which pins the set: a
// rule cannot start being pushed down without a passing comparison here.
//
// Needs a live server, because the divergences at issue are collation, NULL
// handling, and numeric coercion, all of which a fake would define away.
// Point BROKOLI_TEST_POSTGRES_URL at one.

const diffSrcTable = "brokoli_xform_diff"

func setupDiffTable(t *testing.T, db *sql.DB) {
	t.Helper()
	stmts := []string{
		`DROP TABLE IF EXISTS ` + diffSrcTable,
		`CREATE TABLE ` + diffSrcTable + ` (
			id bigint, name text, city text, amount numeric, note text, flag boolean
		)`,
		`INSERT INTO ` + diffSrcTable + ` VALUES
			(1,  'alice', 'Lisbon',  10.5, 'first',  true),
			(2,  'BOB',   'porto',   99,   NULL,     false),
			(3,  '  carol  ', 'Faro', 100,  'third',  true),
			(4,  'dave',  NULL,      100.5,'fourth', NULL),
			(5,  NULL,    'Braga',   NULL, 'fifth',  false),
			(6,  'eve',   'lisbon',  -3,   '',       true),
			(7,  '9',     '10',      9,    'ninth',  false),
			(8,  '10',    '9',       10,   'tenth',  true)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}
	t.Cleanup(func() { _, _ = db.Exec(`DROP TABLE IF EXISTS ` + diffSrcTable) })
}

// normalize renders a dataset as comparable text. Both backends are compared
// on the values a downstream node would see, which is what actually has to
// match, rather than on Go types that never leave the process.
func normalize(cols []string, rows []common.DataRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		fields := make([]string, 0, len(cols))
		for _, c := range cols {
			fields = append(fields, fmt.Sprintf("%v", r[c]))
		}
		out = append(out, fmt.Sprint(fields))
	}
	sort.Strings(out)
	return out
}

func TestTransformSQLPrefixDifferential(t *testing.T) {
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

	cases := []struct {
		name         string
		rules        []TransformRule
		wantCompiled bool
		// why records what the compiler would have to prove before this rule
		// can be pushed down. It is documentation with a test attached.
		why string
	}{
		{
			name:         "drop_columns",
			rules:        []TransformRule{{Type: "drop_columns", Columns: []string{"note", "flag"}}},
			wantCompiled: true,
		},
		{
			name:         "rename_columns",
			rules:        []TransformRule{{Type: "rename_columns", Mapping: map[string]string{"name": "full_name", "city": "town"}}},
			wantCompiled: true,
		},
		{
			name: "drop then rename",
			rules: []TransformRule{
				{Type: "drop_columns", Columns: []string{"note"}},
				{Type: "rename_columns", Mapping: map[string]string{"amount": "value"}},
			},
			wantCompiled: true,
		},
		{
			name:         "rename onto an existing column is refused",
			rules:        []TransformRule{{Type: "rename_columns", Mapping: map[string]string{"name": "city"}}},
			wantCompiled: false,
			why:          "the Go executor's surviving value depends on map iteration order",
		},
		{
			name:         "filter numeric",
			rules:        []TransformRule{{Type: "filter_rows", Condition: "amount > 99"}},
			wantCompiled: true,
		},
		{
			name:         "filter on text that looks numeric",
			rules:        []TransformRule{{Type: "filter_rows", Condition: "city > 9"}},
			wantCompiled: false,
			why:          "city is text; Go parses '10' as a number and compares numerically, SQL compares by collation, so 9 vs 10 order differently",
		},
		{
			name:         "filter equality on text",
			rules:        []TransformRule{{Type: "filter_rows", Condition: "name = alice"}},
			wantCompiled: true,
		},
		{
			name:         "apply_function upper",
			rules:        []TransformRule{{Type: "apply_function", Column: "name", Function: "upper"}},
			wantCompiled: false,
			why: "on a text column this now matches SQL exactly, since NULLs are left alone; what still blocks it is that " +
				"the compiler has no column types, and on a non-text column Go renders the value with %v where SQL needs an " +
				"explicit cast whose formatting has to be shown to agree. This is the next rule to push down, once types are threaded through.",
		},
		{
			name:         "replace_values",
			rules:        []TransformRule{{Type: "replace_values", Column: "city", Mapping: map[string]string{"porto": "Porto"}}},
			wantCompiled: false,
			why:          "Go replaces on the stringified value and leaves the column a Go string; the SQL form has to agree on what a non-text column becomes",
		},
		{
			name:         "add_column",
			rules:        []TransformRule{{Type: "add_column", Name: "double", Expression: "amount * 2"}},
			wantCompiled: false,
			why:          "needs the expression evaluator from #213; there is no shared AST to compile to SQL yet",
		},
	}

	extra := []struct {
		name         string
		rules        []TransformRule
		wantCompiled bool
		why          string
	}{
		{"filter inequality on text", []TransformRule{{Type: "filter_rows", Condition: "name != alice"}}, true, ""},
		{"filter numeric >=", []TransformRule{{Type: "filter_rows", Condition: "amount >= 100"}}, true, ""},
		{"filter numeric <", []TransformRule{{Type: "filter_rows", Condition: "amount < 10"}}, true, ""},
		{"filter text ordered", []TransformRule{{Type: "filter_rows", Condition: "city > Faro"}}, true, ""},
		{"filter equality on numeric column", []TransformRule{{Type: "filter_rows", Condition: "amount = 100"}}, true, ""},
		{"filter after a rename follows the new name", []TransformRule{
			{Type: "rename_columns", Mapping: map[string]string{"amount": "value"}},
			{Type: "filter_rows", Condition: "value > 99"},
		}, true, ""},
		{"filter on a boolean column is refused", []TransformRule{{Type: "filter_rows", Condition: "flag = true"}}, false,
			"boolean is unclassified: Go renders it \"true\" and SQL would need a cast whose spelling has to be shown to agree"},
		{"filter with in is refused", []TransformRule{{Type: "filter_rows", Condition: "city in [Lisbon, Faro]"}}, false,
			"set membership compares rendered text; the rendering of a non-text column has not been demonstrated for every member"},
	}
	cases = append(cases, extra...)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, ok := planTransformRules(tc.rules)
			if !ok {
				t.Fatalf("planTransformRules refused %v; this test assumes a row-wise prefix", tc.rules)
			}

			srcCols := []string{"id", "name", "city", "amount", "note", "flag"}
			kinds, err := describeQueryColumns(context.Background(), uri, srcQuery, pgDialect(t))
			if err != nil {
				t.Fatalf("describe columns: %v", err)
			}
			compiled, gotCompiled := compilePrefixToSQLTyped(plan.prefix, srcQuery, srcCols, "postgres", kinds)

			if gotCompiled != tc.wantCompiled {
				if tc.wantCompiled {
					t.Fatalf("compiler declined a rule this test expects it to push down")
				}
				t.Fatalf("compiler pushed down a rule that is not proven equivalent (%s). "+
					"Adding a rule here requires a passing comparison, not just a compiler branch.", tc.why)
			}
			if !tc.wantCompiled {
				t.Logf("not pushed down, by design: %s", tc.why)
				return
			}

			// Backend A: the Go executor over the materialised source.
			goDS, err := QueryDatabase(uri, srcQuery)
			if err != nil {
				t.Fatal(err)
			}
			if err := ApplyTransforms(tc.rules, goDS); err != nil {
				t.Fatalf("go executor: %v", err)
			}

			// Backend B: the generated SQL.
			sqlDS, err := QueryDatabase(uri, compiled.Query)
			if err != nil {
				t.Fatalf("generated SQL failed: %v\n%s", err, compiled.Query)
			}

			if !reflect.DeepEqual(compiled.Columns, goDS.Columns) {
				t.Errorf("column order differs:\n  sql %v\n  go  %v", compiled.Columns, goDS.Columns)
			}
			gotRows := normalize(compiled.Columns, sqlDS.Rows)
			wantRows := normalize(goDS.Columns, goDS.Rows)
			if len(gotRows) != len(wantRows) {
				t.Fatalf("row count differs: sql %d, go %d\n%s", len(gotRows), len(wantRows), compiled.Query)
			}
			for i := range wantRows {
				if gotRows[i] != wantRows[i] {
					t.Errorf("row %d differs:\n  sql %s\n  go  %s\n\n%s", i, gotRows[i], wantRows[i], compiled.Query)
				}
			}
		})
	}
}

// A column name is not a safe SQL fragment. The compiler quotes identifiers,
// and this is the check that it keeps doing so.
func TestPrefixSQLQuotesIdentifiers(t *testing.T) {
	compiled, ok := compilePrefixToSQL(
		[]TransformRule{{Type: "rename_columns", Mapping: map[string]string{`we"ird`: "safe"}}},
		"SELECT 1", []string{`we"ird`, "keep"}, "postgres")
	if !ok {
		t.Fatal("expected a rename to compile")
	}
	if !strings.Contains(compiled.Query, `"we""ird" AS "safe"`) {
		t.Errorf("identifier not escaped: %s", compiled.Query)
	}
}

// The refusals above are only as good as the reasons attached to them, and a
// reason written in a comment is a claim, not a fact. This runs each refused
// rule against the SQL a naive compiler would have emitted and requires the
// two to actually disagree.
//
// It is deliberately the strict direction: if one of these ever stops
// diverging, the test fails and says so, because that rule is then leaving
// pushdown on the table for no reason. Either the rule gets compiled or the
// reason gets rewritten -- what it cannot do is sit there unexamined.
//
// That has already happened once. apply_function was on this list because
// upper() turned a NULL into the string "<NIL>"; fixing that made the two
// backends agree on a text column, this test failed, and the refusal reason
// was rewritten to the thing that actually blocks it now (no column types).
// TestApplyFunctionOnTextNowMatchesSQL records the agreement it reached.
func TestRefusedRulesReallyDiverge(t *testing.T) {
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

	const src = `SELECT id, name, city, amount, note, flag FROM ` + diffSrcTable

	cases := []struct {
		name     string
		rules    []TransformRule
		naiveSQL string
	}{
		{
			name:     "filter numeric drops NULL rows only in SQL",
			rules:    []TransformRule{{Type: "filter_rows", Condition: "amount > 99"}},
			naiveSQL: `SELECT id, name, city, amount, note, flag FROM ` + diffSrcTable + ` WHERE amount > 99`,
		},
		{
			name:     "filter on numeric-looking text orders differently",
			rules:    []TransformRule{{Type: "filter_rows", Condition: "city > 9"}},
			naiveSQL: `SELECT id, name, city, amount, note, flag FROM ` + diffSrcTable + ` WHERE city > '9'`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			goDS, err := QueryDatabase(uri, src)
			if err != nil {
				t.Fatal(err)
			}
			if err := ApplyTransforms(tc.rules, goDS); err != nil {
				t.Fatalf("go executor: %v", err)
			}
			sqlDS, err := QueryDatabase(uri, tc.naiveSQL)
			if err != nil {
				t.Fatalf("naive SQL failed: %v\n%s", err, tc.naiveSQL)
			}

			gotGo := normalize(goDS.Columns, goDS.Rows)
			gotSQL := normalize(goDS.Columns, sqlDS.Rows)

			if reflect.DeepEqual(gotGo, gotSQL) {
				t.Fatalf("this rule no longer diverges from naive SQL, so the refusal in "+
					"compilePrefixToSQL needs revisiting: either compile it or rewrite the reason.\n"+
					"  rows: %d\n  sql: %s", len(gotGo), tc.naiveSQL)
			}
			t.Logf("diverges as documented: go %d row(s), sql %d row(s)", len(gotGo), len(gotSQL))
			if len(gotGo) == len(gotSQL) {
				for i := range gotGo {
					if gotGo[i] != gotSQL[i] {
						t.Logf("  first differing row:\n    go  %s\n    sql %s", gotGo[i], gotSQL[i])
						break
					}
				}
			}
		})
	}
}

// apply_function rendered its input with %v before transforming it, so a NULL
// became the literal string "<NIL>" (upper) or "<nil>" (lower/trim/title) and
// travelled downstream as real data. Found by comparing the Go executor with
// SQL, where UPPER(NULL) is NULL.
func TestApplyFunctionLeavesNullAlone(t *testing.T) {
	for _, fn := range []string{"upper", "lower", "trim", "title"} {
		t.Run(fn, func(t *testing.T) {
			ds := &common.DataSet{
				Columns: []string{"name"},
				Rows: []common.DataRow{
					{"name": nil},
					{"name": "alice"},
				},
			}
			if err := ApplyTransforms([]TransformRule{{Type: "apply_function", Column: "name", Function: fn}}, ds); err != nil {
				t.Fatal(err)
			}
			if got := ds.Rows[0]["name"]; got != nil {
				t.Errorf("%s turned a NULL into %#v", fn, got)
			}
			if got := ds.Rows[1]["name"]; got == nil {
				t.Errorf("%s dropped a real value", fn)
			}
		})
	}
}

// The counterpart to the divergence test: apply_function on a text column now
// produces exactly what SQL does. It is not pushed down yet only because the
// compiler cannot tell a text column from a numeric one -- this pins the
// semantic half so that when types arrive, the remaining question is narrow.
func TestApplyFunctionOnTextNowMatchesSQL(t *testing.T) {
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

	const src = `SELECT id, name, city, amount, note, flag FROM ` + diffSrcTable
	for _, fn := range []struct{ rule, sqlFn string }{
		{"upper", "UPPER"}, {"lower", "LOWER"}, {"trim", "BTRIM"},
	} {
		t.Run(fn.rule, func(t *testing.T) {
			goDS, err := QueryDatabase(uri, src)
			if err != nil {
				t.Fatal(err)
			}
			if err := ApplyTransforms([]TransformRule{{Type: "apply_function", Column: "name", Function: fn.rule}}, goDS); err != nil {
				t.Fatal(err)
			}
			sqlDS, err := QueryDatabase(uri,
				`SELECT id, `+fn.sqlFn+`(name) AS name, city, amount, note, flag FROM `+diffSrcTable)
			if err != nil {
				t.Fatal(err)
			}
			gotGo := normalize(goDS.Columns, goDS.Rows)
			gotSQL := normalize(goDS.Columns, sqlDS.Rows)
			if !reflect.DeepEqual(gotGo, gotSQL) {
				t.Errorf("%s still differs from SQL on a text column:\n  go  %v\n  sql %v", fn.rule, gotGo, gotSQL)
			}
		})
	}
}
