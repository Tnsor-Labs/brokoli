package secretstore

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// These tests run against a real OpenBao dev server (docker-compose.test.yml,
// service "openbao"), which speaks Vault's HTTP API:
//
//	docker compose -f docker-compose.test.yml up -d openbao
//	BROKOLI_TEST_VAULT_ADDR=http://127.0.0.1:55547 go test ./pkg/secretstore -run Vault
//
// They skip when BROKOLI_TEST_VAULT_ADDR is unset. Every test works under
// its own random prefix and auth mount, so they can run again against the
// same server.

const vaultTestRootDefault = "brokoli-test-root"

type openbao struct {
	t    *testing.T
	addr string
	root string
}

func needOpenBao(t *testing.T) *openbao {
	t.Helper()
	addr := os.Getenv("BROKOLI_TEST_VAULT_ADDR")
	if addr == "" {
		t.Skip("BROKOLI_TEST_VAULT_ADDR is not set; start the openbao service in docker-compose.test.yml")
	}
	root := os.Getenv("BROKOLI_TEST_VAULT_TOKEN")
	if root == "" {
		root = vaultTestRootDefault
	}
	withLoopback(t)
	return &openbao{t: t, addr: strings.TrimRight(addr, "/"), root: root}
}

// call makes an admin request with the root token and returns the decoded
// body. It fails the test on any status but want.
func (o *openbao) call(method, path string, body interface{}, want int) map[string]interface{} {
	o.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, o.addr+"/v1/"+path, rd)
	if err != nil {
		o.t.Fatal(err)
	}
	req.Header.Set("X-Vault-Token", o.root)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		o.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		o.t.Fatalf("%s %s: status %d, want %d: %s", method, path, resp.StatusCode, want, b)
	}
	out := map[string]interface{}{}
	_ = json.Unmarshal(b, &out)
	return out
}

// readToken returns a fresh token that can read every secret. Stores under
// test present it instead of the root token, so a fault that revokes the
// token a store presents cannot take the root token, and every later test,
// with it.
func (o *openbao) readToken() string {
	o.t.Helper()
	name := randomName(o.t, "brokoli-all-")
	o.call(http.MethodPut, "sys/policies/acl/"+name, map[string]string{
		"policy": `path "+/data/*" { capabilities = ["read"] }`,
	}, http.StatusNoContent)
	out := o.call(http.MethodPost, "auth/token/create", map[string]interface{}{
		"policies": []string{name}, "no_default_policy": true, "ttl": "10m",
	}, http.StatusOK)
	return out["auth"].(map[string]interface{})["client_token"].(string)
}

// childRoot returns a separate root token, for a store that must reach
// into a namespace; revoking it leaves the configured root token alone.
func (o *openbao) childRoot() string {
	o.t.Helper()
	out := o.call(http.MethodPost, "auth/token/create", map[string]interface{}{"policies": []string{"root"}, "ttl": "10m"}, http.StatusOK)
	return out["auth"].(map[string]interface{})["client_token"].(string)
}

func randomName(t *testing.T, prefix string) string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return prefix + hex.EncodeToString(b)
}

// writeKV writes one version of secret/<path>.
func (o *openbao) writeKV(path string, data map[string]interface{}) {
	o.t.Helper()
	o.call(http.MethodPost, "secret/data/"+path, map[string]interface{}{"data": data}, http.StatusOK)
}

// policyToken creates a policy granting read on exactly secret/data/<path>
// and returns its name and a token holding only it.
func (o *openbao) policyToken(path string) (string, string) {
	o.t.Helper()
	name := randomName(o.t, "brokoli-read-")
	o.call(http.MethodPut, "sys/policies/acl/"+name, map[string]string{
		"policy": `path "secret/data/` + path + `" { capabilities = ["read"] }`,
	}, http.StatusNoContent)
	out := o.call(http.MethodPost, "auth/token/create", map[string]interface{}{
		"policies": []string{name}, "no_default_policy": true, "ttl": "10m",
	}, http.StatusOK)
	return name, out["auth"].(map[string]interface{})["client_token"].(string)
}

