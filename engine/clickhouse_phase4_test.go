package engine

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// ADR-027 phase 4 (#382): the pushdown question answered by measurement,
// and dbt-clickhouse as a first-class dbt backend.

// The measurement that settles pushdown. ADR-023 makes a free count source
// the price of sink-side eligibility -- runSinkDBFromTableRef fails a
// segment whose write reports no row count, "rather than commit a write
// whose size is unknown". clickhouse-go reports RowsAffected() == 0 for
// INSERT ... SELECT, so a ClickHouse segment can never pay that price:
// the compiler would plan segments whose executor must always fail.
//
// That is the primary, measured reason ClickHouse pushdown stays refused
// (the structural half is the missing Addresser, pinned in
// TestClickHouseClaimsNoAddresser). Two further blockers stand behind it,
// each its own ADR-023 violation: no transaction, so "inside a segment the
// commit is the artifact" cannot hold; and no lock primitive, so an
// overwrite segment cannot serialize against a concurrent one.
//
// If this test ever FAILS -- the driver starts reporting a real count --
// the primary refusal is stale: revisit ADR-027's deferred section, and
// only then consider Addresser plus the differential corpus.
func TestClickHouseInsertSelectReportsNoCount(t *testing.T) {
	dsn := clickhouseTestDSN(t)
	db, err := sql.Open("clickhouse", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	db.Exec("DROP TABLE IF EXISTS chp4_a")
	db.Exec("DROP TABLE IF EXISTS chp4_b")
	if _, err := db.Exec("CREATE TABLE chp4_a (id Int64) ENGINE = MergeTree ORDER BY id"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE chp4_b (id Int64) ENGINE = MergeTree ORDER BY id"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Exec("DROP TABLE IF EXISTS chp4_a")
		db.Exec("DROP TABLE IF EXISTS chp4_b")
	})
	if _, err := db.Exec("INSERT INTO chp4_a SELECT number FROM system.numbers LIMIT 500"); err != nil {
		t.Fatal(err)
	}

	res, err := db.Exec("INSERT INTO chp4_b SELECT id FROM chp4_a WHERE id >= 100")
	if err != nil {
		t.Fatal(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		t.Fatalf("RowsAffected errored (%v); the refusal reasoning assumes a silent zero", err)
	}
	// The server really moved 400 rows; verify so the zero below is
	// provably "unreported", not "nothing happened".
	var moved uint64
	if err := db.QueryRow("SELECT count() FROM chp4_b").Scan(&moved); err != nil {
		t.Fatal(err)
	}
	if moved != 400 {
		t.Fatalf("the INSERT SELECT moved %d rows, want 400 -- the measurement setup is broken", moved)
	}
	if n != 0 {
		t.Fatalf("clickhouse-go now reports RowsAffected=%d for INSERT ... SELECT. The measured "+
			"reason ClickHouse pushdown is refused (no free count source, ADR-023) is STALE -- "+
			"revisit ADR-027's deferred section before anything else changes", n)
	}
}

// The dbt half of phase 4: a dbt project runs against a Brokoli ClickHouse
// connection, per-model outcomes and all, and its output model is readable
// back -- ADR-025's read-back rule, satisfied end to end.
func TestDBTRunsOnClickHouse(t *testing.T) {
	dbtBinary(t)
	dsn := clickhouseTestDSN(t)
	cdb := openFor(t, dsn)
	cdb.Exec("DROP DATABASE IF EXISTS dbt_ch_e2e")
	t.Cleanup(func() { cdb.Exec("DROP DATABASE IF EXISTS dbt_ch_e2e") })

	dir := t.TempDir()
	projectDir := filepath.Join(dir, "proj")
	if err := os.MkdirAll(filepath.Join(projectDir, "models"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(p, body string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(projectDir, "dbt_project.yml"),
		"name: \"chproj\"\nversion: \"1.0.0\"\nconfig-version: 2\nprofile: \"chproj\"\n"+
			"model-paths: [\"models\"]\nmodels:\n  chproj:\n    +materialized: table\n")
	write(filepath.Join(projectDir, "models", "cities.sql"),
		"select 1 as id, 'lisbon' as city union all select 2, 'porto'\n")

	st, err := store.NewSQLiteStore(filepath.Join(dir, "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.CreateConnection(&models.Connection{
		ConnID: "ch", Type: models.ConnTypeClickHouse,
		Host: clickhouseHost(t, dsn), Port: clickhousePort(t, dsn),
		Login: "brokoli", Password: "brokoli",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	eng := drainEngineOnCleanup(t, NewEngine(st))
	eng.ConnResolver = NewConnectionResolver(st, nil)

	p := &models.Pipeline{
		ID: "dbt-ch", Name: "dbt ch", Enabled: true,
		Nodes: []models.Node{{
			ID: "dbt", Type: models.NodeTypeDBT, Name: "Build",
			Config: map[string]interface{}{
				"command": "run", "project_dir": projectDir,
				"conn_id": "ch", "target_schema": "dbt_ch_e2e",
			},
		}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := st.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	run, err := eng.RunPipeline(p.ID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("dbt run failed: %s", run.Error)
	}

	// The model is on the server, built by dbt through the generated
	// native-driver profile.
	var city string
	if err := cdb.QueryRow("SELECT city FROM dbt_ch_e2e.cities WHERE id = 1").Scan(&city); err != nil {
		t.Fatalf("read the built model back: %v", err)
	}
	if city != "lisbon" {
		t.Errorf("cities.id=1 = %q, want lisbon", city)
	}

	// And ADR-025's read-back rule, literally: Brokoli's own read path
	// consumes what dbt built.
	ds, err := QueryDatabase(dsn, "SELECT id, city FROM dbt_ch_e2e.cities ORDER BY id")
	if err != nil {
		t.Fatalf("source read of the dbt model: %v", err)
	}
	if len(ds.Rows) != 2 {
		t.Errorf("read back %d rows, want 2", len(ds.Rows))
	}

	// Per-model outcome recording works for this backend too.
	logs, err := st.GetLogs(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var sawModel bool
	for _, l := range logs {
		if strings.Contains(l.Message, "cities") {
			sawModel = true
		}
	}
	if !sawModel {
		t.Error("the run log never mentions the model; per-model reporting should not be backend-shaped")
	}
}
