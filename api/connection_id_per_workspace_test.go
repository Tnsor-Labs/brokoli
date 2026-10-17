package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// Two workspaces can each have a connection called "warehouse", and each
// caller reaches only its own: reading, changing and deleting it.
func TestConnectionIDsAreScopedToTheirWorkspace(t *testing.T) {
	multiTenant(t, map[string][]string{"alice": {wsAlpha}, "bob": {wsBeta}})
	h, s := connRoundTripEnv(t)

	withConn := func(r *http.Request, connID string) *http.Request {
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("connId", connID)
		return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
	}
	for user, ws := range map[string]string{"alice": wsAlpha, "bob": wsBeta} {
		raw, _ := json.Marshal(map[string]interface{}{"conn_id": "warehouse", "type": "postgres", "host": user + ".internal", "port": 5432})
		rec := httptest.NewRecorder()
		h.Create(rec, request("POST", "/api/connections", "", user, ws, raw))
		if rec.Code != http.StatusCreated {
			t.Fatalf("%s creates warehouse: %d %s", user, rec.Code, rec.Body.String())
		}
	}

	get := func(user, ws string) string {
		rec := httptest.NewRecorder()
		h.Get(rec, withConn(request("GET", "/api/connections/warehouse", "", user, ws, nil), "warehouse"))
		var out struct {
			Host string `json:"host"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out.Host
	}
	if got := get("alice", wsAlpha); got != "alice.internal" {
		t.Errorf("alice reads %q, want her own", got)
	}
	if got := get("bob", wsBeta); got != "bob.internal" {
		t.Errorf("bob reads %q, want his own", got)
	}

	rec := httptest.NewRecorder()
	h.Delete(rec, withConn(request("DELETE", "/api/connections/warehouse", "", "bob", wsBeta, nil), "warehouse"))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("bob deletes his: %d %s", rec.Code, rec.Body.String())
	}
	if c, err := s.GetConnectionIn(wsAlpha, "warehouse"); err != nil || c.Host != "alice.internal" {
		t.Errorf("bob's delete reached alice's connection: %+v (%v)", c, err)
	}
}
