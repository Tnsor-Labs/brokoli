package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/secrets"
)

// #752: the connection test decrypted Password and Extra directly, so a
// connection whose credentials are references was tested with none. It
// now resolves them the way a run does.

func testConnection(t *testing.T, body map[string]interface{}) map[string]interface{} {
	t.Helper()
	h, _ := connRoundTripEnv(t)
	r := routeConn(h)
	r.Post("/api/connections/{connId}/test", h.Test)
	id, _ := body["conn_id"].(string)
	if w := doJSON(t, r, "POST", "/api/connections", body); w.Code >= 300 {
		t.Fatalf("create %s: %d %s", id, w.Code, w.Body.String())
	}
	w := doJSON(t, r, "POST", "/api/connections/"+id+"/test", nil)
	var out map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("test %s: %v (%s)", id, err, w.Body.String())
	}
	return out
}

func TestConnectionTestFailsOnAnUnresolvableReference(t *testing.T) {
	t.Setenv(secrets.EnvRefAllowEnv, "BROKOLI_TEST_752_MISSING")
	out := testConnection(t, map[string]interface{}{
		"conn_id": "api", "type": "http", "host": "10.20.0.1", "port": 80, "login": "u",
		"password_ref": "env://BROKOLI_TEST_752_MISSING",
	})
	if ok, _ := out["success"].(bool); ok {
		t.Fatalf("a connection whose password cannot be resolved passed the test: %v", out)
	}
	msg, _ := out["error"].(string)
	for _, want := range []string{`connection "api"`, "password", "could not resolve env://BROKOLI_TEST_752_MISSING", "not set"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not name %q", msg, want)
		}
	}
}

// The test runs on the server; a run resolves references on whichever
// machine runs the node. When the credentials come from the server's
// environment, Vault or Kubernetes, the result says where they were
// resolved. A credential stored in Brokoli resolves the same everywhere,
// so it gets no note.
func TestConnectionTestSaysWhereReferencesWereResolved(t *testing.T) {
	t.Setenv(secrets.EnvRefAllowEnv, "BROKOLI_TEST_752_PASSWORD")
	t.Setenv("BROKOLI_TEST_752_PASSWORD", "from-the-environment")
	// A private address the default outbound policy refuses: the test
	// fails without any network, and the note must be there regardless.
	withRef := testConnection(t, map[string]interface{}{
		"conn_id": "by-ref", "type": "http", "host": "10.20.0.1", "port": 80, "login": "u",
		"password_ref": "env://BROKOLI_TEST_752_PASSWORD",
	})
	if note, _ := withRef["note"].(string); !strings.Contains(note, "resolved on this server") {
		t.Errorf("no note that the reference was resolved on the server: %v", withRef)
	}
	if msg, _ := withRef["error"].(string); strings.Contains(msg, "could not resolve") {
		t.Errorf("a resolvable reference failed to resolve: %s", msg)
	}

	stored := testConnection(t, map[string]interface{}{
		"conn_id": "stored", "type": "http", "host": "10.20.0.1", "port": 80, "login": "u",
		"password": "kept-in-brokoli",
	})
	if _, ok := stored["note"]; ok {
		t.Errorf("a credential stored in Brokoli got the note: %v", stored)
	}
}
