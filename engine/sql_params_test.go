package engine

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

func paramResolver(values map[string]string) func(string) string {
	vc := NewVariableContext(values, "run-1", time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC))
	return vc.resolveKey
}

func TestBindSQLParams(t *testing.T) {
	resolve := paramResolver(map[string]string{
		"name": "x' OR '1'='1", "n": "42", "min": "1.50", "region": "eu west",
	})
	pg := sqlParamDialect{style: placeholderDollar}
	cases := []struct {
		name  string
		d     sqlParamDialect
		query string
		want  string
		args  []interface{}
	}{
		{"no references", pg, "SELECT 1", "SELECT 1", nil},
		{"whole literal binds as text", pg,
			"SELECT * FROM t WHERE name = '${param.name}'",
			"SELECT * FROM t WHERE name = $1", []interface{}{"x' OR '1'='1"}},
		{"bare whole number binds as an integer", pg,
			"SELECT * FROM t LIMIT ${param.n}", "SELECT * FROM t LIMIT $1", []interface{}{int64(42)}},
		{"bare decimal binds as exact text", pg,
			"SELECT * FROM t WHERE amount > ${param.min}", "SELECT * FROM t WHERE amount > $1", []interface{}{"1.50"}},
		{"placeholders number in order, repeats bind again", pg,
			"SELECT '${param.region}', ${param.n}, '${param.region}'", "SELECT $1, $2, $3",
			[]interface{}{"eu west", int64(42), "eu west"}},
		{"question style", sqlParamDialect{style: placeholderQuestion},
			"WHERE a = '${param.region}' AND b < ${param.n}", "WHERE a = ? AND b < ?", []interface{}{"eu west", int64(42)}},
		{"sql server style, N prefix dropped", sqlParamDialect{style: placeholderAtP},
			"WHERE a = N'${param.region}'", "WHERE a = @p1", []interface{}{"eu west"}},
		{"oracle style", sqlParamDialect{style: placeholderColon},
			"WHERE a = '${param.region}'", "WHERE a = :1", []interface{}{"eu west"}},
		{"postgres E prefix dropped, a word ending in e is not", pg,
			"SELECT E'${param.region}', name FROM t WHERE type='${param.region}'",
			"SELECT $1, name FROM t WHERE type=$2", []interface{}{"eu west", "eu west"}},
		{"double-quoted string where the dialect has them", sqlParamDialect{style: placeholderQuestion, doubleQuotedStrings: true},
			`WHERE a = "${param.region}"`, "WHERE a = ?", []interface{}{"eu west"}},
		{"comments keep their references", pg,
			"SELECT 1 -- ${param.name}\n/* ${param.name} */ WHERE a = '${param.region}'",
			"SELECT 1 -- ${param.name}\n/* ${param.name} */ WHERE a = $1", []interface{}{"eu west"}},
		{"other literals and doubled quotes pass through", pg,
			"SELECT 'it''s', '${x}', a FROM t WHERE b = '${param.region}'",
			"SELECT 'it''s', '${x}', a FROM t WHERE b = $1", []interface{}{"eu west"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, args, err := bindSQLParams(tc.query, tc.d, resolve)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want || !reflect.DeepEqual(args, tc.args) {
				t.Fatalf("got %q %#v\nwant %q %#v", got, args, tc.want, tc.args)
			}
		})
	}
}

// Every position a value cannot be bound in is refused, with the value
// never written into the SQL.
func TestBindSQLParamsRefusesUnbindablePositions(t *testing.T) {
	resolve := paramResolver(map[string]string{"name": "orders; DROP TABLE t", "n": "1 OR 1=1", "big": "99999999999999999999"})
	pg := sqlParamDialect{style: placeholderDollar}
	for _, tc := range []struct {
		name, query, want string
		d                 sqlParamDialect
	}{
		{"bare non-number", "SELECT * FROM t WHERE id = ${param.n}", "quote it to bind it as text: '${param.n}'", pg},
		{"bare name", "SELECT * FROM ${param.name}", "not a number", pg},
		{"inside a longer literal", "WHERE a LIKE '%${param.name}%'", "inside a longer string literal", pg},
		{"inside a quoted name", `SELECT * FROM "${param.name}"`, "inside as a quoted name", pg},
		{"inside a backtick name", "SELECT * FROM `${param.name}`", "inside as a quoted name", sqlParamDialect{style: placeholderQuestion}},
		{"inside a longer double-quoted string", `WHERE a = "x${param.name}"`, "inside a longer string literal", sqlParamDialect{doubleQuotedStrings: true}},
		{"whole number out of range", "LIMIT ${param.big}", "out of range", pg},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, args, err := bindSQLParams(tc.query, tc.d, resolve)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %q %v, err %v; want an error containing %q", got, args, err, tc.want)
			}
		})
	}
}