func TestVaultOpenBaoReadsKVv2(t *testing.T) {
	o := needOpenBao(t)
	ctx := context.Background()
	prefix := randomName(t, "brokoli-test-")
	o.writeKV(prefix+"/warehouse", map[string]interface{}{"password": "first-password-0123"})
	o.writeKV(prefix+"/warehouse", map[string]interface{}{"password": "second-password-0123", "user": "loader", "port": 5432})

	st, err := Vault().Open(ctx, Settings{"address": o.addr}, Identity{Method: AuthToken, Token: o.readToken()})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	sec, err := st.Get(ctx, prefix+"/warehouse", "")
	if err != nil {
		t.Fatal(err)
	}
	if string(sec.Fields["password"]) != "second-password-0123" || string(sec.Fields["user"]) != "loader" ||
		string(sec.Fields["port"]) != "5432" || sec.Version != "2" {
		t.Fatalf("current version: %+v", sec)
	}
	v, err := Pick(Ref{Store: "s", Path: prefix + "/warehouse", Field: "password"}, sec)
	if err != nil || string(v) != "second-password-0123" {
		t.Fatalf("pick: %q %v", v, err)
	}

	sec, err = st.Get(ctx, prefix+"/warehouse", "1")
	if err != nil {
		t.Fatal(err)
	}
	if string(sec.Fields["password"]) != "first-password-0123" || sec.Version != "1" || sec.Fields["user"] != nil {
		t.Fatalf("version 1: %+v", sec)
	}

	_, err = st.Get(ctx, prefix+"/warehouse", "9")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("a version that does not exist: %v", err)
	}
	_, err = st.Get(ctx, prefix+"/missing", "")
	if !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "secret/data/"+prefix+"/missing") {
		t.Fatalf("not found: %v", err)
	}

	// A soft-deleted current version is not found, not an empty secret.
	o.call(http.MethodDelete, "secret/data/"+prefix+"/warehouse", nil, http.StatusNoContent)
	if _, err := st.Get(ctx, prefix+"/warehouse", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted version: %v", err)
	}
	if sec, err := st.Get(ctx, prefix+"/warehouse", "1"); err != nil || string(sec.Fields["password"]) != "first-password-0123" {
		t.Fatalf("an older version survives a delete of the current one: %+v %v", sec, err)
	}
}

