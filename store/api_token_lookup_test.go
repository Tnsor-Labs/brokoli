package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

// A token that has never been used has no last_used_at, and looking it up
// by hash must still find it. On first use is exactly when it is looked up
// for the first time, so a lookup that fails on the missing value makes
// every new API token unusable.
func testAPITokenFoundBeforeFirstUse(t *testing.T, s Store) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	if err := s.CreateWorkspace(&models.Workspace{ID: "ws-tok", Name: "Tok", Slug: "tok", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	tok := &models.APIToken{
		ID: "tok-1", Name: "ci", TokenHash: "hash-never-used", WorkspaceID: "ws-tok",
		UserID: "u1", Role: "editor", ExpiresAt: now.Add(time.Hour), CreatedAt: now,
	}
	if err := s.CreateAPIToken(tok); err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	got, err := s.GetAPITokenByHash("hash-never-used")
	if err != nil {
		t.Fatalf("GetAPITokenByHash for a token never used: %v", err)
	}
	if got.ID != "tok-1" || got.Role != "editor" || got.UserID != "u1" || !got.LastUsedAt.IsZero() {
		t.Fatalf("token = %+v, want tok-1/editor/u1 with no last use", got)
	}
	if !got.ExpiresAt.Equal(tok.ExpiresAt) {
		t.Errorf("expires_at = %v, want %v", got.ExpiresAt, tok.ExpiresAt)
	}
}

func TestSQLiteAPITokenFoundBeforeFirstUse(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "tok.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	testAPITokenFoundBeforeFirstUse(t, s)
}

func TestPostgresAPITokenFoundBeforeFirstUse(t *testing.T) {
	url := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	s, err := NewPostgresStore(url)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, _ = s.db.Exec(`DELETE FROM api_tokens WHERE id = 'tok-1'`)
	_, _ = s.db.Exec(`DELETE FROM workspaces WHERE id = 'ws-tok'`)
	testAPITokenFoundBeforeFirstUse(t, s)
}
