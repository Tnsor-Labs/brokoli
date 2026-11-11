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

// serveWithChecker is serveWithRole through the gate as RegisterRoutes
// builds it, with pc reading the roles table of a real store.
func serveWithChecker(t *testing.T, pc *PermissionChecker, role string, perm models.Permission) int {
	t.Helper()
	h := fallbackPermissionMiddlewareWith(perm, pc)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	claims := jwt.MapClaims{"sub": "u1", "role": role}
	req := httptest.NewRequest(http.MethodGet, "/api/pipelines", nil)
	req = req.WithContext(context.WithValue(req.Context(), "claims", &claims))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// The gate decided by role name: admin and editor everything, viewer the
// reads, every other role nothing. So an operator, told by
// /api/auth/me/permissions that it may run a pipeline, was refused, and a
// custom role got nothing it listed (#864). It now grants what the roles
// table lists.
func TestFallbackGrantsWhatTheRoleLists(t *testing.T) {
	s := newDepAPIStore(t)
	if err := s.CreateRole(&models.Role{ID: "runner", Name: "Runner", Permissions: []models.Permission{models.PermPipelinesView, models.PermPipelinesRun}}); err != nil {
		t.Fatal(err)
	}
	pc := NewPermissionChecker(s)
	cases := []struct {
		role string
		perm models.Permission
		want int
	}{
		{"operator", models.PermPipelinesRun, http.StatusNoContent},
		{"operator", models.PermRunsCancel, http.StatusNoContent},
		{"operator", models.PermRunsResume, http.StatusNoContent},
		{"operator", models.PermPipelinesEdit, http.StatusForbidden},
		{"runner", models.PermPipelinesRun, http.StatusNoContent},
		{"runner", models.PermPipelinesDelete, http.StatusForbidden},
		// Editors hold what the editor role lists, not everything.
		{"editor", models.PermPipelinesDelete, http.StatusNoContent},
		{"editor", models.PermSettingsManageUsers, http.StatusForbidden},
		{"editor", models.PermVariablesViewSecret, http.StatusForbidden},
		{"editor", models.PermWorkspacesManageTokens, http.StatusForbidden},
		// Viewers read what the viewer role lists, those three included.
		{"viewer", models.PermAuditView, http.StatusNoContent},
		{"viewer", models.PermGitSyncView, http.StatusNoContent},
		{"viewer", models.PermWorkspacesView, http.StatusNoContent},
		{"viewer", models.PermConnectionsTest, http.StatusForbidden},
		{"admin", models.PermSettingsManageUsers, http.StatusNoContent},
		{"superadmin", models.PermPluginsManage, http.StatusNoContent},
		// A role the table does not know is refused, reads included.
		{"owner", models.PermPipelinesView, http.StatusForbidden},
		{"", models.PermPipelinesView, http.StatusForbidden},
	}
	for _, tc := range cases {
		if code := serveWithChecker(t, pc, tc.role, tc.perm); code != tc.want {
			t.Errorf("%s %s: %d, want %d", tc.role, tc.perm, code, tc.want)
		}
	}
}

// The gate and /api/auth/me/permissions give one answer, for every role
// and permission: the UI offers exactly what the routes allow.
func TestFallbackAgreesWithThePermissionsEndpoint(t *testing.T) {
	s := newDepAPIStore(t)
	pc := NewPermissionChecker(s)
	roles := []string{"superadmin", "owner"}
	for _, r := range models.DefaultRoles() {
		roles = append(roles, r.ID)
	}
	for _, role := range roles {
		claims := jwt.MapClaims{"sub": "u1", "role": role}
		req := httptest.NewRequest(http.MethodGet, "/api/auth/me/permissions", nil)
		listed := map[models.Permission]bool{}
		for _, p := range pc.requestPermissions(req, &claims) {
			listed[p] = true
		}
		for _, perm := range models.AllPermissions() {
			allowed := serveWithChecker(t, pc, role, perm) == http.StatusNoContent
			if allowed != listed[perm] {
				t.Errorf("%s %s: route allows %v, endpoint lists %v", role, perm, allowed, listed[perm])
			}
		}
	}
}

// A role the table does not know holds nothing, and the endpoint says so
// with an empty list, not null and not a viewer's permissions.
func TestAnUnknownRoleHoldsNothing(t *testing.T) {
	pc := NewPermissionChecker(newDepAPIStore(t))
	for _, role := range []string{"owner", "", "Admin"} {
		got := pc.GetUserPermissions(role, "")
		if got == nil || len(got) != 0 {
			t.Errorf("%q holds %v, want an empty list", role, got)
		}
	}
}
