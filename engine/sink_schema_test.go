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
	"github.com/Tnsor-Labs/brokoli/store"
)

// #363: a sink_db creating its table must use the columns' real types, not
// types guessed from a sample of the values that happen to be flowing.
//
// These run against live servers on purpose. The defect is in what the
// destination server ends up declaring and then accepting: a bigint whose
// sample fits in an int32 produced an INTEGER column, and the load failed on
// the first value above 2^31 -- after the table existed. Generated DDL
// inspected in a unit test looks perfectly fine in exactly that case, which
// is why the assertions here read information_schema and then push a value
// through.

// runSourceToSink drives source_db -> [transform] -> sink_db(create_table).
// rules is optional; nil means no transform node at all.
func runSourceToSink(
	t *testing.T,
	srcURI, srcQuery, destURI, destTable string,
	rules []map[string]interface{},
) *models.Run {
	t.Helper()
	dir := t.TempDir()
	st, err := store.NewSQLiteStore(filepath.Join(dir, "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	eng := drainEngineOnCleanup(t, NewEngine(st))

	nodes := []models.Node{{
		ID: "src", Type: models.NodeTypeSourceDB, Name: "Source",
		Config: map[string]interface{}{"uri": srcURI, "query": srcQuery},
	}}
	edges := []models.Edge{}
	last := "src"
	if rules != nil {
		nodes = append(nodes, models.Node{
			ID: "xf", Type: models.NodeTypeTransform, Name: "Transform",
			Config: map[string]interface{}{"rules": rules},
		})
		edges = append(edges, models.Edge{From: "src", To: "xf"})
		last = "xf"
	}
	nodes = append(nodes, models.Node{
		ID: "sink", Type: models.NodeTypeSinkDB, Name: "Sink",
		Config: map[string]interface{}{
			"uri": destURI, "table": destTable,
			"create_table": true, "mode": "append",
		},
	})
	edges = append(edges, models.Edge{From: last, To: "sink"})

	p := &models.Pipeline{
		ID: "sink-" + destTable, Name: destTable, Enabled: true,
		Nodes: nodes, Edges: edges,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := st.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	run, err := eng.RunPipeline(p.ID)
	if err != nil {
		// A refusal surfaces as a run error; hand it back so refusal
		// tests can assert on it.
		return &models.Run{Status: models.RunStatusFailed, Error: err.Error()}
	}
	return run
}

// pgColumnType reports a Postgres column's declared type, rendered the way
// the catalog spells it including precision and scale.
func pgColumnType(t *testing.T, uri, table, column string) string {
	t.Helper()
	db := openFor(t, uri)
	var dataType string
	var precision, scale *int
	err := db.QueryRow(`
		SELECT data_type, numeric_precision, numeric_scale
		FROM information_schema.columns
		WHERE table_name = $1 AND column_name = $2`, table, column).Scan(&dataType, &precision, &scale)
	if err != nil {
		t.Fatalf("read type of %s.%s: %v", table, column, err)
	}
	if dataType == "numeric" && precision != nil && scale != nil {
		return fmt.Sprintf("numeric(%d,%d)", *precision, *scale)
	}
	return dataType
}

// The headline: types that value inference gets wrong, carried intact.
func TestSinkCreateTableCarriesTheSourcesTypes(t *testing.T) {
	pg, _ := bothBackends(t)
	pdb := openFor(t, pg)

	pdb.Exec("DROP TABLE IF EXISTS sink_types_src")
	if _, err := pdb.Exec(`CREATE TABLE sink_types_src (
		id bigint NOT NULL,
		amount numeric(12,2),
		seen_at timestamptz,
		is_admin boolean,
		city text)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pdb.Exec("DROP TABLE IF EXISTS sink_types_src") })

	// Every value here is one inference reads as something narrower than
	// the column really is: a bigint that fits in an int32, an exact
	// decimal that parses as a float, an instant that parses as a date.
	if _, err := pdb.Exec(
		`INSERT INTO sink_types_src VALUES (7, 10.25, now(), true, 'Lisbon')`); err != nil {
		t.Fatal(err)
	}

	pdb.Exec("DROP TABLE IF EXISTS sink_types_dst")
	t.Cleanup(func() { pdb.Exec("DROP TABLE IF EXISTS sink_types_dst") })

	run := runSourceToSink(t, pg,
		"SELECT id, amount, seen_at, is_admin, city FROM sink_types_src",
		pg, "sink_types_dst", nil)
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run failed: %s", run.Error)
	}

	for _, tc := range []struct{ column, want string }{
		{"id", "bigint"},
		{"amount", "numeric(12,2)"},
		{"seen_at", "timestamp with time zone"},
		{"is_admin", "boolean"},
		{"city", "text"},
	} {
		if got := pgColumnType(t, pg, "sink_types_dst", tc.column); got != tc.want {
			t.Errorf("%s was created as %s, want %s", tc.column, got, tc.want)
		}
	}

	// Nullability is deliberately NOT carried from a Postgres source, and
	// this pins that rather than leaving it to chance. pgx does not report
	// it -- ColumnType.Nullable() answers ok=false -- and #362 chose the
	// permissive answer for exactly that case: a NOT NULL the source did
	// not actually have would fail the load on the first null. A driver
	// that does report it (go-sql-driver does) carries it through.
	db := openFor(t, pg)
	var nullable string
	if err := db.QueryRow(`
		SELECT is_nullable FROM information_schema.columns
		WHERE table_name = 'sink_types_dst' AND column_name = 'id'`).Scan(&nullable); err != nil {
		t.Fatal(err)
	}
	if nullable != "YES" {
		t.Errorf("id is_nullable = %q, want YES: a driver that cannot report nullability "+
			"must not have one invented for it", nullable)
	}
}

// The consequence, rather than the declaration: a value that the inferred
// table could not have held.
//
// Without a carried type, `id` samples as an int and is created INTEGER, so
// this row fails to load with "integer out of range" -- after the table
// exists. This is the assertion that makes the test about data rather than
// about DDL text.
func TestSinkCreateTableHoldsAValueInferenceWouldHaveRejected(t *testing.T) {
	pg, _ := bothBackends(t)
	pdb := openFor(t, pg)

	pdb.Exec("DROP TABLE IF EXISTS sink_big_src")
	if _, err := pdb.Exec("CREATE TABLE sink_big_src (id bigint)"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pdb.Exec("DROP TABLE IF EXISTS sink_big_src") })
	// 2^53+1: past int32, and past float64's exact range, so a round trip
	// through a float would corrupt it too.
	const big = 9007199254740993
	if _, err := pdb.Exec("INSERT INTO sink_big_src VALUES ($1)", int64(big)); err != nil {
		t.Fatal(err)
	}

	pdb.Exec("DROP TABLE IF EXISTS sink_big_dst")
	t.Cleanup(func() { pdb.Exec("DROP TABLE IF EXISTS sink_big_dst") })

	run := runSourceToSink(t, pg, "SELECT id FROM sink_big_src", pg, "sink_big_dst", nil)
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run failed: %s", run.Error)
	}

	var got int64
	if err := pdb.QueryRow("SELECT id FROM sink_big_dst").Scan(&got); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got != big {
		t.Errorf("id round-tripped as %d, want %d", got, big)
	}
}

// The per-column claim, which is what makes this usable on real pipelines:
// a transform that rewrites one column must not cost the other four their
// types.
func TestATransformedColumnDegradesAlone(t *testing.T) {
	pg, _ := bothBackends(t)
	pdb := openFor(t, pg)

	pdb.Exec("DROP TABLE IF EXISTS sink_mix_src")
	if _, err := pdb.Exec(`CREATE TABLE sink_mix_src (
		id bigint, amount numeric(12,2), seen_at timestamptz, city text)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pdb.Exec("DROP TABLE IF EXISTS sink_mix_src") })
	if _, err := pdb.Exec(
		"INSERT INTO sink_mix_src VALUES (7, 10.25, now(), 'lisbon')"); err != nil {
		t.Fatal(err)
	}

	pdb.Exec("DROP TABLE IF EXISTS sink_mix_dst")
	t.Cleanup(func() { pdb.Exec("DROP TABLE IF EXISTS sink_mix_dst") })

	run := runSourceToSink(t, pg,
		"SELECT id, amount, seen_at, city FROM sink_mix_src",
		pg, "sink_mix_dst",
		[]map[string]interface{}{
			{"type": "apply_function", "column": "city", "function": "upper"},
		})
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run failed: %s", run.Error)
	}

	// Untouched columns keep everything.
	for _, tc := range []struct{ column, want string }{
		{"id", "bigint"},
		{"amount", "numeric(12,2)"},
		{"seen_at", "timestamp with time zone"},
	} {
		if got := pgColumnType(t, pg, "sink_mix_dst", tc.column); got != tc.want {
			t.Errorf("%s was created as %s, want %s -- one rewritten column cost the others their types",
				tc.column, got, tc.want)
		}
	}
	// The rewritten one falls back to inference, which is the honest
	// answer for a column whose values a rule replaced.
	if got := pgColumnType(t, pg, "sink_mix_dst", "city"); got != "text" {
		t.Errorf("city was created as %s, want text", got)
	}
	var city string
	if err := pdb.QueryRow("SELECT city FROM sink_mix_dst").Scan(&city); err != nil {
		t.Fatal(err)
	}
	if city != "LISBON" {
		t.Errorf("the transform did not run: city = %q", city)
	}
}

// An aggregate rewrites the whole shape, and the group key is the part that
// keeps its type. This is the aggregate row of the table, end to end.
func TestAggregateKeepsTheGroupKeysType(t *testing.T) {
	pg, _ := bothBackends(t)
	pdb := openFor(t, pg)

	pdb.Exec("DROP TABLE IF EXISTS sink_agg_src")
	if _, err := pdb.Exec("CREATE TABLE sink_agg_src (account bigint, amount numeric(12,2))"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pdb.Exec("DROP TABLE IF EXISTS sink_agg_src") })
	if _, err := pdb.Exec(
		"INSERT INTO sink_agg_src VALUES (4294967296, 10.25), (4294967296, 5.75)"); err != nil {
		t.Fatal(err)
	}

	pdb.Exec("DROP TABLE IF EXISTS sink_agg_dst")
	t.Cleanup(func() { pdb.Exec("DROP TABLE IF EXISTS sink_agg_dst") })

	run := runSourceToSink(t, pg,
		"SELECT account, amount FROM sink_agg_src",
		pg, "sink_agg_dst",
		[]map[string]interface{}{{
			"type":     "aggregate",
			"group_by": []interface{}{"account"},
			"agg_fields": []interface{}{
				map[string]interface{}{"column": "amount", "function": "sum"},
			},
		}})
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run failed: %s", run.Error)
	}

	// The group key is copied through, so it stays a bigint -- and the
	// account number here is above 2^31, so an inferred INTEGER would have
	// failed the load outright.
	if got := pgColumnType(t, pg, "sink_agg_dst", "account"); got != "bigint" {
		t.Errorf("group key created as %s, want bigint", got)
	}
	var account int64
	if err := pdb.QueryRow("SELECT account FROM sink_agg_dst").Scan(&account); err != nil {
		t.Fatal(err)
	}
	if account != 4294967296 {
		t.Errorf("account = %d, want 4294967296", account)
	}
}

