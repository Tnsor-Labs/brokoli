package engine

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Both write paths must make the same choice, or which statement clears
// the table depends on whether COPY happened to be eligible.
func TestClearTablePerDialect(t *testing.T) {
	cases := []struct {
		dialect  string
		truncate bool
		want     string
	}{
		{"postgres", false, `DELETE FROM "t"`},
		{"postgres", true, `TRUNCATE TABLE "t"`},
		// MySQL's TRUNCATE implicitly commits, which would break
		// overwrite's one-transaction contract, so the request degrades
		// to the transactional statement. TestMySQLTruncateWouldBreakAtomicity
		// demonstrates the behaviour this avoids against a live server.
		{"mysql", true, "DELETE FROM `t`"},
		{"sqlserver", true, `TRUNCATE TABLE [t]`},
		// SQLite has no TRUNCATE; its planner already turns a WHERE-less
		// DELETE into the same whole-table drop.
		{"sqlite", true, `DELETE FROM "t"`},
		{"generic", true, `DELETE FROM "t"`},
	}
	for _, c := range cases {
		got := getDialect(c.dialect).clearTable("t", c.truncate)
		if got != c.want {
			t.Errorf("%s truncate=%v: got %q, want %q", c.dialect, c.truncate, got, c.want)
		}
	}
}

// The generated SQL reflects the option, and defaults to DELETE.
func TestGenerateSQLHonoursTruncate(t *testing.T) {
	ds := &common.DataSet{Columns: []string{"id"}, Rows: []common.DataRow{{"id": 1}}}

	plain, err := GenerateSQL(SQLGenConfig{Dialect: "postgres", Table: "t", Mode: ModeOverwrite, BatchSize: 10}, ds)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plain, "DELETE FROM") || strings.Contains(plain, "TRUNCATE") {
		t.Errorf("default should DELETE:\n%s", plain)
	}

	trunc, err := GenerateSQL(SQLGenConfig{Dialect: "postgres", Table: "t", Mode: ModeOverwrite, BatchSize: 10, Truncate: true}, ds)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(trunc, "TRUNCATE TABLE") || strings.Contains(trunc, "DELETE FROM") {
		t.Errorf("truncate=true should TRUNCATE:\n%s", trunc)
	}

	// An append never clears the table, whatever the option says.
	app, err := GenerateSQL(SQLGenConfig{Dialect: "postgres", Table: "t", Mode: ModeAppend, BatchSize: 10, Truncate: true}, ds)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(app, "TRUNCATE") || strings.Contains(app, "DELETE FROM") {
		t.Errorf("append must not clear the table:\n%s", app)
	}
}

func TestSinkStreamConfigCarriesTruncate(t *testing.T) {
	node := models.Node{Type: models.NodeTypeSinkDB, Config: map[string]interface{}{
		"uri": "postgres://u:p@h/db", "table": "t", "mode": "overwrite", "truncate": true,
	}}
	_, cfg, ok := sinkStreamConfig(node)
	if !ok || !cfg.Truncate {
		t.Fatalf("truncate should reach the streamed sink: ok=%v cfg=%+v", ok, cfg)
	}
}

// The point of the option, against a real server: the same overwrite
// repeated leaves no dead tuples and does not grow the table.
func TestTruncateOverwriteLeavesNoBloat(t *testing.T) {
	uri := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if uri == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	db, err := sql.Open("pgx", uri)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	rows := func(n int) *common.DataSet {
		ds := &common.DataSet{Columns: []string{"id", "payload"}}
		for i := 0; i < n; i++ {
			ds.Rows = append(ds.Rows, common.DataRow{"id": i, "payload": strings.Repeat("x", 200)})
		}
		return ds
	}

	measure := func(t *testing.T, table string, truncate bool) int64 {
		t.Helper()
		if _, err := db.Exec(`DROP TABLE IF EXISTS ` + table); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`CREATE TABLE ` + table + ` (id bigint PRIMARY KEY, payload text)`); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = db.Exec(`DROP TABLE IF EXISTS ` + table) })

		cfg := SQLGenConfig{Dialect: "postgres", Table: table, Mode: ModeOverwrite, Truncate: truncate}
		for i := 0; i < 6; i++ {
			if _, err := copyBatchesToPostgres(context.Background(), uri, cfg,
				[]string{"id", "payload"}, oneShot(rows(5000))); err != nil {
				t.Fatalf("overwrite %d: %v", i, err)
			}
		}
		var dead int64
		if err := db.QueryRow(
			`SELECT COALESCE(n_dead_tup,0) FROM pg_stat_user_tables WHERE relname = $1`,
			strings.TrimPrefix(table, "public."),
		).Scan(&dead); err != nil {
			t.Fatal(err)
		}
		// The contents must be right either way.
		var live int
		if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&live); err != nil {
			t.Fatal(err)
		}
		if live != 5000 {
			t.Fatalf("%s: %d rows after overwrite, want 5000", table, live)
		}
		return dead
	}

	withDelete := measure(t, "truncate_test_delete", false)
	withTruncate := measure(t, "truncate_test_truncate", true)
	t.Logf("dead tuples after 6 overwrites of 5000 rows: DELETE=%d TRUNCATE=%d", withDelete, withTruncate)

	if withTruncate != 0 {
		t.Errorf("TRUNCATE should leave no dead tuples, got %d", withTruncate)
	}
	if withDelete <= withTruncate {
		t.Errorf("DELETE should leave the bloat this option exists to avoid: DELETE=%d TRUNCATE=%d", withDelete, withTruncate)
	}
}
