package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/golang-jwt/jwt/v5"

	"github.com/Tnsor-Labs/brokoli/models"
)

// The open-source permission fallback denied exactly one case, a viewer
// attempting a write, and let everything else through. So any role the
// build did not recognise -- a typo, a value from a newer build, a role
// an operator invented, or the empty string -- passed every
// requirePerm-gated route (#527).
//
// Both directions per role, because half of this is "the right people
// still get in" and the other half is the actual defect.

func serveWithRole(t *testing.T, role string, perm models.Permission) int {
	t.Helper()
	reached := false
	h := fallbackPermissionMiddleware(perm)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusNoContent)
	}))

	claims := jwt.MapClaims{"sub": "u1", "role": role}
	req := httptest.NewRequest(http.MethodGet, "/api/pipelines", nil)
	req = req.WithContext(context.WithValue(req.Context(), "claims", &claims))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if reached != (rec.Code == http.StatusNoContent) {
		t.Fatalf("handler reached=%v but status=%d", reached, rec.Code)
	}
	return rec.Code
}

func TestFallbackRefusesUnrecognisedRoles(t *testing.T) {
	// Every one of these passed every gate before this fix.
	for _, role := range []string{
		"",
		"owner",           // plausible, and not a role this build has
		"Admin",           // case matters; the comparison is exact
		"admin ",          // trailing space
		"viewer-but-more", // near miss
		"root",
	} {
		t.Run("role="+role, func(t *testing.T) {
			if code := serveWithRole(t, role, models.PermPipelinesEdit); code != http.StatusForbidden {
				t.Errorf("write with role %q = %d, want 403", role, code)
			}
			// Reads too: an unrecognised role is not a viewer.
			if code := serveWithRole(t, role, models.PermPipelinesView); code != http.StatusForbidden {
				t.Errorf("read with role %q = %d, want 403", role, code)
			}
		})
	}
}

func TestFallbackStillAdmitsTheRolesItShould(t *testing.T) {
	cases := []struct {
		role  string
		perm  models.Permission
		want  int
		notes string
	}{
		{"admin", models.PermPipelinesEdit, http.StatusNoContent, "admin writes"},
		{"admin", models.PermPipelinesView, http.StatusNoContent, "admin reads"},
		{"superadmin", models.PermPipelinesEdit, http.StatusNoContent, "superadmin writes"},
		{"editor", models.PermPipelinesEdit, http.StatusNoContent, "editor writes"},
		{"editor", models.PermPipelinesView, http.StatusNoContent, "editor reads"},
		{"viewer", models.PermPipelinesView, http.StatusNoContent, "viewer reads"},
		{"viewer", models.PermPipelinesEdit, http.StatusForbidden, "viewer cannot write"},
	}
	for _, tc := range cases {
		t.Run(tc.notes, func(t *testing.T) {
			if code := serveWithRole(t, tc.role, tc.perm); code != tc.want {
				t.Errorf("%s: status = %d, want %d", tc.notes, code, tc.want)
			}
		})
	}
}
