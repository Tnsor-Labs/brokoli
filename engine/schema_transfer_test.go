package engine

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/dbdialect"
	"github.com/Tnsor-Labs/brokoli/store"
)

// #360: a migration must carry the source's real column types, not types
// guessed from a sample of its values.
//
// These run against live servers because the defect is in what the
// destination server accepts. A Postgres bigint became a MySQL INT and the
// run failed on the first value above 2^31 -- a fake would have reported
// success, since the generated DDL "looked" fine.

func bothBackends(t *testing.T) (pg, my string) {
	t.Helper()
	pg = os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	my = os.Getenv("BROKOLI_TEST_MYSQL_URL")
	if pg == "" || my == "" {
		t.Skip("needs BROKOLI_TEST_POSTGRES_URL and BROKOLI_TEST_MYSQL_URL")
	}
	return pg, my
}

func openFor(t *testing.T, uri string) *sql.DB {
	t.Helper()
	driver, dsn, err := DetectDriver(uri)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// runMigrate drives a real migrate node end to end.
func runMigrate(t *testing.T, srcURI, srcQuery, destURI, destTable string) *models.Run {
	t.Helper()
	dir := t.TempDir()
	st, err := store.NewSQLiteStore(filepath.Join(dir, "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	eng := drainEngineOnCleanup(t, NewEngine(st))

	p := &models.Pipeline{
		ID: "mig-" + destTable, Name: destTable, Enabled: true,
		Nodes: []models.Node{{
			ID: "mig", Type: models.NodeTypeMigrate, Name: "Mig",
			Config: map[string]interface{}{
				"source_uri": srcURI, "source_query": srcQuery,
				"dest_uri": destURI, "dest_table": destTable,
				"create_table": true, "mode": "append",
			},
		}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := st.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	run, err := eng.RunPipeline(p.ID)
	if err != nil {
		// A refusal surfaces as a run error; hand it back rather than
		// failing, so refusal tests can assert on it.
		return &models.Run{Status: models.RunStatusFailed, Error: err.Error()}
	}
	return run
}

// The headline case: a bigint above 2^31 used to fail the load because the
// destination had been created as INT. It must now survive intact.
func TestMigrationCarriesIntegerWidth(t *testing.T) {
	pg, my := bothBackends(t)
	pdb, mdb := openFor(t, pg), openFor(t, my)

	pdb.Exec("DROP TABLE IF EXISTS width_src")
	if _, err := pdb.Exec("CREATE TABLE width_src (id bigint, small smallint)"); err != nil {
		t.Fatal(err)
	}
	// 2^53+1: past int32 by a mile, and past float64's exact range too, so
	// this also catches a numeric round trip that goes through a float.
	const big = 9007199254740993
	if _, err := pdb.Exec("INSERT INTO width_src VALUES ($1, $2)", int64(big), 7); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pdb.Exec("DROP TABLE IF EXISTS width_src") })
	mdb.Exec("DROP TABLE IF EXISTS width_dst")
	t.Cleanup(func() { mdb.Exec("DROP TABLE IF EXISTS width_dst") })

	run := runMigrate(t, pg, "SELECT id, small FROM width_src", my, "width_dst")
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("migration failed: %s", run.Error)
	}

	var got int64
	if err := mdb.QueryRow("SELECT id FROM width_dst").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != big {
		t.Errorf("id = %d, want %d — the value did not survive the trip", got, big)
	}

	var col, typ string
	var rest [10]interface{}
	ptrs := []interface{}{&col, &typ}
	for i := range rest {
		ptrs = append(ptrs, &rest[i])
	}
	rows, err := mdb.Query("SHOW COLUMNS FROM width_dst")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	seen := map[string]string{}
	for rows.Next() {
		vals := make([]interface{}, len(cols))
		p := make([]interface{}, len(cols))
		for i := range vals {
			p[i] = &vals[i]
		}
		if err := rows.Scan(p...); err != nil {
			t.Fatal(err)
		}
		name := fmt.Sprintf("%s", vals[0].([]byte))
		seen[name] = strings.ToLower(fmt.Sprintf("%s", vals[1].([]byte)))
	}
	if !strings.HasPrefix(seen["id"], "bigint") {
		t.Errorf("id column is %q, want bigint — a narrower type cannot hold the source", seen["id"])
	}
	// Narrow stays narrow: widening everything to bigint would pass this
	// test's first half while wasting the destination's storage.
	if !strings.HasPrefix(seen["small"], "smallint") {
		t.Errorf("small column is %q, want smallint", seen["small"])
	}
}

// An exact decimal must stay exact. Rendered as a float, 12345678901.23
// comes back changed.
func TestMigrationKeepsDecimalExact(t *testing.T) {
	pg, my := bothBackends(t)
	pdb, mdb := openFor(t, pg), openFor(t, my)

	pdb.Exec("DROP TABLE IF EXISTS dec_src")
	if _, err := pdb.Exec("CREATE TABLE dec_src (amount numeric(20,4))"); err != nil {
		t.Fatal(err)
	}
	const exact = "12345678901.2345"
	if _, err := pdb.Exec("INSERT INTO dec_src VALUES ($1)", exact); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pdb.Exec("DROP TABLE IF EXISTS dec_src") })
	mdb.Exec("DROP TABLE IF EXISTS dec_dst")
	t.Cleanup(func() { mdb.Exec("DROP TABLE IF EXISTS dec_dst") })

	run := runMigrate(t, pg, "SELECT amount FROM dec_src", my, "dec_dst")
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("migration failed: %s", run.Error)
	}
	var got string
	if err := mdb.QueryRow("SELECT CAST(amount AS CHAR) FROM dec_dst").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != exact {
		t.Errorf("amount = %q, want %q — precision was lost", got, exact)
	}
}

