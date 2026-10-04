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

// refuseOneTeam is a distribution's permission hook that refuses exactly one
// permission and allows every other.
type refuseOneTeam struct{ refuse string }

func (refuseOneTeam) Enabled() bool                           { return true }
func (refuseOneTeam) RegisterRoutes(interface{}, interface{}) {}
func (refuseOneTeam) MigrateDB(interface{})                   {}
func (t refuseOneTeam) PermissionMiddleware(permission string) interface{} {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if permission == t.refuse {
				http.Error(w, "refused: "+permission, http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Changing a secret store asks for secret_stores.manage, not a connection
// permission, so a distribution can hold it to a stricter rule; viewing and
// testing do not ask for it.
func TestSecretStoreWritesAskForTheirOwnPermission(t *testing.T) {
	s := newOrgCheckStore(t)
	e := engine.NewEngine(s)
	t.Cleanup(func() { _ = e.Close(context.Background()) })
	r := chi.NewRouter()
	RegisterRoutes(r, s, e, sodp.NewServer(), nil, &extensions.Registry{Team: refuseOneTeam{refuse: string(models.PermSecretStoresManage)}}, nil)

	for _, tc := range []struct {
		method, path string
		refused      bool
	}{
		{http.MethodPost, "/api/secret-stores", true},
		{http.MethodPut, "/api/secret-stores/x", true},
		{http.MethodDelete, "/api/secret-stores/x", true},
		{http.MethodGet, "/api/secret-stores", false},
		{http.MethodPost, "/api/secret-stores/x/test", false},
	} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, reqAsRole(tc.method, tc.path, "admin"))
		refused := rec.Code == http.StatusForbidden && strings.Contains(rec.Body.String(), "secret_stores.manage")
		if refused != tc.refused {
			t.Errorf("%s %s: status %d %q, refused for secret_stores.manage = %v, want %v",
				tc.method, tc.path, rec.Code, strings.TrimSpace(rec.Body.String()), refused, tc.refused)
		}
	}
}
