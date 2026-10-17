package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// A create that names its workspace in the body writes only into the
// workspace the request was authorized for. The permission check runs
// against the request's workspace; a body workspace_id honoured as given
// let a caller write a variable or a connection into anyone's workspace
// whose id it knew.

func TestVariableSetRefusesAnotherWorkspace(t *testing.T) {
	multiTenant(t, map[string][]string{"alice": {wsAlpha}, "bob": {wsBeta}})
	h, _, s := secretHandler(t)

	set := func(body map[string]string) int {
		raw, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		h.Set(rec, request("POST", "/api/variables", "", "alice", wsAlpha, raw))
		return rec.Code
	}

	if code := set(map[string]string{"key": "api_host", "value": "evil.example", "workspace_id": wsBeta}); code != http.StatusForbidden {
		t.Errorf("writing into another workspace: status %d, want 403", code)
	}
	if v, err := s.GetVariable(wsBeta, "api_host"); err == nil && v != nil {
		t.Errorf("refused write still stored a variable in %s: %+v", wsBeta, v)
	}

	// Controls: the caller's own workspace, named or not, still works.
	for _, ws := range []string{wsAlpha, "", "default"} {
		if code := set(map[string]string{"key": "k_" + ws, "value": "v", "workspace_id": ws}); code != http.StatusOK && code != http.StatusCreated {
			t.Errorf("workspace_id %q: status %d, want success", ws, code)
		}
		if v, err := s.GetVariable(wsAlpha, "k_"+ws); err != nil || v == nil {
			t.Errorf("workspace_id %q: variable not stored in %s (%v)", ws, wsAlpha, err)
		}
	}
}

func TestVariablePutKeyMustMatchPath(t *testing.T) {
	multiTenant(t, map[string][]string{"alice": {wsAlpha}})
	h, _, s := secretHandler(t)
	put := func(pathKey string, body map[string]string) int {
		raw, _ := json.Marshal(body)
		r := request("PUT", "/api/variables/"+pathKey, "", "alice", wsAlpha, raw)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("key", pathKey)
		rec := httptest.NewRecorder()
		h.Set(rec, r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx)))
		return rec.Code
	}
	if code := put("allowed", map[string]string{"key": "other", "value": "x"}); code != http.StatusBadRequest {
		t.Errorf("body key differing from the path: status %d, want 400", code)
	}
	if v, err := s.GetVariable(wsAlpha, "other"); err == nil && v != nil {
		t.Errorf("mismatched PUT still wrote %q", "other")
	}
	if code := put("allowed", map[string]string{"value": "x"}); code != http.StatusOK && code != http.StatusCreated {
		t.Errorf("PUT with the key only in the path: status %d, want success", code)
	}
	if v, err := s.GetVariable(wsAlpha, "allowed"); err != nil || v == nil {
		t.Errorf("PUT with the key only in the path stored nothing (%v)", err)
	}
}

func TestConnectionCreateRefusesAnotherWorkspace(t *testing.T) {
	multiTenant(t, map[string][]string{"alice": {wsAlpha}, "bob": {wsBeta}})
	h, s := connRoundTripEnv(t)

	create := func(connID, ws string) int {
		raw, _ := json.Marshal(map[string]interface{}{"conn_id": connID, "type": "postgres", "host": "db.internal", "port": 5432, "workspace_id": ws})
		rec := httptest.NewRecorder()
		h.Create(rec, request("POST", "/api/connections", "", "alice", wsAlpha, raw))
		return rec.Code
	}

	if code := create("planted", wsBeta); code != http.StatusForbidden {
		t.Errorf("creating in another workspace: status %d, want 403", code)
	}
	if c, err := s.GetConnection("planted"); err == nil && c != nil {
		t.Errorf("refused create still stored a connection: workspace %s", c.WorkspaceID)
	}
	for _, ws := range []string{wsAlpha, ""} {
		id := "own-" + ws
		if code := create(id, ws); code != http.StatusCreated {
			t.Errorf("workspace_id %q: status %d, want 201", ws, code)
		}
		if c, err := s.GetConnection(id); err != nil || c == nil || c.WorkspaceID != wsAlpha {
			t.Errorf("workspace_id %q: stored %+v (%v), want workspace %s", ws, c, err, wsAlpha)
		}
	}
}
