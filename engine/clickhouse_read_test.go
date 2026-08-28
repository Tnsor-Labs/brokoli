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

// ADR-027 phase 1: ClickHouse is readable, and only readable.
//
// Three claims, each against the live server: a source_db reads real rows
// with real value types; a migration OUT of ClickHouse carries the columns'
// declared types to the destination (#360's rule, through the TypeReader);
// and every write path refuses by name until phase 2 earns it.

func clickhouseSeededTable(t *testing.T) string {
	t.Helper()
	dsn := clickhouseTestDSN(t)
	db := openFor(t, dsn)
	db.Exec("DROP TABLE IF EXISTS phase1_src")
	if _, err := db.Exec(`CREATE TABLE phase1_src (
		id UInt32,
		city Nullable(String),
		amount Decimal(12, 2),
		seen DateTime64(3, 'UTC'),
		flag Bool
	) ENGINE = MergeTree ORDER BY id`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec("DROP TABLE IF EXISTS phase1_src") })
	// 4000000000 is past int32: only the UInt32 -> 64-bit widening rule
	// carries it, so the migrate test below cannot pass by accident.
	if _, err := db.Exec(`INSERT INTO phase1_src VALUES
		(4000000000, 'Lisbon', 10.25, '2026-08-28 10:00:00.123', true),
		(2, NULL, 3.50, '2026-08-28 11:00:00.000', false)`); err != nil {
		t.Fatal(err)
	}
	return dsn
}

