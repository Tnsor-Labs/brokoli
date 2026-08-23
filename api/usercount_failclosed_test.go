package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// brokenUserStore returns a UserStore whose database is closed, so every
// query fails. That is a stand-in for the states that actually produce
// this in production — a restarting database, an exhausted connection
// pool, a network blip — all of which were observed turning into
// "system requires initial setup" on a fully provisioned deployment.
func brokenUserStore(t *testing.T) *UserStore {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "u.db"))
	if err != nil {
		t.Fatal(err)
	}
	us, err := NewUserStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := us.CreateUser("realadmin", "Correct9Horse", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	// The system now has a user. Break the database underneath it.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return us
}

// A failed count must not read as "no users". Zero users means open mode,
// and open mode is unauthenticated.
func TestUserCountDistinguishesFailureFromZero(t *testing.T) {
	us := brokenUserStore(t)
	if _, err := us.UserCountErr(); err == nil {
		t.Fatal("a broken database must report an error, not a count")
	}
}

// With the count failing, an unauthenticated POST to /api/auth/users must
// refuse explicitly rather than proceed.
//
// The status code is the assertion, not merely the absence of a 201. With
// the database fully down the insert would fail on its own and return 500,
// which looks like a refusal but is not one — it is the bypass running to
// completion and being saved by an unrelated failure. The real-world shape
// is a count that fails while the pool recovers in time for the insert to
// succeed, which is exactly what was observed in the lab. So this requires
// the handler to have decided not to try.
func TestCreateUserDeniedWhenUserCountFails(t *testing.T) {
	us := brokenUserStore(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/users",
		strings.NewReader(`{"username":"attacker","password":"Correct9Horse","role":"admin"}`))
	CreateUserHandler(us)(rec, req)

	if rec.Code == http.StatusCreated {
		t.Fatalf("an unauthenticated caller created a user while the count was failing: %s", rec.Body.String())
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 (refused before attempting the write), got %d: %s", rec.Code, rec.Body.String())
	}
}

// The middleware must fail closed too: a failing count must not open
// /api/auth/* to unauthenticated callers.
func TestJWTAuthDoesNotOpenUpWhenUserCountFails(t *testing.T) {
	us := brokenUserStore(t)
	reached := false
	h := JWTAuth(us)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/users", strings.NewReader(`{}`))
	h.ServeHTTP(rec, req)

	if reached {
		t.Fatal("an unauthenticated request reached the handler while the user count was failing")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 while the database is unavailable, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "initial setup") {
		t.Errorf("a database outage should not be reported as needing setup: %s", rec.Body.String())
	}
}

// A genuinely empty system must still be able to bootstrap, or this fix
// would lock everyone out of a fresh install.
func TestFreshInstallStillOpensForSetup(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "u.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	us, err := NewUserStore(db)
	if err != nil {
		t.Fatal(err)
	}

	n, err := us.UserCountErr()
	if err != nil || n != 0 {
		t.Fatalf("fresh install: count=%d err=%v, want 0 and no error", n, err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/users",
		strings.NewReader(`{"username":"firstadmin","password":"Correct9Horse"}`))
	CreateUserHandler(us)(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first user creation should still work: %d %s", rec.Code, rec.Body.String())
	}

	// And once it exists, the next unauthenticated attempt is refused.
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/api/auth/users",
		strings.NewReader(`{"username":"second","password":"Correct9Horse","role":"admin"}`))
	CreateUserHandler(us)(rec2, req2)
	if rec2.Code == http.StatusCreated {
		t.Fatal("a second user was created without authentication")
	}
}
