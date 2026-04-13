package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// TestUserPostCreateHook_Runs verifies the hook fires after a successful
// user creation and receives the created user. This is the core contract
// the enterprise platform relies on for attaching new users to an org —
// without it, admin-invited users land orphaned and silently can't see
// any data.
func TestUserPostCreateHook_Runs(t *testing.T) {
	us := newTestUserStore(t)
	handler := CreateUserHandler(us)

	// Install a hook that records the user it was called with.
	var (
		mu       sync.Mutex
		gotUser  *User
		callCount int
	)
	orig := UserPostCreateHook
	UserPostCreateHook = func(u *User, r *http.Request) error {
		mu.Lock()
		defer mu.Unlock()
		gotUser = u
		callCount++
		return nil
	}
	t.Cleanup(func() { UserPostCreateHook = orig })

	body, _ := json.Marshal(map[string]string{
		"username": "hookuser",
		"password": "ValidPass123",
		"role":     "admin",
	})
	req := httptest.NewRequest("POST", "/api/auth/users", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status: got %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if callCount != 1 {
		t.Errorf("hook call count: got %d, want 1", callCount)
	}
	if gotUser == nil {
		t.Fatal("hook received nil user")
	}
	if gotUser.Username != "hookuser" {
		t.Errorf("hook received wrong user: got %q, want hookuser", gotUser.Username)
	}
}

// TestUserPostCreateHook_FailureBlocksCreate verifies that if the hook
// returns an error, the handler reports 500 and doesn't pretend the
// create succeeded. An orphan user (in `users` but not in `org_members`)
// is worse than a clear failure — admins can retry, and the hook is
// idempotent so retries are safe.
func TestUserPostCreateHook_FailureBlocksCreate(t *testing.T) {
	us := newTestUserStore(t)
	handler := CreateUserHandler(us)

	orig := UserPostCreateHook
	UserPostCreateHook = func(u *User, r *http.Request) error {
		return errors.New("simulated hook failure")
	}
	t.Cleanup(func() { UserPostCreateHook = orig })

	body, _ := json.Marshal(map[string]string{
		"username": "failuser",
		"password": "ValidPass123",
		"role":     "admin",
	})
	req := httptest.NewRequest("POST", "/api/auth/users", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status: got %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
}
