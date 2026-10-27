package store

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

// openVariablesKeyTestStore connects to a dedicated database, live or skip,
// like openTestPostgresStore.
func openVariablesKeyTestStore(t *testing.T) (*PostgresStore, string) {
	t.Helper()
	dsn := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if dsn == "" {
		dsn = "postgres://postgres@localhost:5432/brokoli_varpk_test?sslmode=disable"
	}
	probe, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Skipf("skipping live-Postgres variables key test: sql.Open: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := probe.PingContext(ctx); err != nil {
		probe.Close()
		t.Skipf("skipping live-Postgres variables key test: no reachable Postgres at %s: %v", dsn, err)
	}
	probe.Close()

	s, err := NewPostgresStore(dsn)
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, dsn
}

func variablesKeyOID(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var oid int64
	if err := db.QueryRow(`SELECT oid FROM pg_constraint
		WHERE conrelid = 'variables'::regclass AND contype = 'p'`).Scan(&oid); err != nil {
		t.Fatalf("read variables primary key: %v", err)
	}
	return oid
}

// Every start used to drop the variables primary key and add it again,
// with both errors ignored. While a new process migrated, a running one's
// SetVariable (ON CONFLICT (workspace_id, key)) failed for want of the
// constraint, and an ADD that failed left the table with no key at all.
// The key is now changed once, and left alone after that.
func TestPostgresVariablesKeyIsNotRebuiltOnEveryStart(t *testing.T) {
	s, dsn := openVariablesKeyTestStore(t)
	before := variablesKeyOID(t, s.db)
	// Run the baseline again, as on a database from before versioning: it
	// must leave a key that is already right alone.
	forgetMigrations(t, s.db)

	again, err := NewPostgresStore(dsn)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer again.Close()

	if after := variablesKeyOID(t, again.db); after != before {
		t.Fatalf("the variables primary key was dropped and added again on start (oid %d -> %d)", before, after)
	}
}

// A database from before variables were scoped to a workspace has the key
// (key). Opening it widens the key to (workspace_id, key), keeps the rows,
// and lets two workspaces hold one name.
func TestPostgresVariablesKeyIsWidenedOnce(t *testing.T) {
	s, dsn := openVariablesKeyTestStore(t)
	if _, err := s.db.Exec(`DELETE FROM variables WHERE key = 'region'`); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if _, err := s.db.Exec(`ALTER TABLE variables DROP CONSTRAINT variables_pkey`); err != nil {
		t.Fatalf("drop current key: %v", err)
	}
	if _, err := s.db.Exec(`ALTER TABLE variables ADD PRIMARY KEY (key)`); err != nil {
		t.Fatalf("restore the old key: %v", err)
	}
	now := time.Now().UTC()
	if _, err := s.db.Exec(`INSERT INTO variables (key, value, created_at, updated_at, workspace_id)
		VALUES ('region', 'eu', $1, $1, '')`, now); err != nil {
		t.Fatalf("insert an old row: %v", err)
	}
	forgetMigrations(t, s.db)

	again, err := NewPostgresStore(dsn)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer again.Close()

	var cols string
	if err := again.db.QueryRow(`SELECT string_agg(a.attname, ',' ORDER BY k.n)
		FROM pg_constraint c
		CROSS JOIN LATERAL unnest(c.conkey) WITH ORDINALITY AS k(attnum, n)
		JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum
		WHERE c.conrelid = 'variables'::regclass AND c.contype = 'p'`).Scan(&cols); err != nil {
		t.Fatalf("read key columns: %v", err)
	}
	if cols != "workspace_id,key" {
		t.Fatalf("variables key = (%s), want (workspace_id,key)", cols)
	}
	old, err := again.GetVariable("default", "region")
	if err != nil || old.Value != "eu" {
		t.Fatalf("the existing row did not survive in the default workspace: %+v, %v", old, err)
	}
	if err := again.SetVariable(&models.Variable{Key: "region", Value: "us", WorkspaceID: "other", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("a second workspace could not hold the same name: %v", err)
	}
	if _, err := again.db.Exec(`DELETE FROM variables WHERE key = 'region'`); err != nil {
		t.Fatalf("clean up: %v", err)
	}
}

// The old code could leave the table with no key: its ADD failed with the
// error ignored. If two rows then share a name in one workspace, the key
// cannot be added, and starting has to say so instead of running keyless.
func TestPostgresVariablesWithoutAKeyFailToStartWhenTheKeyCannotBeAdded(t *testing.T) {
	s, dsn := openVariablesKeyTestStore(t)
	t.Cleanup(func() {
		// Leave the database as a start finds it: no duplicates, keyed.
		s.db.Exec(`DELETE FROM variables WHERE key = 'dup'`)
		s.db.Exec(`ALTER TABLE variables ADD PRIMARY KEY (workspace_id, key)`)
	})
	if _, err := s.db.Exec(`ALTER TABLE variables DROP CONSTRAINT variables_pkey`); err != nil {
		t.Fatalf("drop current key: %v", err)
	}
	now := time.Now().UTC()
	for range 2 {
		if _, err := s.db.Exec(`INSERT INTO variables (key, value, created_at, updated_at, workspace_id)
			VALUES ('dup', 'x', $1, $1, 'default')`, now); err != nil {
			t.Fatalf("insert a duplicate: %v", err)
		}
	}
	forgetMigrations(t, s.db)

	again, err := NewPostgresStore(dsn)
	if err == nil {
		again.Close()
		t.Fatal("opened a database whose variables table has no key and cannot get one")
	}
	if !strings.Contains(err.Error(), "add the new key") {
		t.Fatalf("error does not say what failed: %v", err)
	}
}
