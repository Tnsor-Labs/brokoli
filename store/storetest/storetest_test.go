package storetest

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

func schema(t *testing.T, s *store.SQLiteStore) []string {
	t.Helper()
	db := s.RawDB().(*sql.DB)
	var out []string
	for _, q := range []string{
		`SELECT type || ' ' || name || ': ' || COALESCE(sql, '') FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name`,
		`SELECT 'migration ' || version FROM schema_migrations ORDER BY version`,
		`SELECT 'role ' || id || ' ' || permissions FROM roles ORDER BY id`,
	} {
		rows, err := db.Query(q)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			out = append(out, line)
		}
		rows.Close()
	}
	return out
}

// A store opened from the template is the store a fresh migration makes:
// the same tables, indexes, recorded migrations and seeded roles.
func TestATemplateStoreIsAFreshlyMigratedStore(t *testing.T) {
	fresh, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	copied, err := NewSQLiteStore(filepath.Join(t.TempDir(), "copied.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer copied.Close()
	want, got := schema(t, fresh), schema(t, copied)
	if len(want) < 20 || !reflect.DeepEqual(got, want) {
		t.Fatalf("template store differs from a fresh one (%d lines vs %d)", len(got), len(want))
	}
}

// An existing database is opened, not replaced, so a test that closes and
// reopens its store keeps what it wrote; and two stores share nothing.
func TestAnExistingStoreIsReopenedAndStoresAreSeparate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keep.db")
	s, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	p := &models.Pipeline{ID: "kept", Name: "kept", Nodes: []models.Node{}, Edges: []models.Edge{}, CreatedAt: now, UpdatedAt: now}
	if err := s.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	s.Close()
	again, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if _, err := again.GetPipeline("kept"); err != nil {
		t.Fatalf("reopened store lost its pipeline: %v", err)
	}
	other, err := NewSQLiteStore(filepath.Join(t.TempDir(), "other.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.GetPipeline("kept"); err == nil {
		t.Fatal("a new store holds another store's pipeline")
	}
}