func TestClickHouseSourceDBReads(t *testing.T) {
	dsn := clickhouseSeededTable(t)

	dir := t.TempDir()
	st, err := store.NewSQLiteStore(filepath.Join(dir, "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	eng := drainEngineOnCleanup(t, NewEngine(st))

	p := &models.Pipeline{
		ID: "ch-read", Name: "ch read", Enabled: true,
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceDB, Name: "S",
				Config: map[string]interface{}{"uri": dsn, "query": "SELECT id, city, amount, flag FROM phase1_src ORDER BY id"}},
			{ID: "sink", Type: models.NodeTypeSinkFile, Name: "F",
				Config: map[string]interface{}{"path": filepath.Join(dir, "out.csv"), "format": "csv"}},
		},
		Edges:     []models.Edge{{From: "src", To: "sink"}},
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
		t.Fatalf("run failed: %s", run.Error)
	}

	out, err := os.ReadFile(filepath.Join(dir, "out.csv"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(out)
	for _, want := range []string{"Lisbon", "4000000000", "10.25"} {
		if !strings.Contains(content, want) {
			t.Errorf("output missing %q:\n%s", want, content)
		}
	}
}

// The reader's payoff: a migration out of ClickHouse creates its Postgres
// destination from the columns' declared types. Each assertion maps to a
// row of the canonical-type table and would fail under value inference --
// the UInt32 sample is past int32 but inference would still call the
// Decimal a float and the DateTime text.
func TestMigrateOutOfClickHouseCarriesTypes(t *testing.T) {
	pg := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if pg == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	dsn := clickhouseSeededTable(t)
	pdb := openFor(t, pg)
	pdb.Exec("DROP TABLE IF EXISTS ch_mig_dst")
	t.Cleanup(func() { pdb.Exec("DROP TABLE IF EXISTS ch_mig_dst") })

	run := runMigrate(t, dsn,
		"SELECT id, city, amount, seen, flag FROM phase1_src", pg, "ch_mig_dst")
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("migrate failed: %s", run.Error)
	}

	for _, tc := range []struct{ column, want string }{
		// UInt32 widens to 64 bits; INTEGER would reject 4000000000.
		{"id", "bigint"},
		{"city", "text"},
		{"amount", "numeric(12,2)"},
		// A ClickHouse DateTime is an epoch instant: timestamptz, not text.
		{"seen", "timestamp with time zone"},
		{"flag", "boolean"},
	} {
		if got := pgColumnType(t, pg, "ch_mig_dst", tc.column); got != tc.want {
			t.Errorf("%s created as %s, want %s", tc.column, got, tc.want)
		}
	}

	var id int64
	var amount string
	if err := pdb.QueryRow(
		"SELECT id, amount::text FROM ch_mig_dst WHERE id > 2").Scan(&id, &amount); err != nil {
		t.Fatal(err)
	}
	if id != 4000000000 {
		t.Errorf("id = %d, want 4000000000 intact", id)
	}
	if amount != "10.25" {
		t.Errorf("amount = %q, want exact 10.25", amount)
	}
	// The NULL city survived as NULL, not as an empty string.
	var nullCities int
	if err := pdb.QueryRow("SELECT count(*) FROM ch_mig_dst WHERE city IS NULL").Scan(&nullCities); err != nil {
		t.Fatal(err)
	}
	if nullCities != 1 {
		t.Errorf("null cities = %d, want 1", nullCities)
	}
}

// Phase 2 narrowed the refusal deliberately: append is earned (the
// equivalence tests in clickhouse_append_test.go are the proof), and the
// modes that are not stay refused by name -- overwrite until phase 3,
// upsert for good, with the refusal explaining ReplacingMergeTree rather
// than counterfeiting it.
func TestClickHouseWriteModesAreGatedByName(t *testing.T) {
	dsn := clickhouseTestDSN(t)
	pg := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if pg == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}

	pdb := openFor(t, pg)
	pdb.Exec("DROP TABLE IF EXISTS ch_gate_src")
	if _, err := pdb.Exec("CREATE TABLE ch_gate_src (id bigint)"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pdb.Exec("DROP TABLE IF EXISTS ch_gate_src") })
	if _, err := pdb.Exec("INSERT INTO ch_gate_src VALUES (1)"); err != nil {
		t.Fatal(err)
	}

	run := func(mode string, createTable bool) *models.Run {
		t.Helper()
		dir := t.TempDir()
		st, err := store.NewSQLiteStore(filepath.Join(dir, "g.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
		eng := drainEngineOnCleanup(t, NewEngine(st))
		cfg := map[string]interface{}{"uri": dsn, "table": "ch_gate_dst", "mode": mode, "create_table": createTable}
		if mode == "upsert" {
			cfg["key_columns"] = []interface{}{"id"}
		}
		p := &models.Pipeline{
			ID: "chg-" + mode, Name: mode, Enabled: true,
			Nodes: []models.Node{
				{ID: "src", Type: models.NodeTypeSourceDB, Name: "S",
					Config: map[string]interface{}{"uri": pg, "query": "SELECT id FROM ch_gate_src"}},
				{ID: "sink", Type: models.NodeTypeSinkDB, Name: "K", Config: cfg},
			},
			Edges:     []models.Edge{{From: "src", To: "sink"}},
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}
		if err := st.CreatePipeline(p); err != nil {
			t.Fatal(err)
		}
		r, err := eng.RunPipeline(p.ID)
		if err != nil {
			return &models.Run{Status: models.RunStatusFailed, Error: err.Error()}
		}
		return r
	}

	cdb := openFor(t, dsn)
	cdb.Exec("DROP TABLE IF EXISTS ch_gate_dst")
	t.Cleanup(func() { cdb.Exec("DROP TABLE IF EXISTS ch_gate_dst") })

	// Append works, end to end, table created on the way.
	if r := run("append", true); r.Status != models.RunStatusSuccess {
		t.Fatalf("append should be earned since phase 2, got: %s", r.Error)
	}

	// Overwrite works since phase 3, and replaces: the append above put
	// one row in; the overwrite leaves exactly one row, not two.
	if r := run("overwrite", false); r.Status != models.RunStatusSuccess {
		t.Fatalf("overwrite should be earned since phase 3, got: %s", r.Error)
	}
	cdb2 := openFor(t, dsn)
	var n uint64
	if err := cdb2.QueryRow("SELECT count() FROM ch_gate_dst").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("after overwrite the table holds %d rows, want 1 -- it must replace, not append", n)
	}

	// Upsert: refused for good, pointing at what actually exists.
	if r := run("upsert", false); r.Status == models.RunStatusSuccess {
		t.Fatal("upsert must be refused: ClickHouse has no synchronous merge")
	} else if !strings.Contains(r.Error, "ReplacingMergeTree") {
		t.Errorf("the upsert refusal should explain the native alternative, got: %s", r.Error)
	}
}

