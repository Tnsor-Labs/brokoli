package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/go-chi/chi/v5"
)

// NewServer mounts JWTAuth only when a user store exists:
//
//	if userStore != nil {
//	    r.Use(JWTAuth(userStore))
//	}
//
// So when the user store cannot be built — RawDB is not a *sql.DB, or
// NewUserStore returns an error, which a database still starting up will
// produce — no authentication middleware is mounted at all. Every request
// then reaches handlers with no claims, and HasPermission reads absent
// claims as "open mode, allow everything".
//
// The result is a server that answers permission-gated routes to anyone,
// on a database that may well be full of users, marked only by a WARNING
// line in the log.
func TestPermissionGateWithoutAuthMiddleware(t *testing.T) {
	pc := NewPermissionChecker(nil)

	// A request as an anonymous caller would arrive with no claims,
	// exactly as it does when JWTAuth was never mounted.
	r := chi.NewRouter()
	reached := false
	r.With(pc.RequirePermission(models.PermSettingsEdit)).
		Get("/api/settings", func(w http.ResponseWriter, _ *http.Request) {
			reached = true
			w.WriteHeader(http.StatusOK)
		})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/settings", nil))

	if reached {
		t.Errorf("an unauthenticated request passed a settings.edit gate (status %d).\n"+
			"HasPermission treats absent claims as open mode. That is only safe while "+
			"JWTAuth is guaranteed to be mounted — and NewServer skips it whenever the "+
			"user store could not be built.", rec.Code)
	}
}

// The other half of the contract: a genuinely unconfigured system must
// still be usable, or this fix locks everyone out of a fresh install.
// The difference is that open mode is now something JWTAuth decides and
// marks, rather than something inferred from a request having no claims.
func TestOpenModeStillPassesPermissionGate(t *testing.T) {
	pc := NewPermissionChecker(nil)

	r := chi.NewRouter()
	reached := false
	r.With(pc.RequirePermission(models.PermSettingsEdit)).
		Get("/api/settings", func(w http.ResponseWriter, _ *http.Request) {
			reached = true
			w.WriteHeader(http.StatusOK)
		})

	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	req = req.WithContext(withOpenMode(req.Context()))

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if !reached {
		t.Errorf("a marked open-mode request was refused (status %d); a fresh "+
			"install must still be able to set itself up", rec.Code)
	}
}
