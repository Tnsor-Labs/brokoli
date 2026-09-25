package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	_ "modernc.org/sqlite"
)

func profileStore(t *testing.T) *UserStore {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	us, err := NewUserStore(db)
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	return us
}

func profileUser(t *testing.T, us *UserStore, username string) *User {
	t.Helper()
	u, err := us.CreateUser(username, "StrongPassword!123", RoleAdmin)
	if err != nil {
		t.Fatalf("CreateUser(%s): %v", username, err)
	}
	return u
}

func putProfile(us *UserStore, userID, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPut, "/api/auth/me/profile", strings.NewReader(body))
	claims := &jwt.MapClaims{"sub": userID, "role": "admin"}
	r = r.WithContext(context.WithValue(r.Context(), "claims", claims))
	rec := httptest.NewRecorder()
	UpdateProfileHandler(us)(rec, r)
	return rec
}

// The endpoint #604 says is missing. Before this an account created with
// just a username had an empty display name and email and no way to fill
// them in.
func TestUpdateProfileWritesTheFields(t *testing.T) {
	us := profileStore(t)
	u := profileUser(t, us, "alice")

	rec := putProfile(us, u.ID, `{"display_name":"Alice A","email":"alice@example.com","avatar_url":"https://example.com/a.png"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT profile: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got User
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response: %v", err)
	}
	if got.DisplayName != "Alice A" || got.Email != "alice@example.com" || got.AvatarURL != "https://example.com/a.png" {
		t.Fatalf("response = %+v, want the three fields set", got)
	}

	// Persisted, not merely echoed.
	stored, err := us.GetUserByID(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.DisplayName != "Alice A" || stored.Email != "alice@example.com" || stored.AvatarURL != "https://example.com/a.png" {
		t.Fatalf("stored = %+v, want the three fields set", stored)
	}
}

// The distinction SetProfile could not express, and the reason this does
// not forward to it: an absent field keeps its value, a field sent as ""
// is cleared.
func TestUpdateProfileAbsentKeepsAndEmptyClears(t *testing.T) {
	us := profileStore(t)
	u := profileUser(t, us, "bob")
	if rec := putProfile(us, u.ID, `{"display_name":"Bob B","email":"bob@example.com"}`); rec.Code != http.StatusOK {
		t.Fatalf("seed: status = %d; body=%s", rec.Code, rec.Body.String())
	}

	// Absent display_name must survive a write that only touches email.
	if rec := putProfile(us, u.ID, `{"email":"bob2@example.com"}`); rec.Code != http.StatusOK {
		t.Fatalf("partial update: status = %d; body=%s", rec.Code, rec.Body.String())
	}
	stored, _ := us.GetUserByID(u.ID)
	if stored.DisplayName != "Bob B" {
		t.Errorf("display_name = %q after a partial update, want it kept", stored.DisplayName)
	}
	if stored.Email != "bob2@example.com" {
		t.Errorf("email = %q, want bob2@example.com", stored.Email)
	}

	// Explicit empty clears it.
	if rec := putProfile(us, u.ID, `{"display_name":""}`); rec.Code != http.StatusOK {
		t.Fatalf("clear: status = %d; body=%s", rec.Code, rec.Body.String())
	}
	stored, _ = us.GetUserByID(u.ID)
	if stored.DisplayName != "" {
		t.Errorf("display_name = %q after being cleared, want empty", stored.DisplayName)
	}
	if stored.Email != "bob2@example.com" {
		t.Errorf("email = %q, want the untouched value kept", stored.Email)
	}
}

func TestUpdateProfileRejectsBadInput(t *testing.T) {
	us := profileStore(t)
	u := profileUser(t, us, "carol")

	cases := map[string]string{
		"not an address":            `{"email":"not-an-address"}`,
		"javascript avatar":         `{"avatar_url":"javascript:alert(1)"}`,
		"data uri avatar":           `{"avatar_url":"data:image/png;base64,AAAA"}`,
		"relative avatar":           `{"avatar_url":"/uploads/a.png"}`,
		"display name with newline": `{"display_name":"Carol\nAdmin"}`,
		"over-long display name":    `{"display_name":"` + strings.Repeat("x", 129) + `"}`,
	}
	for name, body := range cases {
		if rec := putProfile(us, u.ID, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400; body=%s", name, rec.Code, rec.Body.String())
		}
	}
	// The other direction: the account is untouched by every refusal.
	stored, _ := us.GetUserByID(u.ID)
	if stored.DisplayName != "" || stored.Email != "" || stored.AvatarURL != "" {
		t.Fatalf("stored = %+v, want untouched after refusals", stored)
	}
	// And a plain http avatar is accepted, so the scheme check is not
	// refusing everything.
	if rec := putProfile(us, u.ID, `{"avatar_url":"http://example.com/a.png"}`); rec.Code != http.StatusOK {
		t.Errorf("http avatar: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

func TestUpdateProfileRefusesAnAddressAnotherAccountHolds(t *testing.T) {
	us := profileStore(t)
	a := profileUser(t, us, "dave")
	b := profileUser(t, us, "erin")
	if rec := putProfile(us, a.ID, `{"email":"shared@example.com"}`); rec.Code != http.StatusOK {
		t.Fatalf("seed: status = %d; body=%s", rec.Code, rec.Body.String())
	}

	rec := putProfile(us, b.ID, `{"email":"shared@example.com"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("second account takes the address: status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	// Keeping your own address is not a collision with yourself.
	if rec := putProfile(us, a.ID, `{"email":"shared@example.com","display_name":"Dave"}`); rec.Code != http.StatusOK {
		t.Fatalf("owner rewrites their own address: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

// The account is taken from the token, never the body, so there is no
// field that edits somebody else.
func TestUpdateProfileTakesTheAccountFromTheToken(t *testing.T) {
	us := profileStore(t)
	victim := profileUser(t, us, "frank")
	attacker := profileUser(t, us, "mallory")

	body := `{"id":"` + victim.ID + `","user_id":"` + victim.ID + `","display_name":"owned"}`
	if rec := putProfile(us, attacker.ID, body); rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	stored, _ := us.GetUserByID(victim.ID)
	if stored.DisplayName != "" {
		t.Fatalf("victim display_name = %q, want untouched", stored.DisplayName)
	}
	mine, _ := us.GetUserByID(attacker.ID)
	if mine.DisplayName != "owned" {
		t.Fatalf("caller display_name = %q, want their own record written", mine.DisplayName)
	}
}

func TestUpdateProfileRequiresAuthentication(t *testing.T) {
	us := profileStore(t)
	r := httptest.NewRequest(http.MethodPut, "/api/auth/me/profile", strings.NewReader(`{"display_name":"x"}`))
	rec := httptest.NewRecorder()
	UpdateProfileHandler(us)(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no claims: status = %d, want 401", rec.Code)
	}

	// Claims present but carrying no subject is the same refusal: there
	// is no account to write.
	r = httptest.NewRequest(http.MethodPut, "/api/auth/me/profile", strings.NewReader(`{"display_name":"x"}`))
	claims := &jwt.MapClaims{"role": "admin"}
	r = r.WithContext(context.WithValue(r.Context(), "claims", claims))
	rec = httptest.NewRecorder()
	UpdateProfileHandler(us)(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no subject: status = %d, want 401", rec.Code)
	}
}
