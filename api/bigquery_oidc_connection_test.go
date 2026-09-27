package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Tnsor-Labs/brokoli/crypto"
	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/pkg/identity"
	"github.com/Tnsor-Labs/brokoli/pkg/sodp"
	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
)

// stopTokenSource records the request and refuses, so a connection test
// proves it reached the deployment's token source without any network.
type stopTokenSource struct {
	mu   sync.Mutex
	reqs []identity.TokenRequest
}

var errTokenSourceReached = errors.New("token source reached")

func (s *stopTokenSource) Token(_ context.Context, req identity.TokenRequest) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqs = append(s.reqs, req)
	return "", errTokenSourceReached
}

func adminRequest(method, path, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	return req.WithContext(context.WithValue(req.Context(), "claims", &jwt.MapClaims{"sub": "u1", "role": "admin"}))
}

// Test connection on an oidc BigQuery connection authenticates with the
// deployment's token source, which the router takes from the engine, for
// the connection's own workspace and immutable ID. It used to be given no
// source, so the test could only ever fail.
func TestBigQueryOIDCConnectionTestUsesTheDeploymentTokenSource(t *testing.T) {
	t.Setenv("BROKOLI_BIGQUERY_ENDPOINT", "")
	s := newOrgCheckStore(t)
	e := engine.NewEngine(s)
	t.Cleanup(func() { _ = e.Close(context.Background()) })
	tokens := &stopTokenSource{}
	e.ConnResolver.SetTokenSource(tokens)
	key := make([]byte, 32)
	key[0] = 1
	r := chi.NewRouter()
	RegisterRoutes(r, s, e, sodp.NewServer(), nil, nil, nil, &crypto.Config{Key: key})

	extra, _ := json.Marshal(map[string]string{
		"auth_method": "oidc",
		"provider":    "//iam.googleapis.com/projects/123456789/locations/global/workloadIdentityPools/brokoli/providers/runs",
	})
	body, _ := json.Marshal(map[string]string{"conn_id": "bq-oidc", "type": "bigquery", "schema": "acme.analytics", "extra": string(extra)})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, adminRequest(http.MethodPost, "/api/connections", string(body)))
	if rec.Code >= 300 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	stored, err := s.GetConnection("bq-oidc")
	if err != nil {
		t.Fatal(err)
	}

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, adminRequest(http.MethodPost, "/api/connections/bq-oidc/test", ""))
	var out map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	msg, _ := out["error"].(string)
	if strings.Contains(msg, "no OIDC token source") {
		t.Fatalf("the connection test was given no token source: %s", msg)
	}
	if len(tokens.reqs) == 0 {
		t.Fatalf("the connection test never asked the deployment's token source for a token (result: %v)", out)
	}
	got := tokens.reqs[0]
	if got.SubjectKind != "connection" || got.SubjectID != stored.ID || got.WorkspaceID != stored.WorkspaceID {
		t.Fatalf("token request = %+v, want connection %q in workspace %q", got, stored.ID, stored.WorkspaceID)
	}
	if !strings.Contains(msg, errTokenSourceReached.Error()) {
		t.Errorf("result error %q does not carry the token source's refusal", msg)
	}
}
