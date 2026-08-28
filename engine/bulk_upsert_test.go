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

// #377: an upsert stages through the bulk writer -- COPY into a session
// temp table, one merge statement out -- instead of rendering every row as
// INSERT ... ON CONFLICT text. These tests are the capability's earning
// papers in ADR-024's sense: the staged path may exist only while the
// equivalence below holds.
//
// One behaviour change is deliberate and chosen, not inherited: duplicate
// keys within one dataset resolve to THE LAST ROW WINS. The statement path
// errored or not depending on where a 100-row batch boundary fell, which
// was an accident, not a contract. The tests pin both the new rule and the
// old accident, so the difference is documented by a test rather than
// discovered in production.

func upsertRows(n, offset int, tag string) *common.DataSet {
	rows := make([]common.DataRow, 0, n)
	for i := 0; i < n; i++ {
		rows = append(rows, common.DataRow{
			"id": int64(i + offset), "city": fmt.Sprintf("%s-%d", tag, i%20),
			"amount": 10.25, "note": "note " + tag,
		})
	}
	return &common.DataSet{Columns: []string{"id", "city", "amount", "note"}, Rows: rows}
}

func upsertCfg(table string) SQLGenConfig {
	return SQLGenConfig{Dialect: "postgres", Table: table, BatchSize: 1000,
		Mode: ModeUpsert, KeyColumns: []string{"id"}}
}

// writeUpsert drives one upsert through whichever path the config selects,
// and reports which it was -- the anti-vacuity handle every test here uses.
func writeUpsert(t *testing.T, uri string, cfg SQLGenConfig, ds *common.DataSet) (int64, bool) {
	t.Helper()
	if w, ok := bulkWriterFor(cfg); ok {
		n, err := bulkWriteRows(w, uri, cfg, ds)
		if err != nil {
			t.Fatalf("staged upsert: %v", err)
		}
		return n, true
	}
	stmt, err := GenerateSQL(cfg, ds)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	n, err := ExecuteSQL(uri, stmt)
	if err != nil {
		t.Fatalf("statement upsert: %v", err)
	}
	return n, false
}

// upsertTableContents reads the whole table in comparable form.
func upsertTableContents(t *testing.T, uri, table string) string {
	t.Helper()
	rows, err := openFor(t, uri).Query(
		"SELECT id, city, amount, note FROM " + table + " ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id int64
		var amount float64
		var city, note string
		if err := rows.Scan(&id, &city, &amount, &note); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%d|%s|%.2f|%s", id, city, amount, note))
	}
	return strings.Join(out, "\n")
}

// The equivalence argument: staged and statement upserts must leave the
// target byte-identical, through a mixed insert-and-update pass.
func TestStagedUpsertMatchesTheStatementPath(t *testing.T) {
	pg := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if pg == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	db := openFor(t, pg)

	results := map[string]string{}
	for _, path := range []string{"statement", "staged"} {
		table := "upeq_" + path
		db.Exec("DROP TABLE IF EXISTS " + table)
		if _, err := db.Exec("CREATE TABLE " + table +
			" (id bigint PRIMARY KEY, city text, amount double precision, note text)"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Exec("DROP TABLE IF EXISTS " + table) })

		if path == "statement" {
			t.Setenv("BROKOLI_SINK_COPY", "false")
		} else {
			t.Setenv("BROKOLI_SINK_COPY", "true")
		}

		// Pass 1 inserts 0..499; pass 2 updates 250..499 and inserts
		// 500..749 -- both the conflict and non-conflict arms in one run.
		_, bulk1 := writeUpsert(t, pg, upsertCfg(table), upsertRows(500, 0, "first"))
		_, bulk2 := writeUpsert(t, pg, upsertCfg(table), upsertRows(500, 250, "second"))
		wantBulk := path == "staged"
		if bulk1 != wantBulk || bulk2 != wantBulk {
			t.Fatalf("%s run took the wrong path (bulk=%v,%v) -- the comparison would be vacuous",
				path, bulk1, bulk2)
		}
		results[path] = upsertTableContents(t, pg, table)
	}

	if results["staged"] != results["statement"] {
		t.Errorf("staged and statement upserts diverged:\nstatement %d chars, staged %d chars",
			len(results["statement"]), len(results["staged"]))
	}
	if n := len(strings.Split(results["staged"], "\n")); n != 750 {
		t.Errorf("expected 750 rows, got %d", n)
	}
}

