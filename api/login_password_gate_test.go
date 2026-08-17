package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestLoginHandler_PasswordLoginEnabledFunc guards both directions of the
// PasswordLoginEnabledFunc gate: nil (community edition, and the default
// for every existing deployment) must behave exactly as before this hook
// existed, and an enterprise integration returning false must reject valid
// credentials with 403 rather than authenticating them.
func TestLoginHandler_PasswordLoginEnabledFunc(t *testing.T) {
	us := newTestUserStore(t)
	if _, err := us.CreateUser("gateuser", "ValidPass123!", RoleAdmin); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	handler := LoginHandler(us)

	login := func() *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"username": "gateuser", "password": "ValidPass123!"})
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	t.Run("nil hook allows password login", func(t *testing.T) {
		orig := PasswordLoginEnabledFunc
		PasswordLoginEnabledFunc = nil
		t.Cleanup(func() { PasswordLoginEnabledFunc = orig })

		rec := login()
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("hook returning false rejects valid credentials", func(t *testing.T) {
		orig := PasswordLoginEnabledFunc
		PasswordLoginEnabledFunc = func() bool { return false }
		t.Cleanup(func() { PasswordLoginEnabledFunc = orig })

		rec := login()
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("hook returning true allows password login", func(t *testing.T) {
		orig := PasswordLoginEnabledFunc
		PasswordLoginEnabledFunc = func() bool { return true }
		t.Cleanup(func() { PasswordLoginEnabledFunc = orig })

		rec := login()
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
	})
}
