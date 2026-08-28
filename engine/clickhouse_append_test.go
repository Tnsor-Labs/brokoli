package engine

import (
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

// ADR-027 phase 2: ClickHouse earns append. These are the earning papers --
// the equivalence corpus the bulk writer's registration claims, the DDL the
// renderer produces observed on the live server, and the count contract.

// chHostileRows is the corpus shape MySQL's capability was earned with:
// values that mean something to an escaper, a parser, or a protocol.
func chHostileRows() *common.DataSet {
	rows := []common.DataRow{
		{"id": int64(1), "city": `x\'; DROP TABLE t; --`, "amount": 10.25, "note": "plain"},
		{"id": int64(2), "city": `C:\Users\path\`, "amount": -3.5, "note": "tab\there"},
		{"id": int64(3), "city": "line\nbreak", "amount": 0.0, "note": `quote"double`},
		{"id": int64(4), "city": "naïve — 中文 🚀", "amount": 1e15, "note": "unicode"},
		{"id": int64(5), "city": "", "amount": 0.001, "note": "empty string stays empty"},
	}
	return &common.DataSet{Columns: []string{"id", "city", "amount", "note"}, Rows: rows}
}

// The equivalence argument: the native batch and the rendered-statement
// path must leave the table identical, over values chosen to break either.
// The statement path is the BROKOLI_SINK_COPY=0 escape hatch, so this also
// proves the hatch works for this backend.
func TestClickHouseAppendMatchesTheStatementPath(t *testing.T) {
	dsn := clickhouseTestDSN(t)
	db := openFor(t, dsn)

	results := map[string]string{}
	for _, path := range []string{"statement", "bulk"} {
		table := "cheq_" + path
		db.Exec("DROP TABLE IF EXISTS " + table)
		if _, err := db.Exec("CREATE TABLE " + table +
			" (id Int64, city String, amount Float64, note String) ENGINE = MergeTree ORDER BY id"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Exec("DROP TABLE IF EXISTS " + table) })

		if path == "statement" {
			t.Setenv("BROKOLI_SINK_COPY", "false")
		} else {
			t.Setenv("BROKOLI_SINK_COPY", "true")
		}
		cfg := SQLGenConfig{Dialect: "clickhouse", Table: table, BatchSize: 2, Mode: ModeAppend}
		ds := chHostileRows()

		w, bulk := bulkWriterFor(cfg)
		wantBulk := path == "bulk"
		if bulk != wantBulk {
			t.Fatalf("%s run offered the wrong path (bulk=%v) -- the comparison would be vacuous", path, bulk)
		}
		if bulk {
			n, err := bulkWriteRows(w, dsn, cfg, ds)
			if err != nil {
				t.Fatalf("bulk append: %v", err)
			}
			// The count contract: rows appended across acknowledged batches.
			if n != int64(len(ds.Rows)) {
				t.Errorf("count = %d, want %d appended rows", n, len(ds.Rows))
			}
		} else {
			stmt, err := GenerateSQL(cfg, ds)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ExecuteSQL(dsn, stmt); err != nil {
				t.Fatalf("statement append: %v", err)
			}
		}

		rows, err := db.Query("SELECT id, city, amount, note FROM " + table + " ORDER BY id")
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for rows.Next() {
			var id int64
			var amount float64
			var city, note string
			if err := rows.Scan(&id, &city, &amount, &note); err != nil {
				t.Fatal(err)
			}
			out = append(out, fmt.Sprintf("%d|%s|%v|%s", id, city, amount, note))
		}
		rows.Close()
		results[path] = strings.Join(out, "\u0000")
	}

	if results["bulk"] != results["statement"] {
		t.Errorf("bulk and statement appends diverged:\nstatement: %q\nbulk:      %q",
			results["statement"], results["bulk"])
	}
	// Anti-vacuity on the corpus itself: the injection string arrived as
	// DATA -- the table the "statement" would have dropped still exists,
	// and the value is stored verbatim.
	var city string
	if err := db.QueryRow("SELECT city FROM cheq_bulk WHERE id = 1").Scan(&city); err != nil {
		t.Fatal(err)
	}
	if city != `x\'; DROP TABLE t; --` {
		t.Errorf("hostile value corrupted in transit: %q", city)
	}
}

// create_table renders through the TypeRenderer: the default engine clause,
// Nullable as part of the type, and the created shape read back from the
// server rather than from the DDL text.
func TestClickHouseCreateTableRendersEngineAndNullable(t *testing.T) {
	dsn := clickhouseTestDSN(t)
	pg := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if pg == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	pdb := openFor(t, pg)
	pdb.Exec("DROP TABLE IF EXISTS ch_ct_src")
	if _, err := pdb.Exec(`CREATE TABLE ch_ct_src (
		id bigint NOT NULL, city text, amount numeric(12,2), seen timestamptz)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pdb.Exec("DROP TABLE IF EXISTS ch_ct_src") })
	if _, err := pdb.Exec("INSERT INTO ch_ct_src VALUES (9007199254740993, 'Lisbon', 10.25, now())"); err != nil {
		t.Fatal(err)
	}

	cdb := openFor(t, dsn)
	cdb.Exec("DROP TABLE IF EXISTS ch_ct_dst")
	t.Cleanup(func() { cdb.Exec("DROP TABLE IF EXISTS ch_ct_dst") })

	run := runMigrate(t, pg, "SELECT id, city, amount, seen FROM ch_ct_src", dsn, "ch_ct_dst")
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("migrate into ClickHouse failed: %s", run.Error)
	}

	// The created shape, from the server's own catalog.
	rows, err := cdb.Query(
		"SELECT name, type FROM system.columns WHERE database = currentDatabase() AND table = 'ch_ct_dst' ORDER BY position")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var name, typ string
		if err := rows.Scan(&name, &typ); err != nil {
			t.Fatal(err)
		}
		got[name] = typ
	}
	for col, want := range map[string]string{
		// Every column Nullable -- id's NOT NULL included -- because pgx
		// does not report nullability and #362 chose the permissive
		// answer (a NOT NULL the source did not report would fail the
		// load). That rule is pinned in TestSinkCreateTableCarriesTheSourcesTypes;
		// this asserts its ClickHouse rendering. The bare-type half is
		// proven from a MySQL source below, whose driver does report.
		"id":     "Nullable(Int64)",
		"city":   "Nullable(String)",
		"amount": "Nullable(Decimal(12, 2))",
		"seen":   "Nullable(DateTime64(6, 'UTC'))",
	} {
		if got[col] != want {
			t.Errorf("%s created as %q, want %q", col, got[col], want)
		}
	}

	// The engine default, from the catalog too.
	var engine string
	if err := cdb.QueryRow(
		"SELECT engine FROM system.tables WHERE database = currentDatabase() AND name = 'ch_ct_dst'").Scan(&engine); err != nil {
		t.Fatal(err)
	}
	if engine != "MergeTree" {
		t.Errorf("engine = %q, want the MergeTree default", engine)
	}

	// And the 2^53+1 value survived: only Int64 carries it, so the type
	// assertions above are proven by a value, not just by catalog text.
	var id int64
	if err := cdb.QueryRow("SELECT id FROM ch_ct_dst").Scan(&id); err != nil {
		t.Fatal(err)
	}
	if id != 9007199254740993 {
		t.Errorf("id = %d, want 9007199254740993 intact", id)
	}
}

// The stated, overridable default: table_engine in node config replaces the
// ENGINE clause, observed from the server's catalog.
// The source is MySQL on purpose: its driver reports nullability, so the
// NOT NULL id arrives non-nullable, renders bare Int64, and can be a sort
// key. From a Postgres source it could not -- pgx reports no nullability,
// #362's permissive rule wraps everything Nullable, and ClickHouse refuses
// a Nullable sorting key. That interplay is real and worth this comment: a
// table_engine override naming a sort key needs a source whose driver
// reports NOT NULL, or a pre-created table.
func TestClickHouseTableEngineOverride(t *testing.T) {
	dsn := clickhouseTestDSN(t)
	my := os.Getenv("BROKOLI_TEST_MYSQL_URL")
	if my == "" {
		t.Skip("BROKOLI_TEST_MYSQL_URL not set")
	}
	mdb := openFor(t, my)
	mdb.Exec("DROP TABLE IF EXISTS ch_eng_src")
	if _, err := mdb.Exec("CREATE TABLE ch_eng_src (id bigint NOT NULL, city text)"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mdb.Exec("DROP TABLE IF EXISTS ch_eng_src") })
	mdb.Exec("INSERT INTO ch_eng_src VALUES (1, 'a')")

	cdb := openFor(t, dsn)
	cdb.Exec("DROP TABLE IF EXISTS ch_eng_dst")
	t.Cleanup(func() { cdb.Exec("DROP TABLE IF EXISTS ch_eng_dst") })

	run := runMigrateWithConfig(t, map[string]interface{}{
		"source_uri": my, "source_query": "SELECT id, city FROM ch_eng_src",
		"dest_uri": dsn, "dest_table": "ch_eng_dst",
		"create_table": true, "mode": "append",
		"table_engine": "ReplacingMergeTree ORDER BY id",
	})
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("migrate failed: %s", run.Error)
	}
	var engine, sortKey string
	if err := cdb.QueryRow(
		"SELECT engine, sorting_key FROM system.tables WHERE database = currentDatabase() AND name = 'ch_eng_dst'").Scan(&engine, &sortKey); err != nil {
		t.Fatal(err)
	}
	if engine != "ReplacingMergeTree" || sortKey != "id" {
		t.Errorf("engine=%q sorting_key=%q, want the overridden ReplacingMergeTree ORDER BY id", engine, sortKey)
	}
	// And the id column really is the bare type the sort key required.
	var typ string
	if err := cdb.QueryRow(
		"SELECT type FROM system.columns WHERE database = currentDatabase() AND table = 'ch_eng_dst' AND name = 'id'").Scan(&typ); err != nil {
		t.Fatal(err)
	}
	if typ != "Int64" {
		t.Errorf("id = %q, want bare Int64 (MySQL reported NOT NULL and it must survive)", typ)
	}
}

// runMigrateWithConfig drives a migrate node with an explicit config map,
// for options runMigrate's fixed shape does not carry.
func runMigrateWithConfig(t *testing.T, cfg map[string]interface{}) *models.Run {
	t.Helper()
	dir := t.TempDir()
	st, err := store.NewSQLiteStore(filepath.Join(dir, "mc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	eng := drainEngineOnCleanup(t, NewEngine(st))
	p := &models.Pipeline{
		ID: "migcfg", Name: "migcfg", Enabled: true,
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
		return &models.Run{Status: models.RunStatusFailed, Error: err.Error()}
	}
	return run
}
