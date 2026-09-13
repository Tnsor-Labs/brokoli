package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

// The variables table keyed on (key) alone, so a variable name was
// global: one workspace's save overwrote another's through
// ON CONFLICT(key), and a read by key alone returned whichever workspace
// had written last. Variables hold secrets.

func varStore(t *testing.T) *SQLiteStore {
	t.Helper()
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "v.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func put(t *testing.T, s *SQLiteStore, ws, key, val string) {
	t.Helper()
	now := time.Now().UTC()
	if err := s.SetVariable(&models.Variable{
		Key: key, Value: val, Type: models.VarTypeString,
		WorkspaceID: ws, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("SetVariable(%s/%s): %v", ws, key, err)
	}
}

func TestOneWorkspaceCannotOverwriteAnothersVariable(t *testing.T) {
	s := varStore(t)
	put(t, s, "tenant-a", "api_token", "a-secret")
	put(t, s, "tenant-b", "api_token", "b-secret")

	a, err := s.GetVariable("tenant-a", "api_token")
	if err != nil {
		t.Fatalf("tenant-a lost its variable: %v", err)
	}
	if a.Value != "a-secret" {
		t.Errorf("tenant-a reads %q; another workspace overwrote it", a.Value)
	}
	b, err := s.GetVariable("tenant-b", "api_token")
	if err != nil {
		t.Fatalf("tenant-b: %v", err)
	}
	if b.Value != "b-secret" {
		t.Errorf("tenant-b reads %q", b.Value)
	}
}

func TestAWorkspaceCannotReadAnothersVariable(t *testing.T) {
	s := varStore(t)
	put(t, s, "tenant-a", "only_in_a", "a-secret")

	if _, err := s.GetVariable("tenant-b", "only_in_a"); err == nil {
		t.Error("tenant-b read a variable that belongs to tenant-a")
	}
}

func TestDeleteIsScopedToItsWorkspace(t *testing.T) {
	s := varStore(t)
	put(t, s, "tenant-a", "shared_name", "a-value")
	put(t, s, "tenant-b", "shared_name", "b-value")

	if err := s.DeleteVariable("tenant-b", "shared_name"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.GetVariable("tenant-a", "shared_name"); err != nil {
		t.Error("deleting tenant-b's variable removed tenant-a's")
	}
	if _, err := s.GetVariable("tenant-b", "shared_name"); err == nil {
		t.Error("tenant-b's variable survived its own delete")
	}
}

// Updating in place must still work, or every edit creates a duplicate.
func TestSetUpdatesWithinOneWorkspace(t *testing.T) {
	s := varStore(t)
	put(t, s, "tenant-a", "rotating", "first")
	put(t, s, "tenant-a", "rotating", "second")

	v, err := s.GetVariable("tenant-a", "rotating")
	if err != nil {
		t.Fatal(err)
	}
	if v.Value != "second" {
		t.Errorf("value = %q, want the updated one", v.Value)
	}
	vars, _ := s.ListVariablesByWorkspace("tenant-a")
	if len(vars) != 1 {
		t.Errorf("workspace holds %d rows, want 1; the update inserted instead", len(vars))
	}
}

// An empty workspace means the default one, on both the write and the
// read, or single-tenant callers break.
func TestEmptyWorkspaceMeansDefault(t *testing.T) {
	s := varStore(t)
	put(t, s, "", "plain", "value")

	if v, err := s.GetVariable("", "plain"); err != nil || v.Value != "value" {
		t.Fatalf("empty-workspace read failed: %v", err)
	}
	if v, err := s.GetVariable("default", "plain"); err != nil || v.Value != "value" {
		t.Errorf("an empty workspace did not resolve to default: %v", err)
	}
}

// An existing database carries the old shape: `key TEXT PRIMARY KEY`,
// with workspace_id added later as a plain column. Opening it must widen
// the key without losing a row.
func TestMigrationWidensTheKeyWithoutLosingData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")

	// Build the pre-migration table by hand, exactly as 003_variables.sql
	// created it plus the later workspace_id column.
	old, err := openRawSQLite(path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := old.Exec(`CREATE TABLE variables (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL DEFAULT '',
		type TEXT NOT NULL DEFAULT 'string',
		description TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		workspace_id TEXT NOT NULL DEFAULT 'default'
	)`); err != nil {
		t.Fatalf("create old table: %v", err)
	}
	if _, err := old.Exec(`INSERT INTO variables
		(key, value, type, description, created_at, updated_at, workspace_id) VALUES
		('a_key', 'a-value', 'string', '', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 'tenant-a'),
		('b_key', 'b-value', 'secret', '', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 'tenant-b'),
		('legacy', 'l-value', 'string', '', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	old.Close()

	s, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("reopen with migrations: %v", err)
	}
	defer s.Close()

	for _, tc := range []struct{ ws, key, want string }{
		{"tenant-a", "a_key", "a-value"},
		{"tenant-b", "b_key", "b-value"},
		// A blank workspace_id becomes the default one rather than
		// staying unreachable behind a workspace nobody names.
		{"default", "legacy", "l-value"},
	} {
		v, err := s.GetVariable(tc.ws, tc.key)
		if err != nil {
			t.Errorf("%s/%s lost in migration: %v", tc.ws, tc.key, err)
			continue
		}
		if v.Value != tc.want {
			t.Errorf("%s/%s = %q, want %q", tc.ws, tc.key, v.Value, tc.want)
		}
	}

	// And the widening actually happened: two workspaces may now hold one
	// name, which the old primary key forbade.
	put(t, s, "tenant-a", "same_name", "a")
	put(t, s, "tenant-b", "same_name", "b")
	a, _ := s.GetVariable("tenant-a", "same_name")
	b, _ := s.GetVariable("tenant-b", "same_name")
	if a == nil || b == nil || a.Value != "a" || b.Value != "b" {
		t.Error("the key was not widened to (workspace_id, key)")
	}
}

// Re-running the migration on an already-widened database must be a
// no-op, since it runs on every boot.
func TestMigrationIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "twice.db")
	s1, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	put(t, s1, "tenant-a", "k", "v")
	s1.Close()

	s2, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer s2.Close()
	if v, err := s2.GetVariable("tenant-a", "k"); err != nil || v.Value != "v" {
		t.Errorf("re-running the migration lost data: %v", err)
	}
}

// openRawSQLite opens the file without running migrations, so a test can
// build the pre-migration schema by hand.
func openRawSQLite(path string) (*sql.DB, error) {
	return sql.Open("sqlite", path)
}
