package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// A sink_file node asking for SQL used to get JSON: the "sql" case only
// handled a sql_generate node's single sql_output cell, and any other
// dataset fell through to the JSON marshaller. The log then reported
// "sql", so the file's name, its contents and the log all disagreed
// (#545).
//
// These run whole pipelines rather than calling runSinkFile directly,
// because the handler logs through the runner's store and a hand-built
// Runner has neither.

// runSQLSinkPipeline writes a small CSV, runs source_file -> sink_file
// with format "sql", and returns what landed in the output file.
func runSQLSinkPipeline(t *testing.T, outName string, sinkCfg map[string]interface{}) string {
	t.Helper()
	dir := t.TempDir()

	src := filepath.Join(dir, "in.csv")
	if err := os.WriteFile(src, []byte("id,name,active\n1,Ada,true\n2,O'Brien,false\n"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	out := filepath.Join(dir, outName)

	st, err := store.NewSQLiteStore(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	eng := drainEngineOnCleanup(t, NewEngine(st))

	cfg := map[string]interface{}{"path": out, "format": "sql"}
	for k, v := range sinkCfg {
		cfg[k] = v
	}

	id := fmt.Sprintf("sqlsink-%d", time.Now().UnixNano())
	p := &models.Pipeline{
		ID: id, Name: id, Enabled: true,
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceFile, Name: "S",
				Config: map[string]interface{}{"path": src, "format": "csv"}},
			{ID: "out", Type: models.NodeTypeSinkFile, Name: "O", Config: cfg},
		},
		Edges:     []models.Edge{{From: "src", To: "out"}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := st.CreatePipeline(p); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
	run, err := eng.RunPipeline(p.ID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %s, error = %s", run.Status, run.Error)
	}

	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	return string(b)
}

func TestSinkFileSQLWritesStatementsNotJSON(t *testing.T) {
	out := runSQLSinkPipeline(t, "customers.sql", map[string]interface{}{"table": "customers"})

	if strings.HasPrefix(strings.TrimSpace(out), "[") {
		t.Fatalf("sink_file wrote JSON for format=sql:\n%s", out)
	}
	if !strings.Contains(strings.ToUpper(out), "INSERT INTO") {
		t.Fatalf("no INSERT statement in the output:\n%s", out)
	}
	if !strings.Contains(out, "customers") {
		t.Fatalf("the configured table name is missing:\n%s", out)
	}
}

// A value containing a quote must survive as data rather than ending the
// literal. This is the case a hand-rolled escaper gets wrong, and the
// reason this goes through GenerateSQL and pkg/dbdialect instead.
func TestSinkFileSQLEscapesValues(t *testing.T) {
	out := runSQLSinkPipeline(t, "customers.sql",
		map[string]interface{}{"table": "customers", "dialect": "postgres"})

	if !strings.Contains(out, "O''Brien") {
		t.Fatalf("a single quote in a value was not doubled for postgres:\n%s", out)
	}
}

func TestSinkFileSQLHonoursCreateTable(t *testing.T) {
	with := runSQLSinkPipeline(t, "customers.sql",
		map[string]interface{}{"table": "customers", "create_table": true})
	if !strings.Contains(strings.ToUpper(with), "CREATE TABLE") {
		t.Fatalf("create_table was requested but no DDL was emitted:\n%s", with)
	}

	without := runSQLSinkPipeline(t, "customers.sql", map[string]interface{}{"table": "customers"})
	if strings.Contains(strings.ToUpper(without), "CREATE TABLE") {
		t.Fatalf("DDL was emitted without create_table being asked for:\n%s", without)
	}
}

// The dialect has to reach the generator, or every script comes out
// quoted for whatever the default happens to be.
func TestSinkFileSQLHonoursDialect(t *testing.T) {
	pg := runSQLSinkPipeline(t, "customers.sql",
		map[string]interface{}{"table": "customers", "dialect": "postgres", "create_table": true})
	my := runSQLSinkPipeline(t, "customers.sql",
		map[string]interface{}{"table": "customers", "dialect": "mysql", "create_table": true})

	if pg == my {
		t.Fatal("postgres and mysql produced byte-identical scripts; the dialect is not reaching GenerateSQL")
	}
	if !strings.Contains(pg, `"customers"`) {
		t.Errorf("postgres output does not quote the identifier with double quotes:\n%s", pg)
	}
	if !strings.Contains(my, "`customers`") {
		t.Errorf("mysql output does not quote the identifier with backticks:\n%s", my)
	}
}

// An unset table falls back to the output file's stem, so writing
// customers.sql produces a customers table rather than one called "data".
func TestSinkFileSQLDerivesTableFromFilename(t *testing.T) {
	out := runSQLSinkPipeline(t, "customers.sql", map[string]interface{}{})
	if !strings.Contains(out, "customers") {
		t.Fatalf("the table name was not derived from customers.sql:\n%s", out)
	}
}

func TestTableNameFromPath(t *testing.T) {
	cases := map[string]string{
		"/tmp/customers.sql":     "customers",
		"/tmp/daily-revenue.sql": "daily_revenue",
		"/tmp/orders 2026.sql":   "orders_2026",
		"/tmp/_leading.sql":      "leading",
		"/tmp/mixed.Case-1.sql":  "mixed_Case_1",
		"/tmp/2026.sql":          "", // an identifier cannot lead with a digit
		"/tmp/.sql":              "", // no stem at all
		"/tmp/!!!.sql":           "", // nothing usable survives
	}
	for path, want := range cases {
		if got := tableNameFromPath(path); got != want {
			t.Errorf("tableNameFromPath(%q) = %q, want %q", path, got, want)
		}
	}
}
