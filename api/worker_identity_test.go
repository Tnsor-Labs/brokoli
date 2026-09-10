package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The path matcher is the security boundary for the bypass, so it is
// worth pinning hard. It must admit exactly the blob endpoints and
// nothing that merely looks like them.
func TestDataPlaneBlobRequestMatchesOnlyTheBlobRoutes(t *testing.T) {
	cases := map[string]struct {
		method, path string
		want         bool
	}{
		"get one object":     {http.MethodGet, "/api/runs/r1/nodes/n1/attempts/0/blobs/sha256:abc", true},
		"put one object":     {http.MethodPut, "/api/runs/r1/nodes/n1/attempts/0/blobs/sha256:abc", true},
		"post to collection": {http.MethodPost, "/api/runs/r1/nodes/n1/attempts/0/blobs", true},

		// Everything below must NOT bypass the session middleware.
		"delete is not a blob op":  {http.MethodDelete, "/api/runs/r1/nodes/n1/attempts/0/blobs/x", false},
		"the run itself":           {http.MethodGet, "/api/runs/r1", false},
		"pipelines":                {http.MethodGet, "/api/pipelines", false},
		"a node preview":           {http.MethodGet, "/api/runs/r1/nodes/n1/preview", false},
		"deeper than the route":    {http.MethodGet, "/api/runs/r1/nodes/n1/attempts/0/blobs/x/y", false},
		"blobs at the wrong depth": {http.MethodGet, "/api/blobs/x", false},
		"a lookalike prefix":       {http.MethodGet, "/api/runsX/r1/nodes/n1/attempts/0/blobs/x", false},
		"not under /api":           {http.MethodGet, "/runs/r1/nodes/n1/attempts/0/blobs/x", false},

		// An encoded separator makes the DECODED path wear the blob
		// shape while the router, which dispatches on RawPath, sends the
		// request somewhere else. Matching the decoded path would hand a
		// bypass to a route that is not a blob route at all: this one
		// reaches POST /api/runs/{id}/cancel. Same defect class as
		// GHSA-jxjf-p7pv-22m9.
		"encoded separator smuggling the shape": {
			http.MethodPost, "/api/runs/r1%2Fnodes%2Fn1%2Fattempts%2F0%2Fblobs/cancel", false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := isDataPlaneBlobRequest(httptest.NewRequest(tc.method, tc.path, nil))
			if got != tc.want {
				t.Errorf("isDataPlaneBlobRequest(%s %s) = %v, want %v", tc.method, tc.path, got, tc.want)
			}
		})
	}
}

// A session already in context wins, so a human request is unchanged and
// the worker path can never shadow it.
func TestBlobAuthPrefersAnExistingSession(t *testing.T) {
	prev := WorkerTokenOrgResolver
	t.Cleanup(func() { WorkerTokenOrgResolver = prev })
	WorkerTokenOrgResolver = func(string) (string, bool) { return "org-from-token", true }

	var seen string
	h := blobAuth(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = GetOrgIDFromRequest(r)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/runs/r/nodes/n/attempts/0/blobs/x", nil)
	req.Header.Set("Authorization", "Bearer brk_wp_something")
	req = req.WithContext(withOrgForTest(req, "org-from-session"))
	h.ServeHTTP(httptest.NewRecorder(), req)

	if seen != "org-from-session" {
		t.Errorf("org = %q, want the session's; a worker token must not shadow a real session", seen)
	}
}

// The worker path: an opaque token resolves to a tenant, and ONLY a
// tenant. No role is stamped, because a role would make this identity
// read as a user to anything that inspects claims.
func TestBlobAuthStampsOnlyTheTenant(t *testing.T) {
	prev := WorkerTokenOrgResolver
	t.Cleanup(func() { WorkerTokenOrgResolver = prev })
	WorkerTokenOrgResolver = func(tok string) (string, bool) {
		if tok == "brk_wp_good" {
			return "org-9", true
		}
		return "", false
	}

	var org string
	var claims interface{}
	h := blobAuth(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		org = GetOrgIDFromRequest(r)
		claims = r.Context().Value("claims")
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/runs/r/nodes/n/attempts/0/blobs/x", nil)
	req.Header.Set("Authorization", "Bearer brk_wp_good")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if org != "org-9" {
		t.Errorf("org = %q, want org-9", org)
	}
	if claims != nil {
		t.Error("blobAuth stamped claims; this identity must not read as a user anywhere")
	}
}

// An unrecognised token stamps nothing. The endpoint then reports a
// missing tenant, which is the honest answer, rather than the middleware
// inventing one.
func TestBlobAuthRejectsAnUnknownToken(t *testing.T) {
	prev := WorkerTokenOrgResolver
	t.Cleanup(func() { WorkerTokenOrgResolver = prev })
	WorkerTokenOrgResolver = func(string) (string, bool) { return "", false }

	var org string
	h := blobAuth(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		org = GetOrgIDFromRequest(r)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/runs/r/nodes/n/attempts/0/blobs/x", nil)
	req.Header.Set("Authorization", "Bearer nonsense")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if org != "" {
		t.Errorf("org = %q, want empty for an unrecognised token", org)
	}
}

// A deployment that registered no resolver must behave as it did before
// this existed.
func TestBlobAuthWithNoResolverConfigured(t *testing.T) {
	prev := WorkerTokenOrgResolver
	t.Cleanup(func() { WorkerTokenOrgResolver = prev })
	WorkerTokenOrgResolver = nil

	var org string
	h := blobAuth(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		org = GetOrgIDFromRequest(r)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/runs/r/nodes/n/attempts/0/blobs/x", nil)
	req.Header.Set("Authorization", "Bearer brk_wp_good")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if org != "" {
		t.Errorf("org = %q, want empty when no resolver is registered", org)
	}
}
