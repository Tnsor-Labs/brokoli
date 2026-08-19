package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/pkg/sodp"
	"github.com/go-chi/chi/v5"
)

// TestDLQResolve_ViewerDenied pins the fix for the DLQ-resolve route,
// which previously had no permission check at all beyond org
// membership -- a viewer could mutate DLQ state.
func TestDLQResolve_ViewerDenied(t *testing.T) {
	s := newOrgCheckStore(t)
	e := engine.NewEngine(s)
	t.Cleanup(func() { _ = e.Close(context.Background()) })
	r := chi.NewRouter()
	RegisterRoutes(r, s, e, sodp.NewServer(), nil, nil, nil)

	req := reqAsRole(http.MethodPost, "/api/pipelines/some-pipeline/dlq/some-entry/resolve", "viewer")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("DLQ resolve as viewer: status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}
