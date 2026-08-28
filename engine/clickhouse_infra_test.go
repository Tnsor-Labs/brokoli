package engine

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"
)

// ADR-027 phase 0: the infrastructure smoke test.
//
// There is no ClickHouse dialect yet -- no DetectDriver mapping, no
// connection type, nothing a pipeline can reach. What phase 0 delivers is
// everything later phases stand on: the pure-Go driver compiled into the
// CGO_ENABLED=0 build, the LTS container in docker-compose.test.yml, the
// env-gate variable, and CI running all of it. This test is how a broken
// piece of that stack fails loudly now instead of as a mystery inside
// phase 1's first real test.
//
// Deliberately speaks database/sql with a raw DSN rather than any Brokoli
// path: the moment a Brokoli path exists, it belongs to phase 1 and gets
// tests of its own.

// clickhouseTestDSN returns the phase-0 DSN, or skips.
func clickhouseTestDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("BROKOLI_TEST_CLICKHOUSE_URL")
	if dsn == "" {
		t.Skip("BROKOLI_TEST_CLICKHOUSE_URL not set")
	}
	return dsn
}

func TestClickHouseInfrastructure(t *testing.T) {
	dsn := clickhouseTestDSN(t)

	// The driver must be registered by the blank import in database.go --
	// this is the phase-0 contract, checked by name so a lost import is a
	// named failure rather than sql.Open's generic "unknown driver".
	registered := false
	for _, d := range sql.Drivers() {
		if d == "clickhouse" {
			registered = true
		}
	}
	if !registered {
		t.Fatalf("the clickhouse driver is not compiled in; drivers: %v", sql.Drivers())
	}

	db, err := sql.Open("clickhouse", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping: %v (is the compose clickhouse service up?)", err)
	}

	// A real query against the test database, not just a ping: the env
	// var's database segment has to name a database that exists, or every
	// phase-1 test inherits a confusing auth-or-catalog error.
	var version string
	if err := db.QueryRowContext(ctx, "SELECT version()").Scan(&version); err != nil {
		t.Fatalf("query: %v", err)
	}
	// Pinned to the LTS major the compose file pins, so a silent image
	// bump shows up here as a named difference rather than as phase-2
	// type-mapping tests failing on a behaviour that moved.
	if !strings.HasPrefix(version, "25.3.") {
		t.Errorf("server version = %q, want the 25.3 LTS the compose file pins -- "+
			"if the image was bumped deliberately, update both", version)
	}

	// Round-trip one value through a real table, so create/insert/select
	// permissions for the test user are proven now.
	if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS phase0_smoke"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		"CREATE TABLE phase0_smoke (id Int64, note String) ENGINE = MergeTree ORDER BY id"); err != nil {
		t.Fatalf("create: %v", err)
	}
	defer db.ExecContext(ctx, "DROP TABLE IF EXISTS phase0_smoke")
	if _, err := db.ExecContext(ctx,
		"INSERT INTO phase0_smoke VALUES (1, 'phase zero')"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var note string
	if err := db.QueryRowContext(ctx,
		"SELECT note FROM phase0_smoke WHERE id = 1").Scan(&note); err != nil {
		t.Fatalf("select: %v", err)
	}
	if note != "phase zero" {
		t.Errorf("round-trip = %q, want %q", note, "phase zero")
	}
}

// Phase 1 crossed the phase-0 scope boundary deliberately: clickhouse://
// now routes to the clickhouse driver, and the dialect behind it exists
// (pkg/dbdialect/clickhouse.go, read-side only). This is the test phase 0
// said would have to change by hand.
func TestClickHouseURIsRouteToTheClickHouseDriver(t *testing.T) {
	driver, dsn, err := DetectDriver("clickhouse://u:p@h:9000/db")
	if err != nil {
		t.Fatal(err)
	}
	if driver != "clickhouse" {
		t.Fatalf("driver = %q, want clickhouse", driver)
	}
	if dsn != "clickhouse://u:p@h:9000/db" {
		t.Fatalf("dsn = %q; clickhouse-go takes the URI verbatim", dsn)
	}
	if got := dialectForURI("clickhouse://u:p@h:9000/db"); got != "clickhouse" {
		t.Fatalf("dialectForURI = %q, want clickhouse", got)
	}
}
