package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
)

// A deployment installed before a permission existed must hold it after an
// upgrade: system roles are the code's definition, and the stored rows are
// brought back to it on every start. Custom roles are not touched.
func testSystemRolesFollowTheCode(t *testing.T, db *sql.DB, placeholder func(int) string, reopen func() Store) {
	t.Helper()
	stale := `["pipelines.view"]`
	if _, err := db.Exec("UPDATE roles SET permissions = "+placeholder(1)+" WHERE id = 'admin'", stale); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DELETE FROM roles WHERE id = 'operator'"); err != nil {
		t.Fatal(err)
	}
	custom := &models.Role{ID: "reconcile-custom", Name: "Reconcile custom", Permissions: []models.Permission{models.PermRunsView}, CreatedAt: "2026-10-06T00:00:00Z"}
	s := reopen()
	_ = s.DeleteRole(custom.ID)
	if err := s.CreateRole(custom); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.DeleteRole(custom.ID) })

	s = reopen()
	want := map[string][]models.Permission{}
	for _, role := range models.DefaultRoles() {
		want[role.ID] = role.Permissions
	}
	for _, id := range []string{"admin", "operator"} {
		got, err := s.GetRole(id)
		if err != nil {
			t.Fatalf("system role %s after restart: %v", id, err)
		}
		if !reflect.DeepEqual(got.Permissions, want[id]) || !got.IsSystem {
			t.Fatalf("system role %s = %v (system=%v), want the code's %v", id, got.Permissions, got.IsSystem, want[id])
		}
	}
	got, err := s.GetRole(custom.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Permissions, custom.Permissions) {
		t.Fatalf("custom role changed on restart: %v", got.Permissions)
	}
}

func TestSQLiteSystemRolesFollowTheCode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roles.db")
	first, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	var stores []*SQLiteStore
	t.Cleanup(func() {
		for _, s := range stores {
			_ = s.Close()
		}
	})
	testSystemRolesFollowTheCode(t, first.db, func(int) string { return "?" }, func() Store {
		s, err := NewSQLiteStore(path)
		if err != nil {
			t.Fatal(err)
		}
		stores = append(stores, s)
		return s
	})
}

func TestPostgresSystemRolesFollowTheCode(t *testing.T) {
	url := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	first, err := NewPostgresStore(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	testSystemRolesFollowTheCode(t, first.db, func(n int) string { return "$1" }, func() Store {
		s, err := NewPostgresStore(url)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}