func TestSQLParamPositionsAreCheckedAtValidation(t *testing.T) {
	if errs := sqlParamPositionErrors("SELECT * FROM t WHERE a LIKE '%${param.x}%'"); len(errs) != 1 {
		t.Fatalf("errors = %v, want the partial literal refused", errs)
	}
	for _, ok := range []string{"SELECT 1", "WHERE a = '${param.x}' AND b < ${param.n}", "SELECT '${var.x}%'"} {
		if errs := sqlParamPositionErrors(ok); len(errs) != 0 {
			t.Errorf("%q refused at validation: %v", ok, errs)
		}
	}
	pipe := policyTestPipeline("sql-partial", models.Node{ID: "db", Type: models.NodeTypeSourceDB, Name: "DB",
		Config: map[string]interface{}{"uri": "postgres://h/db", "query": "SELECT * FROM t WHERE a LIKE '%${param.x}%'"}})
	if ve := ValidatePipeline(pipe); !strings.Contains(ve.Error(), "inside a longer string literal") {
		t.Fatalf("validation = %v", ve)
	}
}

// The resolver leaves source and migrate SQL's parameters for binding, and
// still substitutes everything else.
func TestSourceSQLKeepsParametersForBinding(t *testing.T) {
	vc := NewVariableContext(map[string]string{"x": "v"}, "run-9", time.Now())
	for _, tc := range []struct {
		node models.Node
		key  string
	}{
		{models.Node{Type: models.NodeTypeSourceDB, Config: map[string]interface{}{"query": "SELECT '${param.x}', '${run.id}'", "label": "${param.x}"}}, "query"},
		{models.Node{Type: models.NodeTypeMigrate, Config: map[string]interface{}{"source_query": "SELECT '${param.x}', '${run.id}'", "label": "${param.x}"}}, "source_query"},
	} {
		got := resolveNodeConfig(vc, tc.node)
		if got[tc.key] != "SELECT '${param.x}', 'run-9'" {
			t.Errorf("%s %s = %q", tc.node.Type, tc.key, got[tc.key])
		}
		if got["label"] != "v" {
			t.Errorf("%s: other fields must still substitute, label = %q", tc.node.Type, got["label"])
		}
	}
}

// sqlParamDatabase is one real database the binding is proven against.
type sqlParamDatabase struct {
	name, uri string
	ddl       func(table string) string
}

func sqlParamDatabases(t *testing.T) []sqlParamDatabase {
	generic := func(table string) string {
		return "CREATE TABLE " + table + " (id INTEGER, name VARCHAR(64), amount DECIMAL(10,2))"
	}
	dbs := []sqlParamDatabase{{name: "sqlite", uri: "sqlite://" + filepath.Join(t.TempDir(), "params.db"), ddl: generic}}
	if uri := os.Getenv("BROKOLI_TEST_POSTGRES_URL"); uri != "" {
		dbs = append(dbs, sqlParamDatabase{"postgres", uri, generic})
	}
	if uri := os.Getenv("BROKOLI_TEST_MYSQL_URL"); uri != "" {
		dbs = append(dbs, sqlParamDatabase{"mysql", uri, generic})
	}
	if uri := os.Getenv("BROKOLI_TEST_SQLSERVER_URL"); uri != "" {
		dbs = append(dbs, sqlParamDatabase{"sqlserver", uri, func(table string) string {
			return "CREATE TABLE " + table + " (id INT, name NVARCHAR(64), amount DECIMAL(10,2))"
		}})
	}
	if uri := os.Getenv("BROKOLI_TEST_CLICKHOUSE_URL"); uri != "" {
		dbs = append(dbs, sqlParamDatabase{"clickhouse", uri, func(table string) string {
			return "CREATE TABLE " + table + " (id Int64, name String, amount Decimal(10,2)) ENGINE = Memory"
		}})
	}
	return dbs
}

// Against each real database: a hostile value bound as text matches
// nothing (it is compared, not run), an honest one with a quote in it
// matches its row, and bare numbers compare as numbers.
func TestSQLParamsBindAgainstDatabases(t *testing.T) {
	for _, db := range sqlParamDatabases(t) {
		t.Run(db.name, func(t *testing.T) { proveSQLParamBinding(t, db) })
	}
}

func TestOracleBindsRunParameters(t *testing.T) {
	uri := os.Getenv("BROKOLI_TEST_ORACLE_URL")
	if uri == "" {
		t.Skip("set BROKOLI_TEST_ORACLE_URL to run the Oracle tests")
	}
	proveSQLParamBinding(t, sqlParamDatabase{"oracle", uri, func(table string) string {
		return "CREATE TABLE " + table + " (id NUMBER(10), name VARCHAR2(64), amount NUMBER(10,2))"
	}})
}

