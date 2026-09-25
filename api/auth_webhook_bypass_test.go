package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
)

// Both auth middlewares exempt the webhook trigger routes, which carry
// their own token. The exemption used to be
//
//	strings.Contains(r.URL.Path, "/webhook") && r.Method == "POST"
//
// which is a substring test against the DECODED path, while chi
// dispatches on RawPath when it is set. Two shapes satisfied the test
// while routing somewhere else entirely, and both are covered below.
func TestWebhookAuthExemptionMatchesShapeNotSubstring(t *testing.T) {
	cases := []struct {
		name   string
		method string
		target string
		exempt bool
	}{
		// The genuine routes.
		{"pipeline webhook", http.MethodPost, "/api/pipelines/p123/webhook", true},
		{"git sync webhook", http.MethodPost, "/api/git/webhook", true},
		{"pipeline id needing encoding", http.MethodPost, "/api/pipelines/p%20123/webhook", true},

		// The bypasses. Each of these was exempt before the fix and must
		// not be now.
		{"parameter valued webhook", http.MethodPost, "/api/pipelines/webhook/backfill", false},
		{"encoded separator", http.MethodPost, "/api/pipelines/p123%2Fwebhook/backfill", false},
		{"encoded separator on run", http.MethodPost, "/api/pipelines/p123%2Fwebhook/run", false},
		{"webhook as a run id", http.MethodPost, "/api/runs/webhook/cancel", false},
		{"trailing segment", http.MethodPost, "/api/pipelines/p123/webhook/extra", false},
		{"webhook inside a longer segment", http.MethodPost, "/api/pipelines/my-webhook/backfill", false},

		// Shape alone is not enough.
		{"wrong method", http.MethodGet, "/api/pipelines/p123/webhook", false},
		{"wrong collection", http.MethodPost, "/api/runs/p123/webhook", false},
		{"too shallow", http.MethodPost, "/api/webhook", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.target, nil)
			if got := isWebhookTriggerRequest(r); got != tc.exempt {
				t.Errorf("isWebhookTriggerRequest(%s %s) = %v, want %v\n"+
					"  URL.Path=%q RawPath=%q",
					tc.method, tc.target, got, tc.exempt, r.URL.Path, r.URL.RawPath)
			}
		})
	}
}

// The unit test above pins the predicate. This one pins what the
// predicate is for: that a crafted path does not actually get past
// APIKeyAuth to a handler.
func TestAPIKeyAuthRefusesWebhookShapedBypass(t *testing.T) {
	auth := NewAuthConfig()
	auth.AddKey("brk_testkey", "test")

	newRouter := func(reached *bool) http.Handler {
		r := chi.NewRouter()
		r.Use(APIKeyAuth(auth))
		handler := func(w http.ResponseWriter, _ *http.Request) {
			*reached = true
			w.WriteHeader(http.StatusOK)
		}
		r.Post("/api/pipelines/{id}/backfill", handler)
		r.Post("/api/pipelines/{id}/webhook", handler)
		return r
	}

	cases := []struct {
		name   string
		target string
		reach  bool
	}{
		{"ordinary route needs a key", "/api/pipelines/p123/backfill", false},
		{"parameter valued webhook", "/api/pipelines/webhook/backfill", false},
		{"encoded separator", "/api/pipelines/p123%2Fwebhook/backfill", false},
		{"real webhook is still exempt", "/api/pipelines/p123/webhook", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reached := false
			rec := httptest.NewRecorder()
			newRouter(&reached).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, tc.target, nil))
			if reached != tc.reach {
				t.Errorf("POST %s reached the handler = %v, want %v (status %d)",
					tc.target, reached, tc.reach, rec.Code)
			}
		})
	}
}

// The open-source permission gate used to read "no claims" as open mode
// and allow the request. That is the same ambiguity HasPermission was
// corrected for: absent claims is equally what an unauthenticated
// request looks like, so anything that got past the auth middlewares
// also got past this.
func TestFallbackPermissionGateRefusesUnauthenticated(t *testing.T) {
	reached := false
	r := chi.NewRouter()
	r.With(fallbackPermissionMiddleware(models.PermPipelinesRun)).
		Post("/api/pipelines/{id}/backfill", func(w http.ResponseWriter, _ *http.Request) {
			reached = true
			w.WriteHeader(http.StatusOK)
		})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/pipelines/p123/backfill", nil))

	if reached {
		t.Errorf("an unauthenticated request passed a pipelines.run gate (status %d); "+
			"absent claims must not be read as open mode", rec.Code)
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

// The other half of the contract, matching TestOpenModeStillPassesPermissionGate:
// a genuinely unconfigured system must still be able to set itself up.
func TestFallbackPermissionGateAllowsMarkedOpenMode(t *testing.T) {
	reached := false
	r := chi.NewRouter()
	r.With(fallbackPermissionMiddleware(models.PermPipelinesRun)).
		Post("/api/pipelines/{id}/backfill", func(w http.ResponseWriter, _ *http.Request) {
			reached = true
			w.WriteHeader(http.StatusOK)
		})

	req := httptest.NewRequest(http.MethodPost, "/api/pipelines/p123/backfill", nil)
	req = req.WithContext(withOpenMode(req.Context()))

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if !reached {
		t.Errorf("a marked open-mode request was refused (status %d); a fresh install "+
			"must still be able to set itself up", rec.Code)
	}
}

// Existing behaviour that must survive the change: a viewer is refused a
// write permission, and any other recognised role is allowed it.
func TestFallbackPermissionGatePreservesRoleRules(t *testing.T) {
	cases := []struct {
		role  string
		perm  models.Permission
		reach bool
	}{
		{"viewer", models.PermPipelinesRun, false},
		{"viewer", models.PermPipelinesView, true},
		{"editor", models.PermPipelinesRun, true},
		{"admin", models.PermPipelinesRun, true},
	}

	for _, tc := range cases {
		t.Run(tc.role+"/"+string(tc.perm), func(t *testing.T) {
			reached := false
			r := chi.NewRouter()
			r.With(fallbackPermissionMiddleware(tc.perm)).
				Post("/api/thing", func(w http.ResponseWriter, _ *http.Request) {
					reached = true
					w.WriteHeader(http.StatusOK)
				})

			claims := &jwt.MapClaims{"sub": "u1", "username": "u1", "role": tc.role}
			req := httptest.NewRequest(http.MethodPost, "/api/thing", nil)
			req = req.WithContext(contextWithClaims(req.Context(), claims))

			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if reached != tc.reach {
				t.Errorf("role %q with %s reached = %v, want %v (status %d)",
					tc.role, tc.perm, reached, tc.reach, rec.Code)
			}
		})
	}
}