// Cross-backend, which is the real test of whether this rides on the
// dialect abstraction or is a Postgres special case.
func TestSinkCarriesTypesPostgresToMySQL(t *testing.T) {
	pg, my := bothBackends(t)
	pdb, mdb := openFor(t, pg), openFor(t, my)

	pdb.Exec("DROP TABLE IF EXISTS sink_x_src")
	if _, err := pdb.Exec("CREATE TABLE sink_x_src (id bigint, amount numeric(12,2))"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pdb.Exec("DROP TABLE IF EXISTS sink_x_src") })
	const big = 9007199254740993
	if _, err := pdb.Exec("INSERT INTO sink_x_src VALUES ($1, 10.25)", int64(big)); err != nil {
		t.Fatal(err)
	}

	mdb.Exec("DROP TABLE IF EXISTS sink_x_dst")
	t.Cleanup(func() { mdb.Exec("DROP TABLE IF EXISTS sink_x_dst") })

	run := runSourceToSink(t, pg, "SELECT id, amount FROM sink_x_src", my, "sink_x_dst", nil)
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run failed: %s", run.Error)
	}

	var columnType string
	if err := mdb.QueryRow(`
		SELECT COLUMN_TYPE FROM information_schema.columns
		WHERE table_schema = DATABASE() AND table_name = 'sink_x_dst' AND column_name = 'id'`,
	).Scan(&columnType); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(columnType), "bigint") {
		t.Errorf("id created as %s on MySQL, want a bigint", columnType)
	}
	var got int64
	if err := mdb.QueryRow("SELECT id FROM sink_x_dst").Scan(&got); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got != big {
		t.Errorf("id round-tripped as %d, want %d", got, big)
	}
}