// The chosen rule: with duplicate keys in one dataset, the last row wins --
// and the same dataset still errors on the statement path when the
// duplicates share a batch, which pins that the change belongs to the
// staged path and is not an accident of something else.
func TestStagedUpsertResolvesInSetDuplicatesLastWriteWins(t *testing.T) {
	pg := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if pg == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	db := openFor(t, pg)
	db.Exec("DROP TABLE IF EXISTS updup")
	if _, err := db.Exec("CREATE TABLE updup (id bigint PRIMARY KEY, city text, amount double precision, note text)"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec("DROP TABLE IF EXISTS updup") })

	dup := &common.DataSet{
		Columns: []string{"id", "city", "amount", "note"},
		Rows: []common.DataRow{
			{"id": int64(1), "city": "first", "amount": 1.0, "note": "n"},
			{"id": int64(2), "city": "only", "amount": 2.0, "note": "n"},
			{"id": int64(1), "city": "second", "amount": 1.5, "note": "n"},
			{"id": int64(1), "city": "third", "amount": 1.75, "note": "n"},
		},
	}

	affected, bulk := writeUpsert(t, pg, upsertCfg("updup"), dup)
	if !bulk {
		t.Fatal("the staged path did not engage; nothing is being tested")
	}
	// The count is distinct rows merged, not rows staged.
	if affected != 2 {
		t.Errorf("affected = %d, want 2 (four staged rows, two distinct keys)", affected)
	}
	var city string
	if err := db.QueryRow("SELECT city FROM updup WHERE id = 1").Scan(&city); err != nil {
		t.Fatal(err)
	}
	if city != "third" {
		t.Errorf("id=1 city = %q, want %q (the last staged row wins)", city, "third")
	}

	// The old accident, pinned: the same dataset through the statement
	// path errors, because all four rows share one batch. If Postgres
	// ever stops erroring here, the behaviour-change note in the staged
	// path's documentation is stale and this failure says so.
	t.Setenv("BROKOLI_SINK_COPY", "false")
	stmt, err := GenerateSQL(upsertCfg("updup"), dup)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ExecuteSQL(pg, stmt); err == nil {
		t.Error("the statement path accepted in-batch duplicate keys; it used to refuse, and the " +
			"staged path's documented behaviour change assumes it still does")
	} else if !strings.Contains(err.Error(), "second time") {
		t.Errorf("expected the affect-row-a-second-time refusal, got: %v", err)
	}
}

// A failing merge must leave the target untouched and no stage behind. The
// stage is ON COMMIT DROP in the load's own transaction, so this is the
// same one-transaction property the append path has.
func TestAFailedStagedUpsertLeavesNothingBehind(t *testing.T) {
	pg := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if pg == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	db := openFor(t, pg)
	db.Exec("DROP TABLE IF EXISTS upfail")
	// city has no unique index, so ON CONFLICT (city) is refused by the
	// server -- after the stage was created and loaded.
	if _, err := db.Exec("CREATE TABLE upfail (id bigint PRIMARY KEY, city text, amount double precision, note text)"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec("DROP TABLE IF EXISTS upfail") })
	if _, err := db.Exec("INSERT INTO upfail VALUES (1, 'keep', 0, 'keep')"); err != nil {
		t.Fatal(err)
	}

	cfg := upsertCfg("upfail")
	cfg.KeyColumns = []string{"city"}
	w, ok := bulkWriterFor(cfg)
	if !ok {
		t.Fatal("no writer")
	}
	_, err := bulkWriteRows(w, pg, cfg, upsertRows(10, 0, "x"))
	if err == nil {
		t.Fatal("a merge key with no unique index must be refused")
	}
	// The refusal is named -- what was asked for, and what the table
	// actually has -- rather than the server's generic constraint error.
	if !strings.Contains(err.Error(), "unique index") || !strings.Contains(err.Error(), "city") {
		t.Errorf("the refusal should name the key and the table's unique indexes, got: %v", err)
	}

	var n int
	if err := db.QueryRow("SELECT count(*) FROM upfail").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("target has %d rows, want the 1 pre-existing row -- the failed merge leaked", n)
	}
	// And the pipeline-level behaviour matches: a sink_db run with the
	// same bad key fails rather than reporting success.
	run := runSourceToSinkUpsert(t, pg, "upfail", []string{"city"})
	if run.Status == models.RunStatusSuccess {
		t.Error("a run whose merge was refused must not report success")
	}
}