// A type the destination cannot hold faithfully is refused BY NAME, before
// anything moves. MySQL has no zone-carrying timestamp, so a timestamptz
// must not quietly become a DATETIME with the zone dropped.
func TestMigrationRefusesLossyTypeByName(t *testing.T) {
	pg, my := bothBackends(t)
	pdb, mdb := openFor(t, pg), openFor(t, my)

	pdb.Exec("DROP TABLE IF EXISTS lossy_src")
	if _, err := pdb.Exec("CREATE TABLE lossy_src (id int, seen_at timestamptz)"); err != nil {
		t.Fatal(err)
	}
	if _, err := pdb.Exec("INSERT INTO lossy_src VALUES (1, now())"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pdb.Exec("DROP TABLE IF EXISTS lossy_src") })
	mdb.Exec("DROP TABLE IF EXISTS lossy_dst")
	t.Cleanup(func() { mdb.Exec("DROP TABLE IF EXISTS lossy_dst") })

	run := runMigrate(t, pg, "SELECT id, seen_at FROM lossy_src", my, "lossy_dst")
	if run.Status == models.RunStatusSuccess {
		t.Fatal("a timestamptz has no faithful MySQL type; the migration must refuse rather than drop the zone")
	}
	if !strings.Contains(run.Error, "seen_at") {
		t.Errorf("the refusal must name the column, got: %s", run.Error)
	}
	if !strings.Contains(run.Error, "timestamptz") {
		t.Errorf("the refusal must name the source type, got: %s", run.Error)
	}
	// And nothing was created: a refusal that leaves a half-built table
	// behind is not a refusal.
	var n int
	if err := mdb.QueryRow(
		"SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = 'lossy_dst'",
	).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("the destination table was created despite the refusal")
	}
}

// The other direction, which is the real test of whether this is an
// abstraction or a one-way special case.
func TestMigrationMySQLToPostgres(t *testing.T) {
	pg, my := bothBackends(t)
	pdb, mdb := openFor(t, pg), openFor(t, my)

	mdb.Exec("DROP TABLE IF EXISTS rev_src")
	if _, err := mdb.Exec(
		"CREATE TABLE rev_src (id BIGINT, label VARCHAR(64), amount DECIMAL(18,3), when_at DATETIME(6))",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := mdb.Exec(
		"INSERT INTO rev_src VALUES (9007199254740993, 'ok', 123456.789, '2026-08-25 12:34:56.123456')",
	); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mdb.Exec("DROP TABLE IF EXISTS rev_src") })
	pdb.Exec("DROP TABLE IF EXISTS rev_dst")
	t.Cleanup(func() { pdb.Exec("DROP TABLE IF EXISTS rev_dst") })

	run := runMigrate(t, my, "SELECT id, label, amount, when_at FROM rev_src", pg, "rev_dst")
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("migration failed: %s", run.Error)
	}

	var id int64
	var label, amount string
	if err := pdb.QueryRow("SELECT id, label, amount::text FROM rev_dst").Scan(&id, &label, &amount); err != nil {
		t.Fatal(err)
	}
	if id != 9007199254740993 {
		t.Errorf("id = %d, want 9007199254740993", id)
	}
	if amount != "123456.789" {
		t.Errorf("amount = %q, want 123456.789 — precision was lost", amount)
	}

	var typ string
	if err := pdb.QueryRow(
		"SELECT data_type FROM information_schema.columns WHERE table_name = 'rev_dst' AND column_name = 'id'",
	).Scan(&typ); err != nil {
		t.Fatal(err)
	}
	if typ != "bigint" {
		t.Errorf("id column is %q, want bigint", typ)
	}
}

