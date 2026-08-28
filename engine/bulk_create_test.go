package engine

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/store"
)

// #376: creating a table no longer costs the bulk path.
//
// The DDL and the rows used to be one SQL string, so one statement pinned
// fifty thousand rows to the statement path -- measured at about 7x. The
// DDL now runs inside the writer's own transaction instead.
//
// Two things have to hold for that to be a speed-up rather than a rewrite:
// both paths must produce the same table, and Postgres must still roll the
// creation back when the load fails. Both are asserted below against live
// servers, because both are properties of what the server ends up holding.

// pgTableExists reports whether a table is present.
func pgTableExists(t *testing.T, uri, table string) bool {
	t.Helper()
	var n int
	if err := openFor(t, uri).QueryRow(
		"SELECT count(*) FROM information_schema.tables WHERE table_name = $1", table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

// pgColumns reads a table's columns in ordinal order, with their types, as
// one comparable string.
func pgColumns(t *testing.T, uri, table string) string {
	t.Helper()
	rows, err := openFor(t, uri).Query(`
		SELECT column_name, data_type, coalesce(numeric_precision, -1), coalesce(numeric_scale, -1), is_nullable
		FROM information_schema.columns WHERE table_name = $1 ORDER BY ordinal_position`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name, typ, nullable string
		var prec, scale int
		if err := rows.Scan(&name, &typ, &prec, &scale, &nullable); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%s %s(%d,%d) null=%s", name, typ, prec, scale, nullable))
	}
	return strings.Join(out, "\n")
}

// pgContents reads every row as a comparable string.
func pgContents(t *testing.T, uri, table string) string {
	t.Helper()
	rows, err := openFor(t, uri).Query(
		"SELECT id, city, amount, seen_at FROM " + table + " ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id int64
		var city string
		var amount string
		var seen string
		if err := rows.Scan(&id, &city, &amount, &seen); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%d|%s|%s|%s", id, city, amount, seen))
	}
	return strings.Join(out, "\n")
}

// The equivalence argument: the two paths must produce the same table, in
// both its shape and its contents. Without this the speed-up would just be
// a different result arriving sooner.
func TestCreateTableIsIdenticalOnBothPaths(t *testing.T) {
	pg, _ := bothBackends(t)
	db := openFor(t, pg)

	db.Exec("DROP TABLE IF EXISTS bothpaths_src")
	if _, err := db.Exec(`CREATE TABLE bothpaths_src (
		id bigint NOT NULL, city text, amount numeric(12,2), seen_at timestamptz)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec("DROP TABLE IF EXISTS bothpaths_src") })
	if _, err := db.Exec(`INSERT INTO bothpaths_src
		SELECT g, 'city-'||(g%7), (g::numeric)/4, timestamptz '2026-08-27 10:00:00+02' + (g||' seconds')::interval
		FROM generate_series(1, 500) g`); err != nil {
		t.Fatal(err)
	}

	const q = "SELECT id, city, amount, seen_at FROM bothpaths_src"

	// Statement path first, so the comparison is against today's answer.
	t.Setenv("BROKOLI_SINK_COPY", "false")
	db.Exec("DROP TABLE IF EXISTS bothpaths_stmt")
	t.Cleanup(func() { db.Exec("DROP TABLE IF EXISTS bothpaths_stmt") })
	if run := runSourceToSink(t, pg, q, pg, "bothpaths_stmt", nil); run.Status != models.RunStatusSuccess {
		t.Fatalf("statement path failed: %s", run.Error)
	}
	// Anti-vacuity: this run really did avoid the writer.
	if _, ok := bulkWriterFor(SQLGenConfig{Dialect: "postgres", Table: "x", Mode: "append"}); ok {
		t.Fatal("BROKOLI_SINK_COPY=false did not disable the bulk path; the comparison would be against itself")
	}
	stmtCols := pgColumns(t, pg, "bothpaths_stmt")
	stmtRows := pgContents(t, pg, "bothpaths_stmt")

	// Bulk path.
	t.Setenv("BROKOLI_SINK_COPY", "true")
	if _, ok := bulkWriterFor(SQLGenConfig{Dialect: "postgres", Table: "x", Mode: "append"}); !ok {
		t.Fatal("the bulk path is not available; this test would compare the statement path to itself")
	}
	db.Exec("DROP TABLE IF EXISTS bothpaths_bulk")
	t.Cleanup(func() { db.Exec("DROP TABLE IF EXISTS bothpaths_bulk") })
	if run := runSourceToSink(t, pg, q, pg, "bothpaths_bulk", nil); run.Status != models.RunStatusSuccess {
		t.Fatalf("bulk path failed: %s", run.Error)
	}

	bulkCols := strings.ReplaceAll(pgColumns(t, pg, "bothpaths_bulk"), "bothpaths_bulk", "")
	if bulkCols != strings.ReplaceAll(stmtCols, "bothpaths_stmt", "") {
		t.Errorf("the two paths created different tables:\nstatement:\n%s\nbulk:\n%s", stmtCols, bulkCols)
	}
	if got := pgContents(t, pg, "bothpaths_bulk"); got != stmtRows {
		t.Errorf("the two paths wrote different rows:\nstatement had %d chars, bulk %d",
			len(stmtRows), len(got))
	}
	if len(strings.Split(stmtRows, "\n")) != 500 {
		t.Fatalf("expected 500 rows, statement path wrote %d", len(strings.Split(stmtRows, "\n")))
	}
}

// The property the naive version of this change would have lost: on
// Postgres the creation and the load are one transaction, so a failed load
// leaves nothing behind. Executing the DDL as a separate statement first
// would have left an empty table.
func TestAFailedBulkLoadRollsBackTheCreatedTable(t *testing.T) {
	pg, _ := bothBackends(t)
	db := openFor(t, pg)
	db.Exec("DROP TABLE IF EXISTS rollback_dst")
	t.Cleanup(func() { db.Exec("DROP TABLE IF EXISTS rollback_dst") })

	// Inference reads four fifths of the sample: four integers and one
	// word means the column is created INTEGER and the word then fails
	// the load, which is a failure that happens after the DDL succeeded.
	ds := &common.DataSet{
		Columns: []string{"n"},
		Rows: []common.DataRow{
			{"n": "1"}, {"n": "2"}, {"n": "3"}, {"n": "4"}, {"n": "not-a-number"},
		},
	}
	cfg, err := bulkCreateReady(SQLGenConfig{
		Dialect: "postgres", Table: "rollback_dst", Mode: "append",
		BatchSize: 100, CreateTable: true,
	}, ds)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CreateDDL == "" {
		t.Fatal("the DDL was not moved into the writer; this test would prove nothing")
	}
	w, ok := bulkWriterFor(cfg)
	if !ok {
		t.Fatal("no bulk writer")
	}
	if _, err := bulkWriteRows(w, pg, cfg, ds); err == nil {
		t.Fatal("the load should have failed on the non-numeric value")
	}
	if pgTableExists(t, pg, "rollback_dst") {
		t.Error("a failed load left the created table behind; the creation must roll back with it")
	}
}

// The rule that keeps this from changing behaviour where it cannot help: a
// destination with no bulk writer keeps emitting the DDL on the statement
// path, exactly as before.
func TestBulkCreateReadyLeavesTheStatementPathAlone(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"id"},
		Rows:    []common.DataRow{{"id": "1"}},
	}
	cases := []struct {
		name string
		cfg  SQLGenConfig
	}{
		{"no writer for this dialect", SQLGenConfig{Dialect: "sqlite", Table: "t", Mode: "append", CreateTable: true}},
		{"upsert has no bulk protocol", SQLGenConfig{Dialect: "postgres", Table: "t", Mode: ModeUpsert, CreateTable: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := bulkCreateReady(tc.cfg, ds)
			if err != nil {
				t.Fatal(err)
			}
			if !got.CreateTable {
				t.Error("CreateTable was cleared for a write no bulk writer will take, so nothing would create the table")
			}
			if got.CreateDDL != "" {
				t.Error("DDL was rendered for a path that cannot execute it")
			}
		})
	}

	// Nothing to create is left completely alone.
	plain := SQLGenConfig{Dialect: "postgres", Table: "t", Mode: "append"}
	got, err := bulkCreateReady(plain, ds)
	if err != nil {
		t.Fatal(err)
	}
	if got.CreateTable || got.CreateDDL != "" || got.Dialect != plain.Dialect || got.Mode != plain.Mode {
		t.Errorf("a config with no table to create must come back untouched, got %+v", got)
	}
}

// A migration is a bulk load and now says so. Upsert has no bulk protocol
// and must keep the chunked statement path.
func TestMigrateUsesTheBulkPathExceptForUpsert(t *testing.T) {
	pg, _ := bothBackends(t)
	db := openFor(t, pg)

	db.Exec("DROP TABLE IF EXISTS mbulk_src")
	if _, err := db.Exec("CREATE TABLE mbulk_src (id bigint, city text)"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec("DROP TABLE IF EXISTS mbulk_src") })
	if _, err := db.Exec(`INSERT INTO mbulk_src SELECT g, 'c'||g FROM generate_series(1,200) g`); err != nil {
		t.Fatal(err)
	}
	db.Exec("DROP TABLE IF EXISTS mbulk_dst")
	t.Cleanup(func() { db.Exec("DROP TABLE IF EXISTS mbulk_dst") })

	run, logs := runMigrateWithLogs(t, pg, "SELECT id, city FROM mbulk_src", pg, "mbulk_dst", "append")
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("migrate failed: %s", run.Error)
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM mbulk_dst").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 200 {
		t.Errorf("migrated %d rows, want 200", n)
	}
	// The bulk path names itself in the completion line; without that this
	// test passes just as well on the statement path it replaced.
	if !strings.Contains(logs, "(bulk)") {
		t.Errorf("an append migration should have used the bulk writer; log said:\n%s", logs)
	}

	// Upsert has no bulk protocol and must stay on the chunked statement
	// path -- the pairing matters, since a writer that silently took an
	// upsert would drop its conflict handling.
	db.Exec("DROP TABLE IF EXISTS mbulk_up")
	t.Cleanup(func() { db.Exec("DROP TABLE IF EXISTS mbulk_up") })
	if _, err := db.Exec("CREATE TABLE mbulk_up (id bigint PRIMARY KEY, city text)"); err != nil {
		t.Fatal(err)
	}
	upRun, upLogs := runMigrateWithLogs(t, pg, "SELECT id, city FROM mbulk_src", pg, "mbulk_up", "upsert")
	if upRun.Status != models.RunStatusSuccess {
		t.Fatalf("upsert migrate failed: %s", upRun.Error)
	}
	if strings.Contains(upLogs, "(bulk)") {
		t.Errorf("an upsert must not go through the bulk writer; log said:\n%s", upLogs)
	}
}

// runMigrateWithLogs drives a migrate node and hands back its run log, which
// is where the path taken is observable.
func runMigrateWithLogs(t *testing.T, srcURI, srcQuery, destURI, destTable, mode string) (*models.Run, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.NewSQLiteStore(filepath.Join(dir, "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	eng := drainEngineOnCleanup(t, NewEngine(st))

	cfg := map[string]interface{}{
		"source_uri": srcURI, "source_query": srcQuery,
		"dest_uri": destURI, "dest_table": destTable,
		"mode": mode,
	}
	if mode == "upsert" {
		cfg["key_columns"] = []interface{}{"id"}
	} else {
		cfg["create_table"] = true
	}
	p := &models.Pipeline{
		ID: "mb-" + destTable, Name: destTable, Enabled: true,
		Nodes: []models.Node{{
			ID: "mig", Type: models.NodeTypeMigrate, Name: "Mig", Config: cfg,
		}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := st.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	run, err := eng.RunPipeline(p.ID)
	if err != nil {
		return &models.Run{Status: models.RunStatusFailed, Error: err.Error()}, ""
	}
	entries, lerr := st.GetLogs(run.ID)
	if lerr != nil {
		t.Fatal(lerr)
	}
	var sb strings.Builder
	for _, l := range entries {
		sb.WriteString(l.Message)
		sb.WriteString("\n")
	}
	return run, sb.String()
}

// What moving the DDL into the writer's transaction is worth, measured the
// way #324 measured the COPY path it reuses.
//
//	BROKOLI_TEST_POSTGRES_URL=... go test ./engine/ -run xxx \
//	  -bench BenchmarkCreateTableWrite -benchtime 5x
func BenchmarkCreateTableWrite(b *testing.B) {
	uri := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if uri == "" {
		b.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	driver, dsn, err := DetectDriver(uri)
	if err != nil {
		b.Fatal(err)
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()

	const n = 50000
	rows := make([]common.DataRow, 0, n)
	for i := 0; i < n; i++ {
		rows = append(rows, common.DataRow{
			"id": int64(i), "city": fmt.Sprintf("city-%d", i%50),
			"amount": 10.25, "note": "some note text here",
		})
	}
	ds := &common.DataSet{Columns: []string{"id", "city", "amount", "note"}, Rows: rows}
	base := SQLGenConfig{Dialect: "postgres", Table: "bench_create_dst", BatchSize: 1000, Mode: "append"}

	write := func(b *testing.B, cfg SQLGenConfig) {
		b.Helper()
		for i := 0; i < b.N; i++ {
			b.StopTimer()
			db.Exec("DROP TABLE IF EXISTS bench_create_dst")
			ready, err := bulkCreateReady(cfg, ds)
			if err != nil {
				b.Fatal(err)
			}
			b.StartTimer()

			if w, ok := bulkWriterFor(ready); ok {
				if _, err := bulkWriteRows(w, uri, ready, ds); err != nil {
					b.Fatal(err)
				}
			} else {
				stmt, err := GenerateSQL(ready, ds)
				if err != nil {
					b.Fatal(err)
				}
				if _, err := ExecuteSQL(uri, stmt); err != nil {
					b.Fatal(err)
				}
			}
		}
		b.StopTimer()
		var got int
		db.QueryRow("SELECT count(*) FROM bench_create_dst").Scan(&got)
		if got != n {
			b.Fatalf("wrote %d rows, want %d -- the benchmark is not measuring the write", got, n)
		}
		db.Exec("DROP TABLE IF EXISTS bench_create_dst")
	}

	b.Run("create_table_bulk", func(b *testing.B) {
		cfg := base
		cfg.CreateTable = true
		write(b, cfg)
	})

	b.Run("create_table_statement", func(b *testing.B) {
		b.Setenv("BROKOLI_SINK_COPY", "false")
		cfg := base
		cfg.CreateTable = true
		write(b, cfg)
	})

	b.Run("append_into_existing_bulk", func(b *testing.B) {
		db.Exec("DROP TABLE IF EXISTS bench_create_dst")
		if _, err := db.Exec(`CREATE TABLE bench_create_dst (
			id bigint, city text, amount double precision, note text)`); err != nil {
			b.Fatal(err)
		}
		cfg := base
		for i := 0; i < b.N; i++ {
			w, ok := bulkWriterFor(cfg)
			if !ok {
				b.Fatal("no bulk writer")
			}
			if _, err := bulkWriteRows(w, uri, cfg, ds); err != nil {
				b.Fatal(err)
			}
			b.StopTimer()
			db.Exec("TRUNCATE bench_create_dst")
			b.StartTimer()
		}
		b.StopTimer()
		db.Exec("DROP TABLE IF EXISTS bench_create_dst")
	})
}
