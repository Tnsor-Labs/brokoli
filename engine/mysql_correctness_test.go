package engine

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/store"
)

// GenerateSQL's contract says overwrite's clear-then-insert runs as one
// transaction. On MySQL, TRUNCATE TABLE implicitly commits, so a generator
// that emitted it would destroy the previous rows the moment the statement
// ran -- a later failure would roll back nothing. This demonstrates that
// behaviour against a live server, so the DELETE degradation in clearTable
// is pinned to the reason it exists rather than to an assertion about text.
func TestMySQLTruncateWouldBreakAtomicity(t *testing.T) {
	uri := mysqlTestURI(t)
	db := openMySQLForTest(t, uri)

	mustExec := func(q string) {
		t.Helper()
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	count := func(table string) int {
		t.Helper()
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	for _, tbl := range []string{"atomicity_truncate", "atomicity_delete"} {
		mustExec("DROP TABLE IF EXISTS " + tbl)
		mustExec("CREATE TABLE " + tbl + " (id INT PRIMARY KEY)")
		mustExec("INSERT INTO " + tbl + " VALUES (1), (2), (3)")
		t.Cleanup(func() { db.Exec("DROP TABLE IF EXISTS " + tbl) })
	}

	// The defect mechanism: clear with TRUNCATE, then roll back. The
	// implicit commit means the rollback arrives too late.
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("TRUNCATE TABLE atomicity_truncate"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got := count("atomicity_truncate"); got != 0 {
		t.Fatalf("expected TRUNCATE to survive rollback on MySQL (the defect this pins); %d rows remain", got)
	}

	// The statement the generator emits instead: clear with DELETE, roll
	// back, and the previous rows are still there.
	tx, err = db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("DELETE FROM atomicity_delete"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got := count("atomicity_delete"); got != 3 {
		t.Fatalf("DELETE must roll back on MySQL: want 3 rows intact, got %d", got)
	}

	// And the generator no longer emits the statement that cannot roll
	// back, even when truncate is requested.
	sql, err := GenerateSQL(SQLGenConfig{
		Dialect: "mysql", Table: "atomicity_delete", Mode: ModeOverwrite, Truncate: true,
	}, &common.DataSet{Columns: []string{"id"}, Rows: []common.DataRow{{"id": 9}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sql, "TRUNCATE") {
		t.Fatalf("mysql overwrite must not emit TRUNCATE:\n%s", sql)
	}
}

// The alias-form upsert must actually merge on a live server: the offline
// test asserts the text, this asserts the semantics -- including that the
// hostile-value corpus survives the update path, not only the insert path.
func TestMySQLUpsertMergesOnLiveServer(t *testing.T) {
	uri := mysqlTestURI(t)
	db := openMySQLForTest(t, uri)

	if _, err := db.Exec("DROP TABLE IF EXISTS upsert_live"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE upsert_live (id INT PRIMARY KEY, v TEXT)"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec("DROP TABLE IF EXISTS upsert_live") })

	write := func(rows []common.DataRow) {
		t.Helper()
		sql, err := GenerateSQL(SQLGenConfig{
			Dialect: "mysql", Table: "upsert_live", Mode: ModeUpsert, KeyColumns: []string{"id"},
		}, &common.DataSet{Columns: []string{"id", "v"}, Rows: rows})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ExecuteSQL(uri, sql); err != nil {
			t.Fatalf("execute upsert: %v", err)
		}
	}

	write([]common.DataRow{{"id": 1, "v": "first"}, {"id": 2, "v": "second"}})
	for _, hv := range hostileValues() {
		write([]common.DataRow{{"id": 1, "v": hv.value}, {"id": 3, "v": "insert-" + hv.name}})

		var got string
		if err := db.QueryRow("SELECT v FROM upsert_live WHERE id = 1").Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != hv.value {
			t.Errorf("%s: updated value corrupted: got %q, want %q", hv.name, got, hv.value)
		}
	}
	var untouched string
	if err := db.QueryRow("SELECT v FROM upsert_live WHERE id = 2").Scan(&untouched); err != nil {
		t.Fatal(err)
	}
	if untouched != "second" {
		t.Errorf("row not addressed by the upsert changed: %q", untouched)
	}
}

// key_columns is an assertion about the table, checked before execution.
// A configured key that is not a unique index would merge on whatever index
// the schema happens to have, rewriting rows the user never addressed --
// so it refuses, naming what the table actually offers.
func TestMySQLUpsertKeyValidation(t *testing.T) {
	uri := mysqlTestURI(t)
	db := openMySQLForTest(t, uri)
	ctx := context.Background()

	if _, err := db.Exec("DROP TABLE IF EXISTS upsert_keys"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE upsert_keys (
		id INT PRIMARY KEY,
		email VARCHAR(100),
		a INT, b INT,
		note TEXT,
		UNIQUE KEY uq_email (email),
		UNIQUE KEY uq_ab (a, b)
	)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec("DROP TABLE IF EXISTS upsert_keys") })

	// The primary key matches; the other unique indexes come back as the
	// warning list, because a row can still collide on them.
	others, err := validateMySQLUpsertKey(ctx, uri, "upsert_keys", []string{"id"})
	if err != nil {
		t.Fatalf("primary key should validate: %v", err)
	}
	if len(others) != 2 {
		t.Errorf("expected the two other unique indexes surfaced, got %v", others)
	}

	// Order and case do not identify an index, only the column set does.
	if _, err := validateMySQLUpsertKey(ctx, uri, "upsert_keys", []string{"B", "a"}); err != nil {
		t.Errorf("composite key in another order and case should validate: %v", err)
	}

	// A column that is not a unique index refuses, naming the real ones.
	_, err = validateMySQLUpsertKey(ctx, uri, "upsert_keys", []string{"note"})
	if err == nil {
		t.Fatal("a non-unique key column must refuse")
	}
	for _, name := range []string{"PRIMARY", "uq_email", "uq_ab"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("refusal should name the table's unique indexes; %q missing from: %v", name, err)
		}
	}

	// A subset of a composite index is not that index.
	if _, err := validateMySQLUpsertKey(ctx, uri, "upsert_keys", []string{"a"}); err == nil {
		t.Error("a subset of a composite unique index must refuse")
	}

	// A table with no unique index cannot merge at all.
	if _, err := db.Exec("CREATE TABLE upsert_heap (x INT)"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec("DROP TABLE IF EXISTS upsert_heap") })
	_, err = validateMySQLUpsertKey(ctx, uri, "upsert_heap", []string{"x"})
	if err == nil || !strings.Contains(err.Error(), "no unique index") {
		t.Errorf("a heap table must refuse with the reason: %v", err)
	}
}

// The validation must fire on the real path, not only when called directly:
// a sink_db upsert with a key that is not a unique index fails the run with
// the refusal, and one with the real key merges. This drives the whole
// engine -- source_file -> sink_db -- against the live server.
func TestSinkDBMySQLUpsertValidatesKeyEndToEnd(t *testing.T) {
	uri := mysqlTestURI(t)
	db := openMySQLForTest(t, uri)

	if _, err := db.Exec("DROP TABLE IF EXISTS sink_upsert_e2e"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE sink_upsert_e2e (
		id INT PRIMARY KEY, name TEXT, email VARCHAR(100), UNIQUE KEY uq_email (email)
	)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec("DROP TABLE IF EXISTS sink_upsert_e2e") })
	if _, err := db.Exec("INSERT INTO sink_upsert_e2e VALUES (1, 'before', 'one@x')"); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	csv := dir + "/rows.csv"
	writeSinkCSV(t, csv, "id,name,email\n1,after,one@x\n2,new,two@x\n")

	// name is a column, not a unique index: the run must fail with the
	// refusal, and the table must be untouched. The shared harness fatals
	// on a failed run, so this half drives the engine directly.
	err := runMySQLSinkExpectingFailure(t, csv, uri, "sink_upsert_e2e", []string{"name"})
	if err == nil {
		t.Fatal("upsert on a non-unique key should fail the run")
	}
	if !strings.Contains(err.Error(), "does not match any unique index") {
		t.Errorf("run error should carry the refusal, got: %v", err)
	}
	var name string
	if err := db.QueryRow("SELECT name FROM sink_upsert_e2e WHERE id = 1").Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "before" {
		t.Errorf("a refused upsert must write nothing: row changed to %q", name)
	}

	// The real key merges.
	run := runSinkPipeline(t, csv, uri, "sink_upsert_e2e", "upsert", []string{"id"})
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("upsert on the primary key failed: %s", run.Error)
	}
	if err := db.QueryRow("SELECT name FROM sink_upsert_e2e WHERE id = 1").Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "after" {
		t.Errorf("row not merged: name = %q, want %q", name, "after")
	}
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM sink_upsert_e2e").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("expected 2 rows after merge+insert, got %d", n)
	}
}

// runMySQLSinkExpectingFailure mirrors runSinkPipeline but hands back the
// run error instead of fataling on it, for asserting refusals.
func runMySQLSinkExpectingFailure(t *testing.T, csv, targetDB, table string, keyCols []string) error {
	t.Helper()
	dir := t.TempDir()
	s, err := store.NewSQLiteStore(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ks := make([]interface{}, len(keyCols))
	for i, k := range keyCols {
		ks[i] = k
	}
	pipeline := &models.Pipeline{
		ID: "sink-refusal", Name: "sink refusal",
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceFile, Name: "Src", Config: map[string]interface{}{"path": csv, "format": "csv"}},
			{ID: "sink", Type: models.NodeTypeSinkDB, Name: "Sink", Config: map[string]interface{}{
				"uri": targetDB, "table": table, "mode": "upsert", "key_columns": ks,
			}},
		},
		Edges:     []models.Edge{{From: "src", To: "sink"}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}
	eng := NewEngine(s)
	defer eng.Close(context.Background())
	_, err = eng.RunPipeline(pipeline.ID)
	return err
}

// A connection type with no compiled-in driver -- Oracle is the live
// example: advertised in the catalog, no driver -- used to fail a run with
// an error naming neither the connection nor its type, because the sentence
// that explained it went to the server's stdout. It has to reach the run's
// node log, which is what the pipeline author actually reads.
func TestUnsupportedConnectionTypeWarnsInTheRunLog(t *testing.T) {
	dir := t.TempDir()
	st, err := store.NewSQLiteStore(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	conn := &models.Connection{
		ConnID: "legacy-oracle", Type: "oracle",
		Host: "oracle.internal", Port: 1521, Schema: "ORCL",
		Login: "etl", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := st.CreateConnection(conn); err != nil {
		t.Skipf("this store rejects an oracle connection outright, which is also acceptable: %v", err)
	}

	resolver := NewConnectionResolver(st, nil)
	resolved, warnings := resolver.ResolveWithWarnings(
		map[string]interface{}{"conn_id": "legacy-oracle"}, models.NodeTypeSourceDB)

	if len(warnings) == 0 {
		t.Fatal("an unsupported connection type must produce a warning the run can log")
	}
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "legacy-oracle") {
		t.Errorf("the warning must name the connection: %s", joined)
	}
	if !strings.Contains(joined, "oracle") {
		t.Errorf("the warning must name the type: %s", joined)
	}
	if _, ok := resolved["uri"]; ok {
		t.Error("no URI should be fabricated for a driverless connection type")
	}
}
