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

// #754: routes registered without a crypto config used to encrypt with
// 32 zero bytes, storing credentials anyone could decrypt. With no key
// there is nothing to encrypt with, so saving a credential must fail and
// store nothing.
func TestRoutesWithoutACryptoConfigRefuseToStoreACredential(t *testing.T) {
	s := newOrgCheckStore(t)
	e := engine.NewEngine(s)
	t.Cleanup(func() { _ = e.Close(context.Background()) })
	r := chi.NewRouter()
	RegisterRoutes(r, s, e, sodp.NewServer(), nil, nil, nil)

	body := `{"conn_id":"warehouse","type":"postgres","host":"db","password":"hunter2"}`
	req := httptest.NewRequest(http.MethodPost, "/api/connections", strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), "claims", &jwt.MapClaims{"sub": "u1", "role": "admin"}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code < 400 {
		t.Fatalf("saving a password with no encryption key: status = %d, want an error; body=%s", rec.Code, rec.Body.String())
	}
	if c, err := s.GetConnection("warehouse"); err == nil && c != nil {
		t.Fatalf("the connection was stored anyway, password_ref=%q", c.PasswordRef)
	}
}