func TestVaultOpenBaoPermissionDenied(t *testing.T) {
	o := needOpenBao(t)
	ctx := context.Background()
	prefix := randomName(t, "brokoli-test-")
	o.writeKV(prefix+"/allowed", map[string]interface{}{"password": "allowed-password-0123"})
	o.writeKV(prefix+"/other", map[string]interface{}{"password": "other-password-0123"})
	_, token := o.policyToken(prefix + "/allowed")

	st, err := Vault().Open(ctx, Settings{"address": o.addr}, Identity{Method: AuthToken, Token: token})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if sec, err := st.Get(ctx, prefix+"/allowed", ""); err != nil || string(sec.Fields["password"]) != "allowed-password-0123" {
		t.Fatalf("allowed: %+v %v", sec, err)
	}
	_, err = st.Get(ctx, prefix+"/other", "")
	if !errors.Is(err, ErrPermission) || !strings.Contains(err.Error(), "secret/data/"+prefix+"/other") {
		t.Fatalf("other: %v", err)
	}
	if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "other-password-0123") {
		t.Fatalf("the error carries the token or the value: %v", err)
	}

	// An unknown token is a permission error too, and is not echoed.
	st2, err := Vault().Open(ctx, Settings{"address": o.addr}, Identity{Method: AuthToken, Token: "s.not-a-real-token-0123"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st2.Get(ctx, prefix+"/allowed", "")
	if !errors.Is(err, ErrPermission) || strings.Contains(err.Error(), "not-a-real-token") {
		t.Fatalf("unknown token: %v", err)
	}
	// A static token is the customer's: Close does not revoke it.
	_ = st.Close()
	o.call(http.MethodPost, "auth/token/lookup", map[string]string{"token": token}, http.StatusOK)
}

// jwtKey generates an RSA key and returns it with its public half in PEM,
// the form jwt_validation_pubkeys takes.
func jwtKey(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return key, string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

func signJWT(t *testing.T, key *rsa.PrivateKey, claims jwt.MapClaims) string {
	t.Helper()
	s, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestVaultOpenBaoJWTLogin(t *testing.T) {
	o := needOpenBao(t)
	ctx := context.Background()
	prefix := randomName(t, "brokoli-test-")
	o.writeKV(prefix+"/loader", map[string]interface{}{"password": "jwt-read-password-0123"})
	policy, _ := o.policyToken(prefix + "/loader")

	// A JWT auth mount of its own, trusting a key made here, with a role
	// bound to the audience and to one workspace claim.
	mount := randomName(t, "jwt-")
	o.call(http.MethodPost, "sys/auth/"+mount, map[string]string{"type": "jwt"}, http.StatusNoContent)
	t.Cleanup(func() { o.call(http.MethodDelete, "sys/auth/"+mount, nil, http.StatusNoContent) })
	key, pub := jwtKey(t)
	o.call(http.MethodPost, "auth/"+mount+"/config", map[string]interface{}{
		"jwt_validation_pubkeys": []string{pub},
	}, http.StatusNoContent)
	o.call(http.MethodPost, "auth/"+mount+"/role/brokoli-loader", map[string]interface{}{
		"role_type":       "jwt",
		"bound_audiences": []string{"vault"},
		"user_claim":      "sub",
		"bound_claims":    map[string]string{"workspace_id": "ws-a"},
		"token_policies":  []string{policy},
		"token_ttl":       "5m",
	}, http.StatusNoContent)

	now := time.Now()
	claims := func(aud, ws string) jwt.MapClaims {
		return jwt.MapClaims{"iss": "https://brokoli.test", "sub": "store:st-1", "aud": aud, "workspace_id": ws,
			"iat": now.Unix(), "nbf": now.Add(-time.Minute).Unix(), "exp": now.Add(5 * time.Minute).Unix()}
	}
	s := Settings{"address": o.addr}
	a := Settings{"role": "brokoli-loader", "auth_mount": mount}
	if err := Vault().ValidateSettings(s, AuthOIDC, a); err != nil {
		t.Fatal(err)
	}
	aud := Vault().Audience(s, a)

	st, err := Vault().Open(ctx, s, Identity{Method: AuthOIDC, Settings: a, Token: signJWT(t, key, claims(aud, "ws-a"))})
	if err != nil {
		t.Fatal(err)
	}
	sec, err := st.Get(ctx, prefix+"/loader", "")
	if err != nil || string(sec.Fields["password"]) != "jwt-read-password-0123" {
		t.Fatalf("read after jwt login: %+v %v", sec, err)
	}
	// The session token from the login is revoked on Close.
	session := st.(*vaultClient).token
	_ = st.Close()
	o.call(http.MethodPost, "auth/token/lookup", map[string]string{"token": session}, http.StatusForbidden)

	// A token for another workspace, or another audience, is refused with a
	// permission error that names the role and not the token.
	for name, tok := range map[string]string{
		"other workspace": signJWT(t, key, claims(aud, "ws-b")),
		"other audience":  signJWT(t, key, claims("not-vault", "ws-a")),
	} {
		_, err := Vault().Open(ctx, s, Identity{Method: AuthOIDC, Settings: a, Token: tok})
		if !errors.Is(err, ErrPermission) || !strings.Contains(err.Error(), `role "brokoli-loader"`) || strings.Contains(err.Error(), tok) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A JWT signed by a key the mount does not trust is refused.
	other, _ := jwtKey(t)
	if _, err := Vault().Open(ctx, s, Identity{Method: AuthOIDC, Settings: a, Token: signJWT(t, other, claims(aud, "ws-a"))}); !errors.Is(err, ErrPermission) {
		t.Errorf("untrusted key: %v", err)
	}
}

// A namespace setting sends X-Vault-Namespace; OpenBao has namespaces
// since 2.3, so the read lands in the namespace's own KV mount.
func TestVaultOpenBaoNamespace(t *testing.T) {
	o := needOpenBao(t)
	ctx := context.Background()
	ns := randomName(t, "team-")
	o.call(http.MethodPost, "sys/namespaces/"+ns, map[string]interface{}{}, http.StatusOK)
	t.Cleanup(func() {
		o.call(http.MethodDelete, "sys/namespaces/"+ns, nil, http.StatusOK)
	})
	nsCall := func(method, path string, body interface{}, want int) {
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, o.addr+"/v1/"+path, bytes.NewReader(b))
		req.Header.Set("X-Vault-Token", o.root)
		req.Header.Set("X-Vault-Namespace", ns)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != want {
			rb, _ := io.ReadAll(resp.Body)
			t.Fatalf("%s %s in %s: %d %s", method, path, ns, resp.StatusCode, rb)
		}
	}
	nsCall(http.MethodPost, "sys/mounts/kv", map[string]interface{}{"type": "kv", "options": map[string]string{"version": "2"}}, http.StatusNoContent)
	nsCall(http.MethodPost, "kv/data/app", map[string]interface{}{"data": map[string]string{"password": "namespaced-0123"}}, http.StatusOK)

	st, err := Vault().Open(ctx, Settings{"address": o.addr, "namespace": ns, "mount": "kv"}, Identity{Method: AuthToken, Token: o.childRoot()})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if sec, err := st.Get(ctx, "app", ""); err != nil || string(sec.Fields["password"]) != "namespaced-0123" {
		t.Fatalf("namespaced read: %+v %v", sec, err)
	}
	// Without the namespace the same mount and path do not exist.
	root, err := Vault().Open(ctx, Settings{"address": o.addr, "mount": "kv"}, Identity{Method: AuthToken, Token: o.childRoot()})
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, err := root.Get(ctx, "app", ""); err == nil {
		t.Fatal("read the namespace's secret without the namespace")
	}
}