func proveSQLParamBinding(t *testing.T, db sqlParamDatabase) {
	t.Helper()
	driver, dsn, err := DetectDriver(db.uri)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := sql.Open(driver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	table := fmt.Sprintf("brokoli_params_%d", time.Now().UnixNano()%1_000_000_000)
	if _, err := conn.Exec(db.ddl(table)); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _, _ = conn.Exec("DROP TABLE " + table) })
	for _, row := range []string{"(1, 'alpha', 1.25)", "(2, 'O''Brien', 2.50)", "(3, 'gamma', 3.75)"} {
		if _, err := conn.Exec("INSERT INTO " + table + " (id, name, amount) VALUES " + row); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	d := sqlParamDialectFor(db.uri)
	count := func(query string, params map[string]string) int {
		t.Helper()
		bound, args, err := bindSQLParams(query, d, paramResolver(params))
		if err != nil {
			t.Fatalf("bind %q: %v", query, err)
		}
		ds, err := QueryDatabase(db.uri, bound, args...)
		if err != nil {
			t.Fatalf("query %q (%v): %v", bound, args, err)
		}
		return len(ds.Rows)
	}
	byName := "SELECT id FROM " + table + " WHERE name = '${param.name}'"
	for _, hostile := range []string{"x' OR '1'='1", "x' OR 1=1 --", `x\' OR 1=1 #`, "alpha'; DROP TABLE " + table + "; --"} {
		if n := count(byName, map[string]string{"name": hostile}); n != 0 {
			t.Fatalf("hostile value %q matched %d rows: it was run, not compared", hostile, n)
		}
	}
	if n := count(byName, map[string]string{"name": "O'Brien"}); n != 1 {
		t.Fatalf("a quote in an honest value: %d rows, want 1", n)
	}
	if n := count("SELECT id FROM "+table+" WHERE id <= ${param.n}", map[string]string{"n": "2"}); n != 2 {
		t.Fatalf("bare whole number: %d rows, want 2", n)
	}
	if n := count("SELECT id FROM "+table+" WHERE amount > ${param.min}", map[string]string{"min": "2.00"}); n != 2 {
		t.Fatalf("bare decimal: %d rows, want 2", n)
	}
	// The table survived every hostile value.
	if n := count("SELECT id FROM "+table, nil); n != 3 {
		t.Fatalf("table has %d rows after the hostile values, want 3", n)
	}
}

// Through a real run: the run parameter reaches a source_db query bound,
// so a hostile value selects nothing instead of everything. Both execution
// paths: the source streams its result by default, and reads it whole when
// streaming is off (a negative threshold; zero means the default).
func TestSourceDBBindsRunParametersInARun(t *testing.T) {
	for _, mode := range []struct{ name, threshold string }{{"streamed", ""}, {"batch", "-1"}} {
		t.Run(mode.name, func(t *testing.T) {
			if mode.threshold != "" {
				t.Setenv("BROKOLI_STREAM_THRESHOLD_BYTES", mode.threshold)
			}
			sourceDBBindsRunParameters(t)
		})
	}
}

func sourceDBBindsRunParameters(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BROKOLI_DATA_DIRS", dir)
	dbPath := filepath.Join(dir, "src.db")
	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		"CREATE TABLE people (id INTEGER, name TEXT)",
		"INSERT INTO people VALUES (1, 'alpha'), (2, 'beta')",
	} {
		if _, err := conn.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	conn.Close()

	st, err := store.NewSQLiteStore(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	eng := drainEngineOnCleanup(t, NewEngine(st))
	out := filepath.Join(dir, "out.csv")
	p := &models.Pipeline{
		ID: "bind-run", Name: "bind-run", Enabled: true,
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceDB, Name: "Source", Config: map[string]interface{}{
				"uri": "sqlite://" + dbPath, "query": "SELECT id, name FROM people WHERE name = '${param.name}'"}},
			{ID: "sink", Type: models.NodeTypeSinkFile, Name: "Sink", Config: map[string]interface{}{"path": out, "format": "csv"}},
		},
		Edges:     []models.Edge{{From: "src", To: "sink"}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := st.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	lines := func(params map[string]string) int {
		t.Helper()
		run, err := eng.RunPipeline("bind-run", params)
		if err != nil || run.Status != models.RunStatusSuccess {
			t.Fatalf("run: %v %+v", err, run)
		}
		data, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}

		return len(strings.Split(strings.TrimSpace(string(data)), "\n"))
	}
	if n := lines(map[string]string{"name": "alpha"}); n != 2 {
		t.Fatalf("honest value: %d lines, want header + 1", n)
	}
	if n := lines(map[string]string{"name": "x' OR '1'='1"}); n > 1 {
		t.Fatalf("hostile value: %d lines; the parameter was spliced into the SQL", n)
	}
}

