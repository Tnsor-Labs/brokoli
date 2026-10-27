package store

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

func wsConnection(id, ws, connID, host string) *models.Connection {
	now := time.Now().UTC()
	return &models.Connection{ID: id, ConnID: connID, Type: models.ConnTypePostgres, Host: host,
		WorkspaceID: ws, CreatedAt: now, UpdatedAt: now}
}

// conn_id is unique within a workspace. Unique everywhere, one tenant's
// create was refused for a name another tenant used -- telling it the name
// existed, and letting it claim names first.
func TestConnectionIDIsUniqueWithinAWorkspace(t *testing.T) {
	testConnectionIDIsUniqueWithinAWorkspace(t, newTestStore(t))
}

func TestConnectionIDIsUniqueWithinAWorkspacePostgres(t *testing.T) {
	url := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	s, err := NewPostgresStore(url)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	testConnectionIDIsUniqueWithinAWorkspace(t, s)
}

func testConnectionIDIsUniqueWithinAWorkspace(t *testing.T, s Store) {
	tag := fmt.Sprintf("%d", time.Now().UnixNano())
	wsA, wsB, name := "ws-a-"+tag, "ws-b-"+tag, "warehouse-"+tag
	t.Cleanup(func() {
		_ = s.DeleteConnectionIn(wsA, name)
		_ = s.DeleteConnectionIn(wsB, name)
	})
	if err := s.CreateConnection(wsConnection("ca-"+tag, wsA, name, "a.internal")); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateConnection(wsConnection("cb-"+tag, wsB, name, "b.internal")); err != nil {
		t.Fatalf("another workspace's connection with the same conn_id: %v", err)
	}
	if err := s.CreateConnection(wsConnection("ca2-"+tag, wsA, name, "dup.internal")); err == nil {
		t.Fatal("a second connection with the same conn_id in one workspace was stored")
	}
	for ws, host := range map[string]string{wsA: "a.internal", wsB: "b.internal"} {
		c, err := s.GetConnectionIn(ws, name)
		if err != nil || c.Host != host {
			t.Errorf("%s: %+v (%v), want host %s", ws, c, err, host)
		}
	}

	// An update reaches its own workspace's connection only.
	upd := wsConnection("ca-"+tag, wsA, name, "a2.internal")
	if err := s.UpdateConnection(upd); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.GetConnectionIn(wsB, name); c == nil || c.Host != "b.internal" {
		t.Errorf("updating workspace A's connection changed B's: %+v", c)
	}
	// And a delete.
	if err := s.DeleteConnectionIn(wsA, name); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetConnectionIn(wsA, name); err == nil {
		t.Error("the deleted connection is still there")
	}
	if c, err := s.GetConnectionIn(wsB, name); err != nil || c == nil {
		t.Errorf("deleting workspace A's connection removed B's: %v", err)
	}
}

// A database whose connections table declared conn_id UNIQUE inline is
// rebuilt once on open, keeping every row and column.
func TestOpeningAnOlderDatabaseScopesConnectionIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	s, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	// Recreate the table the way older versions declared it.
	for _, stmt := range []string{
		`DROP TABLE connections`,
		`CREATE TABLE connections (id TEXT PRIMARY KEY, conn_id TEXT NOT NULL UNIQUE, type TEXT NOT NULL, description TEXT NOT NULL DEFAULT '', host TEXT NOT NULL DEFAULT '', port INTEGER NOT NULL DEFAULT 0, schema_name TEXT NOT NULL DEFAULT '', login TEXT NOT NULL DEFAULT '', password_enc TEXT NOT NULL DEFAULT '', extra_enc TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL, workspace_id TEXT NOT NULL DEFAULT 'default', password_ref TEXT NOT NULL DEFAULT '', extra_ref TEXT NOT NULL DEFAULT '', max_concurrent INTEGER NOT NULL DEFAULT 0, driver_identity TEXT NOT NULL DEFAULT '')`,
		`CREATE UNIQUE INDEX idx_connections_conn_id ON connections(conn_id)`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	old := wsConnection("c1", "ws-a", "warehouse", "a.internal")
	old.Password, old.PasswordRef, old.MaxConcurrent = "enc", "encrypted://enc", 3
	if err := s.CreateConnection(old); err != nil {
		t.Fatal(err)
	}
	forgetMigrations(t, s.db)
	s.Close()

	s, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s.Close()
	c, err := s.GetConnectionIn("ws-a", "warehouse")
	if err != nil || c.Host != "a.internal" || c.PasswordRef != "encrypted://enc" || c.MaxConcurrent != 3 {
		t.Fatalf("the row did not survive the rebuild intact: %+v (%v)", c, err)
	}
	if err := s.CreateConnection(wsConnection("c2", "ws-b", "warehouse", "b.internal")); err != nil {
		t.Errorf("same conn_id in another workspace after the rebuild: %v", err)
	}
	if err := s.CreateConnection(wsConnection("c3", "ws-a", "warehouse", "dup")); err == nil {
		t.Error("the per-workspace uniqueness is missing after the rebuild")
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='connections_rebuild'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("the rebuild left its scratch table behind (%d, %v)", n, err)
	}
}

// On Postgres the server-wide constraint older versions created inline is
// dropped on open, and the per-workspace index takes its place.
func TestOpeningAnOlderPostgresDatabaseScopesConnectionIDs(t *testing.T) {
	url := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	s, err := NewPostgresStore(url)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`ALTER TABLE connections ADD CONSTRAINT connections_conn_id_key UNIQUE (conn_id)`); err != nil {
		t.Skipf("cannot recreate the old constraint on this database: %v", err)
	}
	forgetMigrations(t, s.db)
	s.Close()
	s, err = NewPostgresStore(url)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pg_constraint WHERE conname = 'connections_conn_id_key'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("the server-wide constraint is still there (%d, %v)", n, err)
	}
	testConnectionIDIsUniqueWithinAWorkspace(t, s)
}
