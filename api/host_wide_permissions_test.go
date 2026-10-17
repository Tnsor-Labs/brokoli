package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/sodp"
)

// Operations on the whole server ask for a permission of their own, so a
// distribution can keep them from administrators who are scoped to less
// than the server. settings.edit does not reach them; each is refused when
// exactly its own permission is withheld, and only then.
func TestHostWideOperationsAskForTheirOwnPermission(t *testing.T) {
	routes := []struct {
		method, path string
		perm         models.Permission
	}{
		{http.MethodPost, "/api/plugins", models.PermPluginsManage},
		{http.MethodPost, "/api/plugins/index/some-plugin", models.PermPluginsManage},
		{http.MethodDelete, "/api/plugins/some-plugin", models.PermPluginsManage},
		{http.MethodGet, "/api/settings/notifications", models.PermNotificationsManage},
		{http.MethodPut, "/api/settings/notifications", models.PermNotificationsManage},
		{http.MethodPost, "/api/settings/notifications/test", models.PermNotificationsManage},
		{http.MethodDelete, "/api/settings/notifications", models.PermNotificationsManage},
	}
	for _, withheld := range []models.Permission{models.PermPluginsManage, models.PermNotificationsManage, models.PermSettingsEdit} {
		s := newOrgCheckStore(t)
		e := engine.NewEngine(s)
		r := chi.NewRouter()
		RegisterRoutes(r, s, e, sodp.NewServer(), nil, &extensions.Registry{Team: refuseOneTeam{refuse: string(withheld)}}, nil)
		for _, rt := range routes {
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, reqAsRole(rt.method, rt.path, "admin"))
			refused := rec.Code == http.StatusForbidden && strings.Contains(rec.Body.String(), string(withheld))
			if want := rt.perm == withheld; refused != want {
				t.Errorf("withholding %s, %s %s: status %d %q, refused = %v, want %v",
					withheld, rt.method, rt.path, rec.Code, strings.TrimSpace(rec.Body.String()), refused, want)
			}
		}
		_ = e.Close(context.Background())
	}
}

// Testing an arbitrary connection string opens a connection from the
// server, so it asks for connections.test like testing a saved one does.
func TestTestConnectionAsksForConnectionsTest(t *testing.T) {
	s := newOrgCheckStore(t)
	e := engine.NewEngine(s)
	t.Cleanup(func() { _ = e.Close(context.Background()) })
	r := chi.NewRouter()
	RegisterRoutes(r, s, e, sodp.NewServer(), nil, &extensions.Registry{Team: refuseOneTeam{refuse: string(models.PermConnectionsTest)}}, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, reqAsRole(http.MethodPost, "/api/test-connection", "admin"))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), string(models.PermConnectionsTest)) {
		t.Errorf("POST /api/test-connection without connections.test: %d %q, want 403 naming it", rec.Code, rec.Body.String())
	}
}

func TestBuiltInAdminHoldsTheNewPermissions(t *testing.T) {
	for _, role := range models.DefaultRoles() {
		for _, p := range []models.Permission{models.PermPluginsManage, models.PermNotificationsManage} {
			if got, want := role.HasPermission(p), role.ID == "admin"; got != want {
				t.Errorf("built-in %s holds %s = %v, want %v", role.ID, p, got, want)
			}
		}
	}
}