// runSourceToSinkUpsert drives source_db -> sink_db(upsert) end to end.
func runSourceToSinkUpsert(t *testing.T, uri, table string, keys []string) *models.Run {
	t.Helper()
	db := openFor(t, uri)
	db.Exec("DROP TABLE IF EXISTS upsrc")
	if _, err := db.Exec("CREATE TABLE upsrc (id bigint, city text, amount double precision, note text)"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec("DROP TABLE IF EXISTS upsrc") })
	if _, err := db.Exec("INSERT INTO upsrc VALUES (5, 'c', 1, 'n')"); err != nil {
		t.Fatal(err)
	}
	keyList := make([]interface{}, len(keys))
	for i, k := range keys {
		keyList[i] = k
	}

	dir := t.TempDir()
	st, err := store.NewSQLiteStore(filepath.Join(dir, "u.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	eng := drainEngineOnCleanup(t, NewEngine(st))
	p := &models.Pipeline{
		ID: "up-" + table, Name: table, Enabled: true,
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceDB, Name: "S",
				Config: map[string]interface{}{"uri": uri, "query": "SELECT id, city, amount, note FROM upsrc"}},
			{ID: "sink", Type: models.NodeTypeSinkDB, Name: "K",
				Config: map[string]interface{}{"uri": uri, "table": table, "mode": "upsert", "key_columns": keyList}},
		},
		Edges:     []models.Edge{{From: "src", To: "sink"}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := st.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	run, err := eng.RunPipeline(p.ID)
	if err != nil {
		return &models.Run{Status: models.RunStatusFailed, Error: err.Error()}
	}
	return run
}

// The numbers behind the staged path, and behind the correction to the
// "measured slower" note this file's writer used to carry.
//
//	BROKOLI_TEST_POSTGRES_URL=... go test ./engine/ -run xxx \
//	  -bench BenchmarkUpsertWrite -benchtime 5x
func BenchmarkUpsertWrite(b *testing.B) {
	for _, backend := range []struct{ name, env, dialect, createSQL string }{
		{"postgres", "BROKOLI_TEST_POSTGRES_URL", "postgres",
			"CREATE TABLE upbench (id bigint PRIMARY KEY, city text, amount double precision, note text)"},
		{"mysql", "BROKOLI_TEST_MYSQL_URL", "mysql",
			"CREATE TABLE upbench (id bigint PRIMARY KEY, city text, amount double, note text)"},
	} {
		uri := os.Getenv(backend.env)
		if uri == "" {
			continue
		}
		b.Run(backend.name, func(b *testing.B) {
			benchUpsertBackend(b, uri, backend.dialect, backend.createSQL)
		})
	}
}

func benchUpsertBackend(b *testing.B, uri, dialect, createSQL string) {
	db, err := openBench(uri)
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()

	for _, size := range []int{1000, 10000, 50000} {
		ds := upsertRowsBench(size)
		for _, path := range []string{"statement", "staged"} {
			for _, load := range []string{"all_insert", "all_conflict"} {
				b.Run(fmt.Sprintf("%s/%s/%drows", path, load, size), func(b *testing.B) {
					if path == "statement" {
						b.Setenv("BROKOLI_SINK_COPY", "false")
					} else {
						b.Setenv("BROKOLI_SINK_COPY", "true")
					}
					for i := 0; i < b.N; i++ {
						b.StopTimer()
						db.Exec("DROP TABLE IF EXISTS upbench")
						db.Exec(createSQL)
						cfg := upsertCfg("upbench")
						cfg.Dialect = dialect
						if load == "all_conflict" {
							// Pre-seed so every row is an update.
							if err := benchWrite(uri, cfg, ds); err != nil {
								b.Fatal(err)
							}
						}
						b.StartTimer()
						if err := benchWrite(uri, cfg, ds); err != nil {
							b.Fatal(err)
						}
					}
					b.StopTimer()
					var n int
					db.QueryRow("SELECT count(*) FROM upbench").Scan(&n)
					if n != size {
						b.Fatalf("wrote %d rows, want %d", n, size)
					}
					db.Exec("DROP TABLE IF EXISTS upbench")
				})
			}
		}
	}
}

func upsertRowsBench(n int) *common.DataSet {
	rows := make([]common.DataRow, 0, n)
	for i := 0; i < n; i++ {
		rows = append(rows, common.DataRow{
			"id": int64(i), "city": fmt.Sprintf("city-%d", i%50),
			"amount": 10.25, "note": "some note text here"})
	}
	return &common.DataSet{Columns: []string{"id", "city", "amount", "note"}, Rows: rows}
}

func openBench(uri string) (*sql.DB, error) {
	driver, dsn, err := DetectDriver(uri)
	if err != nil {
		return nil, err
	}
	return sql.Open(driver, dsn)
}

func benchWrite(uri string, cfg SQLGenConfig, ds *common.DataSet) error {
	if w, ok := bulkWriterFor(cfg); ok {
		_, err := bulkWriteRows(w, uri, cfg, ds)
		return err
	}
	stmt, err := GenerateSQL(cfg, ds)
	if err != nil {
		return err
	}
	_, err = ExecuteSQL(uri, stmt)
	return err
}

// --- MySQL (#377 phase 2) ---

// The MySQL equivalence argument, with two rows of hostile values in the
// dataset so the LOAD DATA escaping is exercised on this path too -- the
// merge itself renders no values, but the claim is about the whole write.
func TestMySQLStagedUpsertMatchesTheStatementPath(t *testing.T) {
	my := os.Getenv("BROKOLI_TEST_MYSQL_URL")
	if my == "" {
		t.Skip("BROKOLI_TEST_MYSQL_URL not set")
	}
	db := openFor(t, my)

	hostile := func(offset int) *common.DataSet {
		ds := upsertRows(300, offset, "pass")
		ds.Rows = append(ds.Rows,
			common.DataRow{"id": int64(offset + 9000), "city": `x\'; DROP TABLE t; --`, "amount": 1.0, "note": "tab\there"},
			common.DataRow{"id": int64(offset + 9001), "city": `C:\Users\path`, "amount": 2.0, "note": "line\nbreak"},
		)
		return ds
	}

	results := map[string]string{}
	for _, path := range []string{"statement", "staged"} {
		table := "myeq_" + path
		db.Exec("DROP TABLE IF EXISTS " + table)
		if _, err := db.Exec("CREATE TABLE " + table +
			" (id bigint PRIMARY KEY, city text, amount double, note text)"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Exec("DROP TABLE IF EXISTS " + table) })

		if path == "statement" {
			t.Setenv("BROKOLI_SINK_COPY", "false")
		} else {
			t.Setenv("BROKOLI_SINK_COPY", "true")
		}
		cfg := upsertCfg(table)
		cfg.Dialect = "mysql"

		_, bulk1 := writeUpsert(t, my, cfg, hostile(0))
		_, bulk2 := writeUpsert(t, my, cfg, hostile(150)) // 150..299 update, 300..449 insert
		wantBulk := path == "staged"
		if bulk1 != wantBulk || bulk2 != wantBulk {
			t.Fatalf("%s run took the wrong path (bulk=%v,%v)", path, bulk1, bulk2)
		}
		results[path] = upsertTableContents(t, my, table)
	}

	if results["staged"] != results["statement"] {
		t.Errorf("staged and statement upserts diverged on MySQL:\nstatement %d chars, staged %d chars",
			len(results["statement"]), len(results["staged"]))
	}
	// Both passes' hostile rows: 4 distinct ids among them. Counted by
	// the server, not by splitting the rendered contents on newlines --
	// the hostile notes deliberately contain newlines.
	var n int
	if err := db.QueryRow("SELECT count(*) FROM myeq_staged").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 454 {
		t.Errorf("expected 454 rows, got %d", n)
	}
}