// A source query with run parameters is not pushed down: a pushed-down
// write embeds the query in SQL that carries no bindings. The segment runs
// in the engine, with the parameter bound, and writes the right rows.
func TestParameterisedSourceIsNotPushedDown(t *testing.T) {
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
		`DROP TABLE IF EXISTS brokoli_pp_src`, `DROP TABLE IF EXISTS brokoli_pp_dst`,
		`CREATE TABLE brokoli_pp_src (id bigint, city text)`,
		`INSERT INTO brokoli_pp_src VALUES (1, 'lisbon'), (2, 'porto'), (3, 'lisbon')`,
		`CREATE TABLE brokoli_pp_dst (id bigint, city text)`,
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP TABLE IF EXISTS brokoli_pp_src`)
		_, _ = db.Exec(`DROP TABLE IF EXISTS brokoli_pp_dst`)
	})
	t.Setenv("BROKOLI_DATA_PLANE", "")

	eng, st := newExecCtxTestEngine(t)
	eng.ArtifactStore = NewLocalDiskArtifactStore(filepath.Join(t.TempDir(), "artifacts"))
	p := &models.Pipeline{ID: "p-pp", Name: "p-pp", Enabled: true,
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceDB, Name: "Src", Config: map[string]interface{}{
				"uri": uri, "query": "SELECT id, city FROM brokoli_pp_src WHERE city = '${param.city}'"}},
			{ID: "dst", Type: models.NodeTypeSinkDB, Name: "Dst", Config: map[string]interface{}{
				"uri": uri, "table": "brokoli_pp_dst", "mode": "append"}},
		},
		Edges: []models.Edge{{From: "src", To: "dst"}},
	}
	if err := st.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	run, err := eng.RunPipeline(p.ID, map[string]string{"city": "lisbon"})
	if err != nil || run.Status != models.RunStatusSuccess {
		t.Fatalf("run: %v %+v", err, run)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM brokoli_pp_dst WHERE city = 'lisbon'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	var total int
	_ = db.QueryRow(`SELECT count(*) FROM brokoli_pp_dst`).Scan(&total)
	if n != 2 || total != 2 {
		t.Fatalf("destination has %d lisbon rows of %d, want 2 of 2", n, total)
	}
}

// migrate binds its source_query's run parameters the same way.
func TestMigrateBindsRunParameters(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BROKOLI_DATA_DIRS", dir)
	src := filepath.Join(dir, "src.db")
	dst := filepath.Join(dir, "dst.db")
	conn, err := sql.Open("sqlite", src)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{"CREATE TABLE people (id INTEGER, name TEXT)", "INSERT INTO people VALUES (1, 'alpha'), (2, 'beta')"} {
		if _, err := conn.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	conn.Close()

	st, err := store.NewSQLiteStore(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	eng := drainEngineOnCleanup(t, NewEngine(st))
	p := &models.Pipeline{
		ID: "bind-migrate", Name: "bind-migrate", Enabled: true,
		Nodes: []models.Node{{ID: "mig", Type: models.NodeTypeMigrate, Name: "Migrate", Config: map[string]interface{}{
			"source_uri": "sqlite://" + src, "source_query": "SELECT id, name FROM people WHERE name = '${param.name}'",
			"dest_uri": "sqlite://" + dst, "dest_table": "copied", "create_table": true}}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := st.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	copied := func(params map[string]string) int {
		t.Helper()
		run, err := eng.RunPipeline("bind-migrate", params)
		if err != nil || run.Status != models.RunStatusSuccess {
			t.Fatalf("run: %v %+v", err, run)
		}
		out, err := sql.Open("sqlite", dst)
		if err != nil {
			t.Fatal(err)
		}
		defer out.Close()
		var n int
		if err := out.QueryRow("SELECT count(*) FROM copied").Scan(&n); err != nil {
			t.Fatal(err)
		}
		_, _ = out.Exec("DELETE FROM copied")
		return n
	}
	if n := copied(map[string]string{"name": "beta"}); n != 1 {
		t.Fatalf("honest value copied %d rows, want 1", n)
	}
	if n := copied(map[string]string{"name": "x' OR '1'='1"}); n != 0 {
		t.Fatalf("hostile value copied %d rows; the parameter was spliced into the SQL", n)
	}
}