// The canonical vocabulary itself, as a table so a change to it is a visible
// decision. Both directions for both backends.
func TestCanonicalTypeRoundTrip(t *testing.T) {
	pgd, _ := dbdialect.For("postgres")
	myd, _ := dbdialect.For("mysql")
	pgr := pgd.(dbdialect.TypeReader)
	myr := myd.(dbdialect.TypeReader)
	pgw := pgd.(dbdialect.TypeRenderer)
	myw := myd.(dbdialect.TypeRenderer)

	cases := []struct {
		srcName          string
		precision, scale int64
		precisionOK      bool
		wantClass        dbdialect.TypeClass
		wantBits         int
		wantMySQL        string
		wantMySQLOK      bool
	}{
		{"INT8", 0, 0, false, dbdialect.TypeInt, 64, "BIGINT", true},
		{"INT4", 0, 0, false, dbdialect.TypeInt, 32, "INT", true},
		{"INT2", 0, 0, false, dbdialect.TypeInt, 16, "SMALLINT", true},
		{"FLOAT8", 0, 0, false, dbdialect.TypeFloat, 64, "DOUBLE", true},
		{"NUMERIC", 20, 4, true, dbdialect.TypeDecimal, 0, "DECIMAL(20,4)", true},
		{"BOOL", 0, 0, false, dbdialect.TypeBool, 0, "BOOLEAN", true},
		{"TEXT", 0, 0, false, dbdialect.TypeText, 0, "TEXT", true},
		{"TIMESTAMP", 0, 0, false, dbdialect.TypeTimestamp, 0, "DATETIME(6)", true},
		// The two that must refuse rather than degrade.
		{"TIMESTAMPTZ", 0, 0, false, dbdialect.TypeTimestampTZ, 0, "", false},
		{"NUMERIC", 0, 0, false, dbdialect.TypeDecimal, 0, "", false},
	}
	for _, c := range cases {
		got := pgr.CanonicalType(c.srcName, c.precision, c.scale, c.precisionOK, 0, false, true)
		if got.Class != c.wantClass {
			t.Errorf("postgres %s -> %v, want %v", c.srcName, got.Class, c.wantClass)
		}
		if c.wantBits != 0 && got.Bits != c.wantBits {
			t.Errorf("postgres %s -> %d bits, want %d", c.srcName, got.Bits, c.wantBits)
		}
		ddl, ok := myw.DDLType(got)
		if ok != c.wantMySQLOK {
			t.Errorf("mysql DDL for %s: ok=%v, want %v (got %q)", c.srcName, ok, c.wantMySQLOK, ddl)
			continue
		}
		if ok && ddl != c.wantMySQL {
			t.Errorf("mysql DDL for %s = %q, want %q", c.srcName, ddl, c.wantMySQL)
		}
	}

	// An unsigned MySQL int holds values a signed one of that width cannot,
	// so it must widen rather than lose the top of the range.
	u := myr.CanonicalType("UNSIGNED INT", 0, 0, false, 0, false, true)
	if u.Bits != 64 {
		t.Errorf("UNSIGNED INT -> %d bits, want 64", u.Bits)
	}
	if ddl, ok := pgw.DDLType(u); !ok || ddl != "BIGINT" {
		t.Errorf("postgres DDL for UNSIGNED INT = %q ok=%v, want BIGINT", ddl, ok)
	}

	// An unknown name stays unknown and is never rendered.
	unk := pgr.CanonicalType("SOME_EXTENSION_TYPE", 0, 0, false, 0, false, true)
	if unk.Class != dbdialect.TypeUnknown {
		t.Errorf("unknown type classified as %v", unk.Class)
	}
	if _, ok := myw.DDLType(unk); ok {
		t.Error("an unknown type must not render as DDL")
	}
}

// Reading a source's types costs one round trip that returns no rows. This
// measures it against the value-sniffing it replaces, so the claim that the
// fix is cheap is a number rather than an assertion.
//
//	BROKOLI_TEST_POSTGRES_URL=... go test ./engine/ -run xxx \
//	  -bench BenchmarkSchemaDiscovery -benchtime 20x
func BenchmarkSchemaDiscovery(b *testing.B) {
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

	db.Exec("DROP TABLE IF EXISTS bench_schema_src")
	if _, err := db.Exec(`CREATE TABLE bench_schema_src (
		id bigint, name text, amount numeric(20,4), flag boolean,
		seen timestamp, tags text, score double precision, note text
	)`); err != nil {
		b.Fatal(err)
	}
	defer db.Exec("DROP TABLE IF EXISTS bench_schema_src")

	// A realistic sample for the inference path to chew on.
	rows := make([]common.DataRow, 0, 1000)
	for i := 0; i < 1000; i++ {
		rows = append(rows, common.DataRow{
			"id": int64(i), "name": fmt.Sprintf("name-%d", i),
			"amount": "12345.6789", "flag": i%2 == 0,
			"seen": "2026-08-25 12:00:00", "tags": "a,b,c",
			"score": 1.5, "note": "some note text",
		})
	}
	cols := []string{"id", "name", "amount", "flag", "seen", "tags", "score", "note"}
	const q = "SELECT id, name, amount, flag, seen, tags, score, note FROM bench_schema_src"

	b.Run("read_source_types", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			types, _, ok := sourceColumnTypes(context.Background(), uri, q)
			if !ok || len(types) != 8 {
				b.Fatalf("ok=%v n=%d", ok, len(types))
			}
		}
	})

	b.Run("infer_from_1000_values", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if got := inferTypes(cols, rows); len(got) != 8 {
				b.Fatalf("n=%d", len(got))
			}
		}
	})
}