// The dup-key rule on MySQL: last-write-wins, same as its statement path
// always was -- but now guaranteed by ORDER BY rather than left to the
// select order the server happened to choose.
func TestMySQLStagedUpsertResolvesInSetDuplicatesLastWriteWins(t *testing.T) {
	my := os.Getenv("BROKOLI_TEST_MYSQL_URL")
	if my == "" {
		t.Skip("BROKOLI_TEST_MYSQL_URL not set")
	}
	db := openFor(t, my)
	db.Exec("DROP TABLE IF EXISTS mydup")
	if _, err := db.Exec("CREATE TABLE mydup (id bigint PRIMARY KEY, city text, amount double, note text)"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec("DROP TABLE IF EXISTS mydup") })

	dup := &common.DataSet{
		Columns: []string{"id", "city", "amount", "note"},
		Rows: []common.DataRow{
			{"id": int64(1), "city": "first", "amount": 1.0, "note": "n"},
			{"id": int64(1), "city": "second", "amount": 1.5, "note": "n"},
			{"id": int64(1), "city": "third", "amount": 1.75, "note": "n"},
		},
	}
	cfg := upsertCfg("mydup")
	cfg.Dialect = "mysql"
	_, bulk := writeUpsert(t, my, cfg, dup)
	if !bulk {
		t.Fatal("the staged path did not engage")
	}
	var city string
	if err := db.QueryRow("SELECT city FROM mydup WHERE id = 1").Scan(&city); err != nil {
		t.Fatal(err)
	}
	if city != "third" {
		t.Errorf("id=1 city = %q, want %q (the last staged row wins)", city, "third")
	}
}

