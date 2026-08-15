package api

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// newProfileTestStore builds a UserStore over an in-memory SQLite DB.
func newProfileTestStore(t *testing.T) *UserStore {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	us, err := NewUserStore(db)
	if err != nil {
		t.Fatalf("new user store: %v", err)
	}
	return us
}

// A password account has no display name or email, and its label falls
// back to the username — the pre-existing behavior must not change.
func TestProfileDefaultsEmptyAndLabelFallsBack(t *testing.T) {
	us := newProfileTestStore(t)

	created, err := us.CreateUser("alice", "Password123", RoleEditor)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	got, err := us.GetUserByID(created.ID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if got.DisplayName != "" || got.Email != "" {
		t.Fatalf("expected empty profile fields, got display=%q email=%q", got.DisplayName, got.Email)
	}
	if got.DisplayLabel() != "alice" {
		t.Fatalf("expected label to fall back to username, got %q", got.DisplayLabel())
	}
}

// SetProfile persists both fields and they survive every read path the
// UI and enterprise code use.
func TestSetProfilePersistsAcrossReadPaths(t *testing.T) {
	us := newProfileTestStore(t)
	created, err := us.CreateUser("google_someone@example.com", "Password123", RoleEditor)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	if err := us.SetProfile(created.ID, "Someone Real", "someone@example.com"); err != nil {
		t.Fatalf("set profile: %v", err)
	}

	byID, err := us.GetUserByID(created.ID)
	if err != nil {
		t.Fatalf("get by id: %v", err)
	}
	if byID.DisplayName != "Someone Real" || byID.Email != "someone@example.com" {
		t.Fatalf("GetUserByID lost profile: %+v", byID)
	}
	if byID.DisplayLabel() != "Someone Real" {
		t.Fatalf("expected display name as label, got %q", byID.DisplayLabel())
	}

	authed, err := us.Authenticate("google_someone@example.com", "Password123")
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if authed.DisplayName != "Someone Real" || authed.Email != "someone@example.com" {
		t.Fatalf("Authenticate lost profile: %+v", authed)
	}

	list, err := us.ListUsers()
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	if len(list) != 1 || list[0].DisplayName != "Someone Real" {
		t.Fatalf("ListUsers lost profile: %+v", list)
	}
}

// A later login that knows only some fields must not blank the others:
// providers differ in what they return, and an empty value is "unknown",
// not "clear this".
func TestSetProfileIgnoresEmptyArguments(t *testing.T) {
	us := newProfileTestStore(t)
	created, err := us.CreateUser("bob", "Password123", RoleEditor)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := us.SetProfile(created.ID, "Bob Real", "bob@example.com"); err != nil {
		t.Fatalf("seed profile: %v", err)
	}

	if err := us.SetProfile(created.ID, "", ""); err != nil {
		t.Fatalf("no-op set profile: %v", err)
	}
	if err := us.SetProfile(created.ID, "Bob Renamed", ""); err != nil {
		t.Fatalf("partial set profile: %v", err)
	}

	got, err := us.GetUserByID(created.ID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if got.DisplayName != "Bob Renamed" {
		t.Fatalf("expected updated display name, got %q", got.DisplayName)
	}
	if got.Email != "bob@example.com" {
		t.Fatalf("empty email argument cleared a stored value: %q", got.Email)
	}
}

// The token carries the presentation fields only when they exist, so
// password-account tokens keep exactly the claims they had before.
func TestGenerateTokenOmitsEmptyProfileClaims(t *testing.T) {
	InitJWTSecret()

	plain := &User{ID: "u1", Username: "alice", Role: RoleEditor}
	tok, err := GenerateToken(plain)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	claims, err := ParseToken(tok)
	if err != nil {
		t.Fatalf("validate token: %v", err)
	}
	if _, ok := (*claims)["display_name"]; ok {
		t.Fatal("display_name claim present for an account without one")
	}
	if _, ok := (*claims)["email"]; ok {
		t.Fatal("email claim present for an account without one")
	}

	sso := &User{ID: "u2", Username: "google_someone@example.com", DisplayName: "Someone Real", Email: "someone@example.com", Role: RoleEditor}
	tok2, err := GenerateToken(sso)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	claims2, err := ParseToken(tok2)
	if err != nil {
		t.Fatalf("validate token: %v", err)
	}
	if (*claims2)["display_name"] != "Someone Real" {
		t.Fatalf("display_name claim missing or wrong: %v", (*claims2)["display_name"])
	}
	if (*claims2)["email"] != "someone@example.com" {
		t.Fatalf("email claim missing or wrong: %v", (*claims2)["email"])
	}
}