// The one refusal kept: a type we actually know and the destination cannot
// hold. Inferring here would substitute something lossy for a known answer,
// silently, after the table exists.
func TestSinkRefusesATypeTheDestinationCannotHold(t *testing.T) {
	pg, my := bothBackends(t)
	pdb, mdb := openFor(t, pg), openFor(t, my)

	pdb.Exec("DROP TABLE IF EXISTS sink_lossy_src")
	if _, err := pdb.Exec("CREATE TABLE sink_lossy_src (id bigint, seen_at timestamptz)"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pdb.Exec("DROP TABLE IF EXISTS sink_lossy_src") })
	if _, err := pdb.Exec("INSERT INTO sink_lossy_src VALUES (1, now())"); err != nil {
		t.Fatal(err)
	}

	mdb.Exec("DROP TABLE IF EXISTS sink_lossy_dst")
	t.Cleanup(func() { mdb.Exec("DROP TABLE IF EXISTS sink_lossy_dst") })

	run := runSourceToSink(t, pg, "SELECT id, seen_at FROM sink_lossy_src", my, "sink_lossy_dst", nil)
	if run.Status == models.RunStatusSuccess {
		t.Fatal("a timestamptz has no faithful MySQL type; the sink must refuse rather than drop the zone")
	}
	if !strings.Contains(run.Error, "seen_at") {
		t.Errorf("the refusal must name the column, got: %s", run.Error)
	}
	if !strings.Contains(run.Error, "timestamptz") {
		t.Errorf("the refusal must name the type, got: %s", run.Error)
	}

	var n int
	if err := mdb.QueryRow(
		"SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = 'sink_lossy_dst'",
	).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("the destination table was created despite the refusal")
	}
}

