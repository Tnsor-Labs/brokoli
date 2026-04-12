package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCreateUser_InvalidPasswordSurfacesReason is the regression test for the
// "user already exists" misdirection bug. The pre-fix CreateUserHandler
// returned 409 "user already exists" for ANY error from CreateUser, including
// validation errors like "password too short" or "password missing uppercase".
// That cost the smoke test ~5 minutes of debugging because the error message
// pointed at the wrong cause.
//
// This test creates a user with a too-weak password and asserts that the
// response carries the actual validation reason and a 400 status, not 409.
func TestCreateUser_InvalidPasswordSurfacesReason(t *testing.T) {
	us := newTestUserStore(t)
	handler := CreateUserHandler(us)

	body, _ := json.Marshal(map[string]string{
		"username": "alice",
		"password": "weakpass", // 8 chars, no uppercase, no digit — fails validation
		"role":     "admin",
	})
	req := httptest.NewRequest("POST", "/api/auth/users", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "user already exists") {
		t.Errorf("response should not say 'user already exists' for a password validation failure; got: %s", rec.Body.String())
	}
	// Should mention the actual reason — the validator complains about length
	// first, then the character-class requirements. Either is acceptable here.
	bodyStr := strings.ToLower(rec.Body.String())
	if !strings.Contains(bodyStr, "password") {
		t.Errorf("response should mention 'password' in the error; got: %s", rec.Body.String())
	}
}

// TestCreateUser_DuplicateUsernameReturnsConflict locks in the original
// "user already exists" behavior for the case where it's actually true.
func TestCreateUser_DuplicateUsernameReturnsConflict(t *testing.T) {
	us := newTestUserStore(t)
	handler := CreateUserHandler(us)

	// First user creates successfully.
	body, _ := json.Marshal(map[string]string{
		"username": "bob",
		"password": "ValidPass123",
		"role":     "admin",
	})
	req := httptest.NewRequest("POST", "/api/auth/users", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first create failed: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Second create with the same username — needs admin auth now since
	// UserCount > 0. Mint a token and pass it via the request context.
	token := signTestToken(t, "bob-id", "bob", "admin", "")
	body2, _ := json.Marshal(map[string]string{
		"username": "bob",
		"password": "AnotherPass456",
		"role":     "admin",
	})
	req2 := httptest.NewRequest("POST", "/api/auth/users", bytes.NewReader(body2))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Authorization", "Bearer "+token)

	// Wrap with JWTAuth so the claims land on the context the way they would
	// in production.
	wrapped := JWTAuth(us)(handler)
	rec2 := httptest.NewRecorder()
	wrapped.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusConflict {
		t.Errorf("status: got %d, want 409; body=%s", rec2.Code, rec2.Body.String())
	}
	if !strings.Contains(rec2.Body.String(), "user already exists") {
		t.Errorf("response should say 'user already exists' for duplicate username; got: %s", rec2.Body.String())
	}
}

// TestCreateUser_UnitErrorsAreSentinels verifies CreateUser itself returns
// the right sentinel errors. This catches regressions where someone unwraps
// or replaces the wrapped error.
func TestCreateUser_UnitErrorsAreSentinels(t *testing.T) {
	us := newTestUserStore(t)

	// Weak password → ErrInvalidPassword
	_, err := us.CreateUser("charlie", "short", RoleAdmin)
	if !errors.Is(err, ErrInvalidPassword) {
		t.Errorf("weak password should return ErrInvalidPassword; got %v", err)
	}

	// Valid create succeeds
	if _, err := us.CreateUser("charlie", "ValidPass123", RoleAdmin); err != nil {
		t.Fatalf("valid create failed: %v", err)
	}

	// Duplicate username → ErrUserExists
	_, err = us.CreateUser("charlie", "AnotherPass456", RoleAdmin)
	if !errors.Is(err, ErrUserExists) {
		t.Errorf("duplicate username should return ErrUserExists; got %v", err)
	}
}
