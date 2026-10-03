package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/pkg/secrets"
	"github.com/Tnsor-Labs/brokoli/pkg/secretstore"
	"github.com/Tnsor-Labs/brokoli/pkg/secretstore/secretstoretest"
	"github.com/Tnsor-Labs/brokoli/store"
)

// secretStoreEnv is a connection handler and a store handler over one
// SQLite store, with a fake map-shaped provider "fakekv" and a fake
// string-shaped one "fakessm".
func secretStoreEnv(t *testing.T) (*chi.Mux, store.Store, *secretstoretest.Provider) {
	t.Helper()
	ch, s := connRoundTripEnv(t)
	kv := secretstoretest.New("fakekv", secretstore.ShapeMap, map[string]interface{}{
		"warehouse": map[string]string{"password": "pw-from-store-0123", "user": "loader"},
	})
	ssm := secretstoretest.New("fakessm", secretstore.ShapeString, map[string]interface{}{"/p": "v"})
	enc := secrets.NewEncryptedResolver(ch.crypto)
	res := engine.NewSecretStoreResolver(s.(store.SecretStoreStore), secretstore.NewRegistry(kv, ssm), secrets.NewChain(enc, enc))
	ch.useSecretStores(res)
	sh := &SecretStoreHandler{store: s, stores: s.(store.SecretStoreStore), crypto: ch.crypto, resolver: res}
	r := routeConn(ch)
	r.Post("/api/connections/{connId}/test", ch.Test)
	r.Get("/api/secret-stores/providers", sh.Providers)
	r.Get("/api/secret-stores", sh.List)
	r.Post("/api/secret-stores", sh.Create)
	r.Get("/api/secret-stores/{storeId}", sh.Get)
	r.Put("/api/secret-stores/{storeId}", sh.Update)
	r.Delete("/api/secret-stores/{storeId}", sh.Delete)
	r.Post("/api/secret-stores/{storeId}/test", sh.Test)
	return r, s, kv
}

// errOf is a response's decoded "error", so expectations read as written
// rather than JSON-escaped.
func errOf(b []byte) string {
	var m struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(b, &m)
	return m.Error
}

func decodeMap(t *testing.T, b []byte) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return m
}

