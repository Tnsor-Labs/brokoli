package engine

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

func TestMySQLLoadEscapeStructuralCharacters(t *testing.T) {
	for in, want := range map[string]string{
		"plain":      "plain",
		"tab\there":  `tab\there`,
		"line\nfee":  `line\nfee`,
		"cr\rhere":   `cr\rhere`,
		`back\one`:   `back\\one`,
		`C:\Users\x`: `C:\\Users\\x`,
		`trailing\`:  `trailing\\`,
		// Quotes are data on a load stream, exactly as on COPY.
		`it's "q"`: `it's "q"`,
	} {
		if got := mysqlLoadEscape(in); got != want {
			t.Errorf("mysqlLoadEscape(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMySQLLoadEscapeNullBoolTime(t *testing.T) {
	if got := mysqlLoadEscape(nil); got != `\N` {
		t.Errorf("nil = %q, want \\N", got)
	}
	if got := mysqlLoadEscape(""); got != "" {
		t.Errorf("empty string must stay empty (distinct from NULL), got %q", got)
	}
	// The keywords TRUE/FALSE would be the *string* TRUE in a load field,
	// which coerces to 0 in a numeric column. 1 and 0 are what the keywords
	// evaluate to on the statement path, in every column type.
	if got := mysqlLoadEscape(true); got != "1" {
		t.Errorf("true = %q, want 1", got)
	}
	if got := mysqlLoadEscape(false); got != "0" {
		t.Errorf("false = %q, want 0", got)
	}
	// Same layout and UTC conversion as the mysql dialect's literal, minus
	// the quotes fields do not carry.
	ts := time.Date(2026, 8, 24, 15, 4, 5, 123456000, time.FixedZone("X", 3600))
	if got, want := mysqlLoadEscape(ts), "2026-08-24 14:04:05.123456"; got != want {
		t.Errorf("time = %q, want %q", got, want)
	}
}

// mysqlComLoad reads the server's LOAD DATA statement counter -- the
// anti-vacuity instrument: a path assertion the Go side cannot fake.
func mysqlComLoad(t *testing.T, uri string) int {
	t.Helper()
	db := openMySQLForTest(t, uri)
	var name string
	var n int
	if err := db.QueryRow("SHOW GLOBAL STATUS LIKE 'Com_load'").Scan(&name, &n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The capability rule from ADR-024: the bulk path may exist only because
// this comparison proves it against the statement path. One corpus, both
// paths, byte-identical destination tables -- and the server's own counter
// proves LOAD DATA actually ran, so the equivalence cannot pass vacuously
// with both sides on the statement path.
func TestMySQLBulkEquivalence(t *testing.T) {
	uri := mysqlTestURI(t)
	db := openMySQLForTest(t, uri)

	for _, tbl := range []string{"bulk_eq_load", "bulk_eq_stmt"} {
		if _, err := db.Exec("DROP TABLE IF EXISTS " + tbl); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("CREATE TABLE " + tbl + " (id INT, v TEXT, flag BOOLEAN, ts DATETIME(6))"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Exec("DROP TABLE IF EXISTS " + tbl) })
	}

	ts := time.Date(2026, 8, 24, 15, 4, 5, 123456000, time.UTC)
	rows := []common.DataRow{}
	for i, hv := range hostileValues() {
		rows = append(rows, common.DataRow{"id": i, "v": hv.value, "flag": i%2 == 0, "ts": ts})
	}
	rows = append(rows, common.DataRow{"id": 1000, "v": nil, "flag": nil, "ts": nil})
	ds := &common.DataSet{Columns: []string{"id", "v", "flag", "ts"}, Rows: rows}

	before := mysqlComLoad(t, uri)
	if _, err := bulkWriteRows(loadBatchesToMySQL, uri, SQLGenConfig{
		Dialect: "mysql", Table: "bulk_eq_load", Mode: ModeAppend,
	}, ds); err != nil {
		t.Fatalf("bulk path: %v", err)
	}
	if after := mysqlComLoad(t, uri); after <= before {
		t.Fatal("Com_load did not increase: the bulk path did not actually use LOAD DATA, so this comparison proves nothing")
	}

	stmt, err := GenerateSQL(SQLGenConfig{Dialect: "mysql", Table: "bulk_eq_stmt", Mode: ModeAppend}, ds)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ExecuteSQL(uri, stmt); err != nil {
		t.Fatalf("statement path: %v", err)
	}

	// Byte-identical, row by row, NULLs included.
	q := func(tbl string) []string {
		var out []string
		rs, err := db.Query("SELECT id, COALESCE(v, '<NULL>'), COALESCE(CAST(flag AS CHAR), '<NULL>'), COALESCE(CAST(ts AS CHAR), '<NULL>') FROM " + tbl + " ORDER BY id")
		if err != nil {
			t.Fatal(err)
		}
		defer rs.Close()
		for rs.Next() {
			var id int
			var v, f, tsv string
			if err := rs.Scan(&id, &v, &f, &tsv); err != nil {
				t.Fatal(err)
			}
			out = append(out, fmt.Sprintf("%d|%s|%s|%s", id, v, f, tsv))
		}
		return out
	}
	got, want := q("bulk_eq_load"), q("bulk_eq_stmt")
	if len(got) != len(want) {
		t.Fatalf("row counts differ: bulk %d, statement %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("row %d differs:\n  bulk:      %s\n  statement: %s", i, got[i], want[i])
		}
	}
}

// Overwrite through the bulk path keeps Phase 2's atomicity contract: a
// failure mid-stream rolls back both the clear and the partial load, so the
// table still holds what it held before the run started.
func TestMySQLBulkOverwriteAtomic(t *testing.T) {
	uri := mysqlTestURI(t)
	db := openMySQLForTest(t, uri)

	if _, err := db.Exec("DROP TABLE IF EXISTS bulk_atomic"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE bulk_atomic (id INT, v TEXT)"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec("DROP TABLE IF EXISTS bulk_atomic") })
	if _, err := db.Exec("INSERT INTO bulk_atomic VALUES (1, 'survivor'), (2, 'survivor')"); err != nil {
		t.Fatal(err)
	}

	sent := false
	_, err := loadBatchesToMySQL(context.Background(), uri, SQLGenConfig{
		Dialect: "mysql", Table: "bulk_atomic", Mode: ModeOverwrite,
	}, []string{"id", "v"}, func() (*common.DataSet, error) {
		if sent {
			return nil, fmt.Errorf("simulated source failure mid-stream")
		}
		sent = true
		return &common.DataSet{Columns: []string{"id", "v"},
			Rows: []common.DataRow{{"id": 10, "v": "doomed"}}}, nil
	})
	if err == nil {
		t.Fatal("a failing source must fail the write")
	}

	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM bulk_atomic WHERE v = 'survivor'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("overwrite failure must leave previous rows intact: want 2 survivors, got %d", n)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM bulk_atomic WHERE v = 'doomed'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("partial load must roll back: found %d doomed rows", n)
	}
}

// A server with local_infile off gets the per-batch INSERT fallback: same
// transaction shape, same contents, no LOAD DATA -- proven by the server's
// own counter standing still. Needs root to toggle the global, so it skips
// where only the app user is available.
func TestMySQLBulkFallsBackWhenLocalInfileDisabled(t *testing.T) {
	uri := mysqlTestURI(t)
	rootURI := os.Getenv("BROKOLI_TEST_MYSQL_ROOT_URL")
	if rootURI == "" {
		t.Skip("BROKOLI_TEST_MYSQL_ROOT_URL not set")
	}
	root := openMySQLForTest(t, rootURI)
	if _, err := root.Exec("SET GLOBAL local_infile = 0"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := root.Exec("SET GLOBAL local_infile = 1"); err != nil {
			t.Errorf("restore local_infile: %v", err)
		}
	}()

	db := openMySQLForTest(t, uri)
	if _, err := db.Exec("DROP TABLE IF EXISTS bulk_fallback"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE bulk_fallback (id INT, v TEXT)"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec("DROP TABLE IF EXISTS bulk_fallback") })

	ds := &common.DataSet{Columns: []string{"id", "v"}}
	for i, hv := range hostileValues() {
		ds.Rows = append(ds.Rows, common.DataRow{"id": i, "v": hv.value})
	}

	before := mysqlComLoad(t, uri)
	affected, err := bulkWriteRows(loadBatchesToMySQL, uri, SQLGenConfig{
		Dialect: "mysql", Table: "bulk_fallback", Mode: ModeAppend,
	}, ds)
	if err != nil {
		t.Fatalf("the fallback must succeed where LOAD DATA is refused: %v", err)
	}
	if int(affected) != len(ds.Rows) {
		t.Errorf("affected = %d, want %d", affected, len(ds.Rows))
	}
	if after := mysqlComLoad(t, uri); after != before {
		t.Error("Com_load moved: the fallback still used LOAD DATA somehow")
	}
	for i, hv := range hostileValues() {
		var v string
		if err := db.QueryRow("SELECT v FROM bulk_fallback WHERE id = ?", i).Scan(&v); err != nil {
			t.Fatal(err)
		}
		if v != hv.value {
			t.Errorf("%s: fallback corrupted the value: got %q want %q", hv.name, v, hv.value)
		}
	}
}

// The whole point of Phase 3: a MySQL sink is now stream-eligible, and an
// end-to-end run through the engine takes the bulk path -- the server's
// counter, again, is what says so.
func TestSinkDBMySQLBulkEndToEnd(t *testing.T) {
	uri := mysqlTestURI(t)
	db := openMySQLForTest(t, uri)

	if _, err := db.Exec("DROP TABLE IF EXISTS bulk_e2e"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE bulk_e2e (id INT, name TEXT)"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec("DROP TABLE IF EXISTS bulk_e2e") })

	dir := t.TempDir()
	csv := dir + "/rows.csv"
	var sb strings.Builder
	sb.WriteString("id,name\n")
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&sb, "%d,name-%d\n", i, i)
	}
	writeSinkCSV(t, csv, sb.String())

	before := mysqlComLoad(t, uri)
	run := runSinkPipeline(t, csv, uri, "bulk_e2e", "append", nil)
	if run.Status != "success" {
		t.Fatalf("run status = %s (error: %s)", run.Status, run.Error)
	}
	if after := mysqlComLoad(t, uri); after <= before {
		t.Error("the end-to-end write did not take the LOAD DATA path")
	}
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM bulk_e2e").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 500 {
		t.Errorf("rows = %d, want 500", n)
	}
}
