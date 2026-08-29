package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

// BearerTokenResolverFunc: the extension seam that makes revocable API
// tokens authenticate. Consulted only after JWT parsing fails; nil or a
// false return leaves the 401 exactly as it was.
func TestBearerTokenResolverFunc(t *testing.T) {
	users := newTestUserStore(t)
	if _, err := users.CreateUser("someone", "Str0ng!Passw0rd", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	pass := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := r.Context().Value("claims").(*jwt.MapClaims)
		if claims == nil {
			w.WriteHeader(http.StatusTeapot)
			return
		}
		w.Header().Set("X-User", (*claims)["username"].(string))
		w.WriteHeader(http.StatusNoContent)
	})
	handler := JWTAuth(users)(pass)

	get := func(bearer string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/pipelines", nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	// Without the hook: a non-JWT bearer is refused, as always.
	prev := BearerTokenResolverFunc
	BearerTokenResolverFunc = nil
	t.Cleanup(func() { BearerTokenResolverFunc = prev })
	if rec := get("brk_not_a_jwt"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no hook: %d, want 401", rec.Code)
	}

	// With the hook: the token it recognizes authenticates with the
	// claims it supplies; ones it refuses stay 401.
	BearerTokenResolverFunc = func(token string) (*jwt.MapClaims, bool) {
		if token == "brk_known_token" {
			return &jwt.MapClaims{"sub": "u1", "username": "tokenuser", "role": "admin"}, true
		}
		return nil, false
	}
	rec := get("brk_known_token")
	if rec.Code != http.StatusNoContent || rec.Header().Get("X-User") != "tokenuser" {
		t.Fatalf("resolved token: %d user=%q", rec.Code, rec.Header().Get("X-User"))
	}
	if rec := get("brk_unknown_token"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("refused token: %d, want 401", rec.Code)
	}

	// A real JWT never consults the hook (it would panic loudly here).
	BearerTokenResolverFunc = func(string) (*jwt.MapClaims, bool) {
		t.Fatal("resolver consulted for a valid JWT")
		return nil, false
	}
	u2, err := users.CreateUser("someone2", "Str0ng!Passw0rd", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	token, err := GenerateToken(u2)
	if err != nil {
		t.Fatal(err)
	}
	if rec := get(token); rec.Code != http.StatusNoContent {
		t.Fatalf("JWT path: %d", rec.Code)
	}
}
