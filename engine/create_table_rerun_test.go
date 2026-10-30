package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// create_table promises "create the table if it does not exist", and the
// generator emitted a plain CREATE TABLE: a pipeline worked on its first
// run and failed every run after with "already exists". Each backend that
// can be written to runs the same sink twice here, on both write paths
// where it has two, and must end with the rows the mode asks for.
func TestCreateTableSinkRunsAgainOnEveryBackend(t *testing.T) {
	backends := map[string]string{
		"sqlite": filepath.Join(t.TempDir(), "rerun.db"),
	}
	for name, env := range map[string]string{
		"postgres":   "BROKOLI_TEST_POSTGRES_URL",
		"mysql":      "BROKOLI_TEST_MYSQL_URL",
		"clickhouse": "BROKOLI_TEST_CLICKHOUSE_URL",
		"sqlserver":  "BROKOLI_TEST_SQLSERVER_URL",
	} {
		if uri := os.Getenv(env); uri != "" {
			backends[name] = uri
		}
	}
	src := writeCSV(t, t.TempDir(), "rows.csv", "id,city\n1,Maputo\n2,Beira\n3,Nampula\n")

	for name, uri := range backends {
		for _, path := range []string{"bulk", "statement"} {
			for _, tc := range []struct {
				mode string
				want int // rows after two runs of three
			}{{"append", 6}, {"overwrite", 3}} {
				t.Run(name+"/"+path+"/"+tc.mode, func(t *testing.T) {
					t.Setenv("BROKOLI_SINK_COPY", map[string]string{"bulk": "true", "statement": "false"}[path])
					table := "rerun_" + path + "_" + tc.mode
					db := openFor(t, uri)
					drop := func() { _, _ = db.Exec("DROP TABLE IF EXISTS " + table) }
					drop()
					t.Cleanup(drop)

					for run := 1; run <= 2; run++ {
						if r := runFileToSink(t, src, uri, table, tc.mode); r.Status != models.RunStatusSuccess {
							t.Fatalf("run %d failed: %s", run, r.Error)
						}
					}
					var n int
					if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
						t.Fatal(err)
					}
					if n != tc.want {
						t.Errorf("%d rows after two %s runs of 3, want %d", n, tc.mode, tc.want)
					}
				})
			}
		}
	}
}

// runFileToSink runs source_file -> sink_db with create_table set.
func runFileToSink(t *testing.T, csvPath, destURI, table, mode string) *models.Run {
	t.Helper()
	st, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	eng := drainEngineOnCleanup(t, NewEngine(st))
	p := &models.Pipeline{
		ID: "rerun-" + table + "-" + time.Now().Format("150405.000000000"), Name: table, Enabled: true,
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceFile, Name: "Source", Config: map[string]interface{}{"path": csvPath, "format": "csv"}},
			{ID: "sink", Type: models.NodeTypeSinkDB, Name: "Sink", Config: map[string]interface{}{
				"uri": destURI, "table": table, "create_table": true, "mode": mode,
			}},
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

// The DDL each dialect is given, both renderers: the one that infers
// types and the one that renders carried column types.
func TestCreateTableDDLDoesNothingWhenTheTableExists(t *testing.T) {
	for dialectName, want := range map[string]string{
		"postgres":   `CREATE TABLE IF NOT EXISTS "t" (`,
		"mysql":      "CREATE TABLE IF NOT EXISTS `t` (",
		"clickhouse": "CREATE TABLE IF NOT EXISTS `t` (",
		"sqlite":     `CREATE TABLE IF NOT EXISTS "t" (`,
		"snowflake":  `CREATE TABLE IF NOT EXISTS "t" (`,
		"databricks": "CREATE TABLE IF NOT EXISTS `t` (",
		"sqlserver":  "IF OBJECT_ID(N'[t]', N'U') IS NULL CREATE TABLE [t] (",
		"mssql":      "IF OBJECT_ID(N'[t]', N'U') IS NULL CREATE TABLE [t] (",
		// Nothing is known about a generic server; it keeps the plain form.
		"generic": `CREATE TABLE "t" (`,
	} {
		d := getDialect(dialectName)
		if got := d.createTable("t", []string{"a"}, map[string]string{"a": "INTEGER"}); !strings.HasPrefix(got, want) {
			t.Errorf("%s inferred DDL = %q, want it to start %q", dialectName, got, want)
		}
	}
}

// A schema-qualified name keeps its quoting inside SQL Server's OBJECT_ID
// literal, and a quote in the name cannot end the literal early.
func TestSQLServerCreateGuardQuotesTheName(t *testing.T) {
	d := getDialect("sqlserver")
	got := d.createTableHead("dbo.it's")
	want := "IF OBJECT_ID(N'[dbo].[it''s]', N'U') IS NULL CREATE TABLE [dbo].[it's]"
	if got != want {
		t.Errorf("head = %q, want %q", got, want)
	}
}

// The same promise through the other renderer: a source_db upstream
// reports its column types, and the table is created from those (#363)
// rather than from sampled values.
func TestCreateTableFromCarriedTypesRunsAgain(t *testing.T) {
	pg := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if pg == "" {
		t.Skip("needs BROKOLI_TEST_POSTGRES_URL")
	}
	db := openFor(t, pg)
	for _, table := range []string{"rerun_typed_src", "rerun_typed_dst"} {
		_, _ = db.Exec("DROP TABLE IF EXISTS " + table)
		t.Cleanup(func() { _, _ = db.Exec("DROP TABLE IF EXISTS " + table) })
	}
	if _, err := db.Exec("CREATE TABLE rerun_typed_src (id bigint NOT NULL, amount numeric(12,2))"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO rerun_typed_src VALUES (1, 2.50), (2, 3.75)"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"true", "false"} {
		t.Setenv("BROKOLI_SINK_COPY", path)
		for run := 1; run <= 2; run++ {
			if r := runSourceToSink(t, pg, "SELECT id, amount FROM rerun_typed_src", pg, "rerun_typed_dst", nil); r.Status != models.RunStatusSuccess {
				t.Fatalf("BROKOLI_SINK_COPY=%s run %d failed: %s", path, run, r.Error)
			}
		}
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM rerun_typed_dst").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 8 {
		t.Errorf("%d rows after four appends of 2, want 8", n)
	}
	// The carried type was used, so this really went through that renderer.
	var typ string
	if err := db.QueryRow("SELECT data_type FROM information_schema.columns WHERE table_name = 'rerun_typed_dst' AND column_name = 'amount'").Scan(&typ); err != nil {
		t.Fatal(err)
	}
	if typ != "numeric" {
		t.Errorf("amount created as %s, want numeric from the carried type", typ)
	}
}