// What carrying a schema costs a real run.
//
// Two things were added to the create_table path: one probe per source (a
// LIMIT 0 round trip, already measured against value-sniffing by
// BenchmarkSchemaDiscovery), and the rule fold that carries it through the
// transforms. The fold is what is new here, and the full run is the
// denominator that says whether either is worth thinking about.
//
//	BROKOLI_TEST_POSTGRES_URL=... go test ./engine/ -run xxx \
//	  -bench BenchmarkSchemaCarry -benchtime 20x
func BenchmarkSchemaCarry(b *testing.B) {
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

	db.Exec("DROP TABLE IF EXISTS bench_carry_src")
	if _, err := db.Exec(`CREATE TABLE bench_carry_src (
		id bigint, city text, amount numeric(12,2), seen_at timestamptz)`); err != nil {
		b.Fatal(err)
	}
	defer db.Exec("DROP TABLE IF EXISTS bench_carry_src")
	for i := 0; i < 1000; i++ {
		if _, err := db.Exec(
			"INSERT INTO bench_carry_src VALUES ($1, $2, $3, now())",
			int64(i), fmt.Sprintf("city-%d", i%20), 10.25); err != nil {
			b.Fatal(err)
		}
	}
	const q = "SELECT id, city, amount, seen_at FROM bench_carry_src"

	// A rule list of the shape these pipelines actually carry.
	rules := []TransformRule{
		{Type: "filter_rows", Condition: "id > 10"},
		{Type: "rename_columns", Mapping: map[string]string{"city": "town"}},
		{Type: "apply_function", Column: "town", Function: "upper"},
		{Type: "deduplicate", Columns: []string{"town"}},
		{Type: "drop_columns", Columns: []string{"seen_at"}},
	}
	source, _, ok := sourceColumnTypes(context.Background(), uri, q)
	if !ok {
		b.Fatal("could not read source types")
	}

	b.Run("propagate_rules_through_schema", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if out := applyRulesToSchema(rules, columnSchema(source)); len(out) == 0 {
				b.Fatal("propagation lost every column")
			}
		}
	})

	b.Run("probe_one_source", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, _, ok := sourceColumnTypes(context.Background(), uri, q); !ok {
				b.Fatal("probe failed")
			}
		}
	})

	// The denominator: a whole run of the shape this feature is for.
	b.Run("full_run_source_to_create_table_sink", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			dest := fmt.Sprintf("bench_carry_dst_%d", i)
			run := benchSourceToSink(b, uri, q, uri, dest)
			db.Exec("DROP TABLE IF EXISTS " + dest)
			if run.Status != models.RunStatusSuccess {
				b.Fatalf("run failed: %s", run.Error)
			}
		}
	})
}

// benchSourceToSink is runSourceToSink for a benchmark, which cannot use the
// t.Cleanup-based helpers.
func benchSourceToSink(b *testing.B, srcURI, srcQuery, destURI, destTable string) *models.Run {
	b.Helper()
	dir := b.TempDir()
	st, err := store.NewSQLiteStore(filepath.Join(dir, "b.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer st.Close()
	eng := NewEngine(st)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := eng.Close(ctx); err != nil {
			b.Errorf("engine close: %v", err)
		}
	}()

	p := &models.Pipeline{
		ID: "bench-" + destTable, Name: destTable, Enabled: true,
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceDB,
				Config: map[string]interface{}{"uri": srcURI, "query": srcQuery}},
			{ID: "sink", Type: models.NodeTypeSinkDB,
				Config: map[string]interface{}{
					"uri": destURI, "table": destTable,
					"create_table": true, "mode": "append",
				}},
		},
		Edges:     []models.Edge{{From: "src", To: "sink"}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := st.CreatePipeline(p); err != nil {
		b.Fatal(err)
	}
	run, err := eng.RunPipeline(p.ID)
	if err != nil {
		b.Fatal(err)
	}
	return run
}
