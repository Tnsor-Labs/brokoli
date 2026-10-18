package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A distribution's signed webhook (GitHub's, say) has no session: its
// signature is the authentication. Registered, its exact POST path passes
// the auth middlewares and nothing near it does.
func TestRegisteredSignedWebhookPathIsExactlyExempt(t *testing.T) {
	RegisterSignedWebhookPath("/api/integrations/github/webhook")

	cases := []struct {
		name   string
		method string
		target string
		exempt bool
	}{
		{"the registered path", http.MethodPost, "/api/integrations/github/webhook", true},
		{"another method", http.MethodGet, "/api/integrations/github/webhook", false},
		{"a trailing segment", http.MethodPost, "/api/integrations/github/webhook/x", false},
		{"a prefix of it", http.MethodPost, "/api/integrations/github", false},
		{"a trailing slash", http.MethodPost, "/api/integrations/github/webhook/", false},
		{"an encoded separator", http.MethodPost, "/api/integrations/github%2Fwebhook", false},
		{"a path never registered", http.MethodPost, "/api/integrations/slack/webhook", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.target, nil)
			if got := isWebhookTriggerRequest(r); got != tc.exempt {
				t.Errorf("isWebhookTriggerRequest(%s %s) = %v, want %v", tc.method, tc.target, got, tc.exempt)
			}
		})
	}
}

// Through both middlewares, as a server stacks them: the registered POST
// reaches its handler with no credentials, and the routes beside it do not.
func TestSignedWebhookReachesItsHandlerThroughAuth(t *testing.T) {
	RegisterSignedWebhookPath("/api/integrations/github/webhook")
	auth := NewAuthConfig()
	auth.AddKey("brk_test", "test key")
	users := newTestUserStore(t)
	// A server with a user is past first-run setup, as production is.
	if _, err := users.CreateUser("admin", "Str0ng!Passw0rd", RoleAdmin); err != nil {
		t.Fatal(err)
	}

	reached := map[string]bool{}
	handler := APIKeyAuth(auth)(JWTAuth(users)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached[r.Method+" "+r.URL.Path] = true
		w.WriteHeader(http.StatusNoContent)
	})))

	for _, c := range []struct {
		method, target string
		reach          bool
	}{
		{http.MethodPost, "/api/integrations/github/webhook", true},
		{http.MethodGet, "/api/integrations/github/webhook", false},
		{http.MethodPost, "/api/integrations/github/webhooks", false},
		{http.MethodPost, "/api/pipelines", false},
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(c.method, c.target, nil))
		if got := reached[c.method+" "+c.target]; got != c.reach {
			t.Errorf("%s %s reached the handler = %v, want %v (status %d)", c.method, c.target, got, c.reach, rec.Code)
		}
		if !c.reach && rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: status %d, want 401", c.method, c.target, rec.Code)
		}
	}
}
