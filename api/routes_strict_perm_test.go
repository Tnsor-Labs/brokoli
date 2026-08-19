package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/pkg/sodp"
	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
)

// reqAsRole builds a request carrying JWT claims with the given role,
// matching what JWTAuth would have already populated by the time
// requirePerm/requireStrictPerm run.
func reqAsRole(method, path, role string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader("{}"))
	claims := &jwt.MapClaims{"sub": "u1", "role": role}
	return r.WithContext(context.WithValue(r.Context(), "claims", claims))
}

// TestRequireStrictPerm_EditorDenied_AdminAllowed pins the RBAC fallback
// fix: requirePerm's OSS fallback only ever blocked "viewer" from write
// permissions, silently treating "editor" as equivalent to "admin" for
// every write route -- including plugin install (arbitrary code
// execution in this process), system/purge (mass deletion of run
// history), and notification/webhook settings. requireStrictPerm closes
// that specifically for those operations without touching the ordinary
// pipeline/connection/variable routes editor is supposed to have.
func TestRequireStrictPerm_EditorDenied_AdminAllowed(t *testing.T) {
	s := newOrgCheckStore(t)
	e := engine.NewEngine(s)
	t.Cleanup(func() { _ = e.Close(context.Background()) })
	r := chi.NewRouter()
	RegisterRoutes(r, s, e, sodp.NewServer(), nil, nil, nil) // ext=nil: OSS fallback path, not EE Team

	cases := []struct {
		name   string
		method string
		path   string
	}{
		{"plugin install", http.MethodPost, "/api/plugins"},
		{"plugin install by name", http.MethodPost, "/api/plugins/index/some-plugin"},
		{"plugin remove", http.MethodDelete, "/api/plugins/some-plugin"},
		{"system purge", http.MethodPost, "/api/system/purge"},
		{"notification settings update", http.MethodPut, "/api/settings/notifications"},
		{"notification settings delete", http.MethodDelete, "/api/settings/notifications"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A plain editor -- previously indistinguishable from admin
			// for every write route -- must now be rejected for these
			// specifically, before the handler is ever reached.
			editorReq := reqAsRole(tc.method, tc.path, "editor")
			editorRec := httptest.NewRecorder()
			r.ServeHTTP(editorRec, editorReq)
			if editorRec.Code != http.StatusForbidden {
				t.Errorf("%s as editor: status = %d, want 403 (insufficient permissions); body=%s",
					tc.name, editorRec.Code, editorRec.Body.String())
			}

			// An admin must still get past the permission check (may
			// still fail downstream for unrelated reasons -- e.g. an
			// empty plugin body -- but never with 403 "admin role
			// required").
			adminReq := reqAsRole(tc.method, tc.path, "admin")
			adminRec := httptest.NewRecorder()
			r.ServeHTTP(adminRec, adminReq)
			if adminRec.Code == http.StatusForbidden {
				t.Errorf("%s as admin: status = 403, want anything but 403 (admin must pass the permission check); body=%s",
					tc.name, adminRec.Body.String())
			}
		})
	}
}

// TestRequireStrictPerm_ViewerStillDenied confirms the pre-existing
// viewer-denial behavior (requirePerm's original, correct check) is
// unaffected by this fix.
func TestRequireStrictPerm_ViewerStillDenied(t *testing.T) {
	s := newOrgCheckStore(t)
	e := engine.NewEngine(s)
	t.Cleanup(func() { _ = e.Close(context.Background()) })
	r := chi.NewRouter()
	RegisterRoutes(r, s, e, sodp.NewServer(), nil, nil, nil)

	req := reqAsRole(http.MethodPost, "/api/system/purge", "viewer")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("system/purge as viewer: status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}
