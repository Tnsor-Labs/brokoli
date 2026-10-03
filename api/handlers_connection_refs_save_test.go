package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// #781: a reference is checked when the connection is saved, without being
// resolved, and a bad one is refused naming the field and the reason.
func TestConnectionSaveRefusesBadReferences(t *testing.T) {
	h, s := connRoundTripEnv(t)
	r := routeConn(h)
	for name, tc := range map[string]struct {
		field, ref, want string
	}{
		"unknown scheme":           {"password_ref", "valt://secret/data/x#k", `password_ref: scheme "valt://" is not supported`},
		"no scheme":                {"password_ref", "WAREHOUSE_PASSWORD", "password_ref:"},
		"malformed vault":          {"extra_ref", "vault://secret/data/x", "extra_ref: vault://secret/data/x: expected vault://path#key"},
		"server's own secret":      {"password_ref", "env://BROKOLI_ENCRYPTION_KEY", "can never be read"},
		"client-made ciphertext":   {"password_ref", "encrypted://Zm9vYmFy", "created by the server"},
		"store reference, not yet": {"password_ref", "secret://vault-prod/x#pw", `scheme "secret://" is not supported`},
	} {
		t.Run(name, func(t *testing.T) {
			w := doJSON(t, r, "POST", "/api/connections", map[string]interface{}{
				"conn_id": "bad-ref", "type": "postgres", "host": "db.example.com", "port": 5432, "login": "u",
				tc.field: tc.ref,
			})
			var out struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(w.Body.Bytes(), &out)
			if w.Code != http.StatusBadRequest || !strings.Contains(out.Error, tc.want) {
				t.Fatalf("create: %d %s, want 400 containing %q", w.Code, w.Body.String(), tc.want)
			}
			if _, err := s.GetConnection("bad-ref"); err == nil {
				t.Fatal("a refused connection was stored")
			}
		})
	}

	// The control: a well-formed reference is saved, without the variable
	// existing or being allowed on this server -- a worker may resolve it.
	w := doJSON(t, r, "POST", "/api/connections", map[string]interface{}{
		"conn_id": "good-ref", "type": "postgres", "host": "db.example.com", "port": 5432, "login": "u",
		"password_ref": "env://WAREHOUSE_PASSWORD_NOT_SET", "extra_ref": "vault://secret/data/prod/wh#extra",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("a well-formed reference was refused: %d %s", w.Code, w.Body.String())
	}

	// An update with a bad new reference is refused, and the stored one kept.
	put := doJSON(t, r, "PUT", "/api/connections/good-ref", map[string]interface{}{
		"type": "postgres", "host": "db.example.com", "port": 5432, "login": "u",
		"password_ref": "k8s://only",
	})
	if put.Code != http.StatusBadRequest || !strings.Contains(put.Body.String(), "password_ref: k8s://only") {
		t.Fatalf("update with a bad reference: %d %s", put.Code, put.Body.String())
	}
	if got, _ := s.GetConnection("good-ref"); got.PasswordRef != "env://WAREHOUSE_PASSWORD_NOT_SET" {
		t.Fatalf("stored reference changed to %q", got.PasswordRef)
	}
}

// A reference already stored is not checked again: the form echoes it on
// every save, and renaming a connection must not fail because the rules
// changed after the reference was saved.
func TestConnectionUpdateKeepsAStoredReferenceUnchecked(t *testing.T) {
	h, s := connRoundTripEnv(t)
	r := routeConn(h)
	if w := doJSON(t, r, "POST", "/api/connections", map[string]interface{}{
		"conn_id": "legacy", "type": "postgres", "host": "db.example.com", "port": 5432, "login": "u",
		"password_ref": "env://WAREHOUSE_PASSWORD",
	}); w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	// A reference saved before the rules existed, which they now refuse.
	c, err := s.GetConnection("legacy")
	if err != nil {
		t.Fatal(err)
	}
	c.PasswordRef = "env://OLD-STYLE-NAME"
	if err := s.UpdateConnection(c); err != nil {
		t.Fatal(err)
	}
	put := doJSON(t, r, "PUT", "/api/connections/legacy", map[string]interface{}{
		"type": "postgres", "host": "db.example.com", "port": 5432, "login": "u", "description": "renamed",
		"password_ref": "env://OLD-STYLE-NAME",
	})
	if put.Code != http.StatusOK {
		t.Fatalf("echoing the stored reference was refused: %d %s", put.Code, put.Body.String())
	}
}

// #755: the API shows a reference that is a location, and hides one whose
// body is the credential.
func TestConnectionAPIShowsLocationsAndHidesCiphertext(t *testing.T) {
	h, _ := connRoundTripEnv(t)
	r := routeConn(h)
	for id, body := range map[string]map[string]interface{}{
		"by-env":   {"password_ref": "env://WAREHOUSE_PASSWORD", "extra_ref": "k8s://brokoli/wh/extra"},
		"by-vault": {"password_ref": "vault://secret/data/prod/wh#password"},
		"stored":   {"password": "kept-in-brokoli", "extra": `{"sslmode":"require"}`},
	} {
		body["conn_id"] = id
		body["type"], body["host"], body["port"], body["login"] = "postgres", "db.example.com", 5432, "u"
		if w := doJSON(t, r, "POST", "/api/connections", body); w.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", id, w.Code, w.Body.String())
		}
	}
	get := func(id string) map[string]interface{} {
		w := doJSON(t, r, "GET", "/api/connections/"+id, nil)
		var out map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if c := get("by-env"); c["password_ref"] != "env://WAREHOUSE_PASSWORD" || c["extra_ref"] != "k8s://brokoli/wh/extra" {
		t.Errorf("env/k8s references are hidden: %v %v", c["password_ref"], c["extra_ref"])
	}
	if c := get("by-vault"); c["password_ref"] != "vault://secret/data/prod/wh#password" {
		t.Errorf("vault reference hidden: %v", c["password_ref"])
	}
	c := get("stored")
	if c["password_ref"] != maskedRef || c["extra_ref"] != maskedRef {
		t.Errorf("encrypted references are shown: %v %v", c["password_ref"], c["extra_ref"])
	}
	if p, _ := c["password"].(string); p != "" {
		t.Errorf("password returned: %q", p)
	}
}