// Pushdown cannot form a ClickHouse segment: the dialect claims no
// Addresser, so sameServer never answers yes. Asserted at the engine level
// so the structural refusal is a fact of this package too.
func TestClickHousePushdownIsStructurallyOff(t *testing.T) {
	uri := "clickhouse://u:p@h:9000/db"
	if sameServer("clickhouse", uri, uri) {
		t.Fatal("sameServer answered yes for clickhouse; pushdown must stay refused until " +
			"the differential corpus exists (ADR-027 phase 3+)")
	}
}

// The hostile-credential proof for the URL-form DSN: clickhouse-go parses
// its DSN with url.Parse and unescapes userinfo, so percent-encoding is the
// correct choice in BuildURI -- the opposite of MySQL's verbatim DSN. This
// creates a real user with a hostile password on the live server and
// authenticates as it through the built URI.
func TestClickHouseHostileCredentialsRoundTrip(t *testing.T) {
	dsn := clickhouseTestDSN(t)
	db := openFor(t, dsn)

	const hostilePass = `zz@ch:pw/with#hash%and space`
	db.Exec("DROP USER IF EXISTS brokoli_hostile")
	if _, err := db.Exec(fmt.Sprintf(
		"CREATE USER brokoli_hostile IDENTIFIED BY '%s'",
		strings.ReplaceAll(hostilePass, "'", "''"))); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec("DROP USER IF EXISTS brokoli_hostile") })
	if _, err := db.Exec("GRANT SELECT ON " + clickhouseDBName(t, dsn) + ".* TO brokoli_hostile"); err != nil {
		t.Fatal(err)
	}

	conn := &models.Connection{
		ConnID: "chx", Type: models.ConnTypeClickHouse,
		Host: clickhouseHost(t, dsn), Port: clickhousePort(t, dsn),
		Schema: clickhouseDBName(t, dsn),
		Login:  "brokoli_hostile", Password: hostilePass,
	}
	if !conn.BuildsURI() {
		t.Fatal("clickhouse connections must build URIs")
	}
	hostileDB := openFor(t, conn.BuildURI())
	var one int
	if err := hostileDB.QueryRow("SELECT 1").Scan(&one); err != nil {
		t.Fatalf("authenticating with a hostile password through BuildURI: %v", err)
	}
}

// Small helpers to take the compose DSN apart for the connection-record
// test above, without a URL parser hiding what the test depends on.
func clickhouseHost(t *testing.T, dsn string) string {
	t.Helper()
	hostPort := clickhouseHostPort(t, dsn)
	return hostPort[:strings.LastIndex(hostPort, ":")]
}

func clickhousePort(t *testing.T, dsn string) int {
	t.Helper()
	hostPort := clickhouseHostPort(t, dsn)
	var port int
	fmt.Sscanf(hostPort[strings.LastIndex(hostPort, ":")+1:], "%d", &port)
	return port
}

func clickhouseHostPort(t *testing.T, dsn string) string {
	t.Helper()
	rest := strings.TrimPrefix(dsn, "clickhouse://")
	if at := strings.LastIndex(rest, "@"); at >= 0 {
		rest = rest[at+1:]
	}
	if slash := strings.Index(rest, "/"); slash >= 0 {
		rest = rest[:slash]
	}
	return rest
}

func clickhouseDBName(t *testing.T, dsn string) string {
	t.Helper()
	rest := strings.TrimPrefix(dsn, "clickhouse://")
	if slash := strings.Index(rest, "/"); slash >= 0 {
		name := rest[slash+1:]
		if q := strings.Index(name, "?"); q >= 0 {
			name = name[:q]
		}
		return name
	}
	return "default"
}
