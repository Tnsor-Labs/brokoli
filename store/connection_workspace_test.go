package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

// GetConnection carries the connection's workspace: the engine refuses a
// connection from another workspace than the running pipeline's, and can
// only do that if the store says where the connection lives.
func testGetConnectionCarriesWorkspace(t *testing.T, s Store) {
	t.Helper()
	now := time.Now().UTC()
	c := &models.Connection{ID: "ws-conn-" + now.Format("150405.000000000"), ConnID: "ws-conn-" + now.Format("150405000000000"),
		Type: models.ConnTypePostgres, Host: "h", WorkspaceID: "ws-a", CreatedAt: now, UpdatedAt: now}
	if err := s.CreateConnection(c); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.DeleteConnection(c.ConnID) })
	got, err := s.GetConnection(c.ConnID)
	if err != nil {
		t.Fatal(err)
	}
	if got.WorkspaceID != "ws-a" {
		t.Fatalf("WorkspaceID = %q, want ws-a", got.WorkspaceID)
	}
}

func TestSQLiteGetConnectionCarriesWorkspace(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	testGetConnectionCarriesWorkspace(t, s)
}

func TestPostgresGetConnectionCarriesWorkspace(t *testing.T) {
	url := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	s, err := NewPostgresStore(url)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	testGetConnectionCarriesWorkspace(t, s)
}