func TestSecretStoreCRUDNeverReturnsTheCredential(t *testing.T) {
	r, s, _ := secretStoreEnv(t)
	w := doJSON(t, r, "POST", "/api/secret-stores", map[string]interface{}{
		"name": "vault-prod", "provider": "fakekv", "settings": map[string]string{"address": "https://vault"},
		"auth_method": "token", "credential": "good-token",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "good-token") {
		t.Fatalf("create response carries the credential: %s", w.Body.String())
	}
	created := decodeMap(t, w.Body.Bytes())
	id := created["id"].(string)
	if created["has_credential"] != true {
		t.Fatalf("has_credential = %v", created["has_credential"])
	}
	stored, _ := s.(store.SecretStoreStore).GetSecretStore("default", id)
	if !strings.HasPrefix(stored.CredentialRef, "encrypted://") || strings.Contains(stored.CredentialRef, "good-token") {
		t.Fatalf("credential stored as %q, want an encrypted reference", stored.CredentialRef)
	}
	for _, path := range []string{"/api/secret-stores", "/api/secret-stores/" + id} {
		if g := doJSON(t, r, "GET", path, nil); g.Code != http.StatusOK || strings.Contains(g.Body.String(), "good-token") ||
			strings.Contains(g.Body.String(), stored.CredentialRef) {
			t.Fatalf("GET %s: %d %s", path, g.Code, g.Body.String())
		}
	}
	// An update without a credential keeps it; one that moves the store to
	// another address drops it, so a token never follows to a new target.
	u := doJSON(t, r, "PUT", "/api/secret-stores/"+id, map[string]interface{}{
		"name": "vault-prod", "provider": "fakekv", "settings": map[string]string{"address": "https://vault"}, "auth_method": "token",
	})
	if u.Code != http.StatusOK || decodeMap(t, u.Body.Bytes())["has_credential"] != true {
		t.Fatalf("update keeping the token: %d %s", u.Code, u.Body.String())
	}
	moved := doJSON(t, r, "PUT", "/api/secret-stores/"+id, map[string]interface{}{
		"name": "vault-prod", "provider": "fakekv", "settings": map[string]string{"address": "https://elsewhere"}, "auth_method": "token",
	})
	if moved.Code != http.StatusBadRequest || !strings.Contains(moved.Body.String(), "needs a credential") {
		t.Fatalf("moving the store kept its token: %d %s", moved.Code, moved.Body.String())
	}
}

func TestSecretStoreValidation(t *testing.T) {
	r, _, _ := secretStoreEnv(t)
	for name, tc := range map[string]struct {
		body map[string]interface{}
		want string
	}{
		"bad name":            {map[string]interface{}{"name": "Vault_Prod", "provider": "fakekv", "auth_method": "ambient", "settings": map[string]string{"address": "a"}}, "is invalid"},
		"unknown provider":    {map[string]interface{}{"name": "v", "provider": "nope", "auth_method": "ambient"}, `provider "nope" is not available`},
		"unknown auth":        {map[string]interface{}{"name": "v", "provider": "fakekv", "auth_method": "password", "settings": map[string]string{"address": "a"}}, `auth_method "password" is not supported`},
		"provider settings":   {map[string]interface{}{"name": "v", "provider": "fakekv", "auth_method": "ambient"}, "settings.address is required"},
		"token without value": {map[string]interface{}{"name": "v", "provider": "fakekv", "auth_method": "token", "settings": map[string]string{"address": "a"}}, "needs a credential"},
		"credential not used": {map[string]interface{}{"name": "v", "provider": "fakekv", "auth_method": "ambient", "settings": map[string]string{"address": "a"}, "credential": "x"}, `only for auth_method "token"`},
	} {
		t.Run(name, func(t *testing.T) {
			w := doJSON(t, r, "POST", "/api/secret-stores", tc.body)
			if w.Code != http.StatusBadRequest || !strings.Contains(errOf(w.Body.Bytes()), tc.want) {
				t.Fatalf("%d %s, want 400 with %q", w.Code, w.Body.String(), tc.want)
			}
		})
	}
	ok := map[string]interface{}{"name": "dup", "provider": "fakekv", "auth_method": "ambient", "settings": map[string]string{"address": "a"}}
	if w := doJSON(t, r, "POST", "/api/secret-stores", ok); w.Code != http.StatusCreated {
		t.Fatalf("control: %d %s", w.Code, w.Body.String())
	}
	if w := doJSON(t, r, "POST", "/api/secret-stores", ok); w.Code != http.StatusConflict {
		t.Fatalf("a duplicate name in the workspace: %d %s", w.Code, w.Body.String())
	}
}

// Connections refer to stores by name: saving checks the store exists and
// the #field suits the provider; a referenced store cannot be deleted or
// renamed; the connection test resolves through it.
func TestConnectionsUseSecretStores(t *testing.T) {
	r, _, _ := secretStoreEnv(t)
	w := doJSON(t, r, "POST", "/api/secret-stores", map[string]interface{}{
		"name": "vault-prod", "provider": "fakekv", "settings": map[string]string{"address": "https://vault"},
		"auth_method": "token", "credential": "good-token",
	})
	id := decodeMap(t, w.Body.Bytes())["id"].(string)

	for name, tc := range map[string]struct {
		body map[string]interface{}
		want string
	}{
		"unknown store":   {map[string]interface{}{"password_ref": "secret://nope/warehouse#password"}, `password_ref: secret://nope/warehouse#password: no secret store named "nope"`},
		"map needs field": {map[string]interface{}{"password_ref": "secret://vault-prod/warehouse"}, "name one with #<field>"},
		"field in extra":  {map[string]interface{}{"extra": `{"secret_key":"secret://nope/x#y"}`}, "extra.secret_key:"},
	} {
		t.Run(name, func(t *testing.T) {
			body := map[string]interface{}{"conn_id": "pg-" + strings.ReplaceAll(name, " ", "-"), "type": "postgres", "host": "db.invalid", "port": 5432, "login": "u"}
			for k, v := range tc.body {
				body[k] = v
			}
			got := doJSON(t, r, "POST", "/api/connections", body)
			if got.Code != http.StatusBadRequest || !strings.Contains(errOf(got.Body.Bytes()), tc.want) {
				t.Fatalf("%d %s, want 400 with %q", got.Code, got.Body.String(), tc.want)
			}
		})
	}
	if c := doJSON(t, r, "POST", "/api/connections", map[string]interface{}{
		"conn_id": "pg-store", "type": "postgres", "host": "db.invalid", "port": 5432, "login": "loader",
		"password_ref": "secret://vault-prod/warehouse#password",
	}); c.Code != http.StatusCreated {
		t.Fatalf("a valid reference was refused: %d %s", c.Code, c.Body.String())
	}

	// Referenced: no delete, no rename.
	if d := doJSON(t, r, "DELETE", "/api/secret-stores/"+id, nil); d.Code != http.StatusConflict || !strings.Contains(d.Body.String(), "pg-store") {
		t.Fatalf("delete of a referenced store: %d %s", d.Code, d.Body.String())
	}
	if u := doJSON(t, r, "PUT", "/api/secret-stores/"+id, map[string]interface{}{
		"name": "vault-new", "provider": "fakekv", "settings": map[string]string{"address": "https://vault"}, "auth_method": "token",
	}); u.Code != http.StatusConflict {
		t.Fatalf("rename of a referenced store: %d %s", u.Code, u.Body.String())
	}

	// The connection test resolves the reference through the store; it
	// fails at the database, not at the reference.
	test := doJSON(t, r, "POST", "/api/connections/pg-store/test", nil)
	if strings.Contains(test.Body.String(), "could not resolve") || strings.Contains(test.Body.String(), "pw-from-store") {
		t.Fatalf("connection test: %s", test.Body.String())
	}
}

// The store test reports the version and field names, never the value.
func TestSecretStoreTestNeverReturnsTheValue(t *testing.T) {
	r, _, _ := secretStoreEnv(t)
	w := doJSON(t, r, "POST", "/api/secret-stores", map[string]interface{}{
		"name": "vault-prod", "provider": "fakekv", "settings": map[string]string{"address": "https://vault"},
		"auth_method": "token", "credential": "good-token",
	})
	id := decodeMap(t, w.Body.Bytes())["id"].(string)
	got := doJSON(t, r, "POST", "/api/secret-stores/"+id+"/test", map[string]string{"path": "warehouse"})
	out := decodeMap(t, got.Body.Bytes())
	if out["success"] != true || out["shape"] != "map" || strings.Contains(got.Body.String(), "pw-from-store") {
		t.Fatalf("test: %s", got.Body.String())
	}
	if fields, _ := out["fields"].([]interface{}); len(fields) != 2 {
		t.Fatalf("fields = %v", out["fields"])
	}
	missing := decodeMap(t, doJSON(t, r, "POST", "/api/secret-stores/"+id+"/test", map[string]string{"path": "nope"}).Body.Bytes())
	if missing["success"] != false || !strings.Contains(missing["error"].(string), "secret not found") {
		t.Fatalf("a missing path: %v", missing)
	}
	bad := doJSON(t, r, "POST", "/api/secret-stores", map[string]interface{}{
		"name": "vault-bad", "provider": "fakekv", "settings": map[string]string{"address": "https://vault"},
		"auth_method": "token", "credential": "wrong-token",
	})
	badID := decodeMap(t, bad.Body.Bytes())["id"].(string)
	refused := decodeMap(t, doJSON(t, r, "POST", "/api/secret-stores/"+badID+"/test", map[string]string{"path": "warehouse"}).Body.Bytes())
	if refused["success"] != false || !strings.Contains(refused["error"].(string), "permission denied") {
		t.Fatalf("a refused token: %v", refused)
	}
}