// A bad merge key is refused by name before any rows move, on this path
// too -- the writer itself validates, because the streamed sink reaches it
// without going through runSinkDB's check.
func TestMySQLStagedUpsertRefusesABadKey(t *testing.T) {
	my := os.Getenv("BROKOLI_TEST_MYSQL_URL")
	if my == "" {
		t.Skip("BROKOLI_TEST_MYSQL_URL not set")
	}
	db := openFor(t, my)
	db.Exec("DROP TABLE IF EXISTS mybad")
	if _, err := db.Exec("CREATE TABLE mybad (id bigint PRIMARY KEY, city text, amount double, note text)"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec("DROP TABLE IF EXISTS mybad") })
	if _, err := db.Exec("INSERT INTO mybad VALUES (1, 'keep', 0, 'keep')"); err != nil {
		t.Fatal(err)
	}

	cfg := upsertCfg("mybad")
	cfg.Dialect = "mysql"
	cfg.KeyColumns = []string{"city"}
	w, ok := bulkWriterFor(cfg)
	if !ok {
		t.Fatal("no writer")
	}
	_, err := bulkWriteRows(w, my, cfg, upsertRows(5, 0, "x"))
	if err == nil {
		t.Fatal("a key that matches no unique index must be refused")
	}
	if !strings.Contains(err.Error(), "unique index") {
		t.Errorf("the refusal should name the problem, got: %v", err)
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM mybad").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("target has %d rows, want 1 -- the refused write leaked", n)
	}
}
