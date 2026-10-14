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

// Curating templates asks for templates.manage, so a distribution decides
// who may change the templates every user starts from; a role named "admin"
// is not enough on its own. Listing them asks for nothing.
func TestTemplateCurationAsksForItsPermission(t *testing.T) {
	s := newOrgCheckStore(t)
	e := engine.NewEngine(s)
	t.Cleanup(func() { _ = e.Close(context.Background()) })
	r := chi.NewRouter()
	RegisterRoutes(r, s, e, sodp.NewServer(), nil, &extensions.Registry{Team: refuseOneTeam{refuse: string(models.PermTemplatesManage)}}, nil)

	for _, tc := range []struct {
		method, path string
		refused      bool
	}{
		{http.MethodPost, "/api/templates", true},
		{http.MethodPut, "/api/templates/hello-world", true},
		{http.MethodDelete, "/api/templates/hello-world", true},
		{http.MethodGet, "/api/templates", false},
	} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, reqAsRole(tc.method, tc.path, "admin"))
		refused := rec.Code == http.StatusForbidden && strings.Contains(rec.Body.String(), "templates.manage")
		if refused != tc.refused {
			t.Errorf("%s %s: status %d %q, refused for templates.manage = %v, want %v",
				tc.method, tc.path, rec.Code, strings.TrimSpace(rec.Body.String()), refused, tc.refused)
		}
	}
}
