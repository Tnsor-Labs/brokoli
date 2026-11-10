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

// The login screen asks GET /api/auth/methods which sign-in forms to offer.
// It said "password": true even when PasswordLoginEnabledFunc turned
// password login off, so the screen offered a form the server refused
// (#594). The endpoint and the login handler now agree, either way.
func TestAuthMethodsAgreesWithTheLoginHandler(t *testing.T) {
	us := newTestUserStore(t)
	if _, err := us.CreateUser("methodsuser", "ValidPass123!", RoleAdmin); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	for _, c := range []struct {
		name string
		hook func() bool
		want bool
	}{
		{"no hook", nil, true},
		{"hook allows", func() bool { return true }, true},
		{"hook refuses", func() bool { return false }, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			orig := PasswordLoginEnabledFunc
			PasswordLoginEnabledFunc = c.hook
			t.Cleanup(func() { PasswordLoginEnabledFunc = orig })

			rec := httptest.NewRecorder()
			authMethodsHandler(rec, httptest.NewRequest(http.MethodGet, "/api/auth/methods", nil))
			var methods struct {
				Password bool     `json:"password"`
				OAuth    []string `json:"oauth"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &methods); err != nil {
				t.Fatalf("decode %s: %v", rec.Body.String(), err)
			}
			if methods.Password != c.want {
				t.Errorf("methods says password=%v, want %v", methods.Password, c.want)
			}

			body, _ := json.Marshal(map[string]string{"username": "methodsuser", "password": "ValidPass123!"})
			login := httptest.NewRecorder()
			LoginHandler(us).ServeHTTP(login, httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body)))
			if accepted := login.Code == http.StatusOK; accepted != methods.Password {
				t.Errorf("methods says password=%v but login answered %d", methods.Password, login.Code)
			}
		})
	}
}
