package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// No caller creates an account above its own role. superadmin operates the
// whole instance, so an admin able to create one could promote itself past
// everything an admin is scoped to. Each case checks the stored account, not
// only the status: a handler that answered 403 after creating it would still
// fail here.
func TestCreateUserRoleCeiling(t *testing.T) {
	us := newTestUserStore(t)
	create := JWTAuth(us)(CreateUserHandler(us))

	// The first account is the setup account and needs no caller.
	setup, _ := json.Marshal(map[string]string{"username": "setup", "password": "ValidPass123", "role": "admin"})
	rec := httptest.NewRecorder()
	CreateUserHandler(us).ServeHTTP(rec, httptest.NewRequest("POST", "/api/auth/users", bytes.NewReader(setup)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup account: %d %s", rec.Code, rec.Body.String())
	}

	for _, tc := range []struct {
		callerRole, requested string
		wantStatus            int
	}{
		{"admin", "superadmin", http.StatusForbidden},
		{"admin", "admin", http.StatusCreated},
		{"admin", "editor", http.StatusCreated},
		{"admin", "viewer", http.StatusCreated},
		{"superadmin", "superadmin", http.StatusCreated},
	} {
		username := tc.callerRole + "-makes-" + tc.requested
		body, _ := json.Marshal(map[string]string{"username": username, "password": "ValidPass123", "role": tc.requested})
		req := httptest.NewRequest("POST", "/api/auth/users", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+signTestToken(t, "caller-"+tc.callerRole, "caller", tc.callerRole, ""))
		rec := httptest.NewRecorder()
		create.ServeHTTP(rec, req)
		if rec.Code != tc.wantStatus {
			t.Errorf("%s creating %s: status %d, want %d (%s)", tc.callerRole, tc.requested, rec.Code, tc.wantStatus, rec.Body.String())
		}

		users, err := us.ListUsers()
		if err != nil {
			t.Fatal(err)
		}
		var stored *User
		for i := range users {
			if users[i].Username == username {
				stored = &users[i]
			}
		}
		switch {
		case tc.wantStatus != http.StatusCreated && stored != nil:
			t.Errorf("%s creating %s: refused, yet the account exists with role %q", tc.callerRole, tc.requested, stored.Role)
		case tc.wantStatus == http.StatusCreated && (stored == nil || string(stored.Role) != tc.requested):
			t.Errorf("%s creating %s: stored account = %+v, want role %q", tc.callerRole, tc.requested, stored, tc.requested)
		}
	}
}
