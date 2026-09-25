package secrets

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// A vault:// or k8s:// reference used to read anything the server's own
// Vault token or service account could, and a connection is something a
// workspace editor can create and point anywhere. Each now resolves only
// what the operator has listed, and refuses before contacting the store.

func testVault(t *testing.T) (*VaultResolver, *int32) {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{"data":{"data":{"pw":"s3cret"}}}`))
	}))
	t.Cleanup(srv.Close)
	return &VaultResolver{addr: srv.URL, token: "t", client: srv.Client()}, &hits
}

func TestVaultRefResolvesOnlyListedPaths(t *testing.T) {
	v, hits := testVault(t)
	t.Setenv(VaultRefAllowEnv, "secret/data/brokoli, kv/data/shared/")

	for _, ref := range []string{"vault://secret/data/brokoli/warehouse#pw", "vault://kv/data/shared#pw", "vault://secret/data/brokoli#pw"} {
		if got, err := v.Resolve(context.Background(), ref); err != nil || got != "s3cret" {
			t.Errorf("%s: %q, %v", ref, got, err)
		}
	}
	before := atomic.LoadInt32(hits)
	for _, ref := range []string{
		"vault://secret/data/other#pw",
		"vault://secret/data/brokoli-old/warehouse#pw", // a prefix is matched on whole segments
		"vault://secret/data/brokolix#pw",
		"vault://secret/data#pw",
		"vault://secret/data/brokoli/%2e%2e/%2e%2e/other#pw", // decoded by Vault after the check
		"vault://secret/data/brokoli/..%2fother#pw",
		"vault://secret/data/brokoli\\..\\other#pw",
	} {
		_, err := v.Resolve(context.Background(), ref)
		if err == nil {
			t.Errorf("%s resolved", ref)
		}
	}
	if n := atomic.LoadInt32(hits) - before; n != 0 {
		t.Fatalf("refused references reached Vault %d times", n)
	}
}

func TestVaultRefIsDeniedByDefault(t *testing.T) {
	v, hits := testVault(t)
	t.Setenv(VaultRefAllowEnv, "")
	_, err := v.Resolve(context.Background(), "vault://secret/data/app#pw")
	if err == nil || !strings.Contains(err.Error(), VaultRefAllowEnv) {
		t.Fatalf("err = %v, want a refusal naming %s", err, VaultRefAllowEnv)
	}
	if *hits != 0 {
		t.Fatal("an unlisted reference reached Vault")
	}
}

// Refused before kubectl runs: with kubectl missing from PATH, the error is
// the allowlist's, not kubectl's.
func TestK8sRefIsDeniedByDefault(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv(K8sRefAllowEnv, "")
	k := &K8sResolver{defaultNS: "brokoli", AllowedNamespaces: map[string]bool{"brokoli": true}}
	for _, ref := range []string{"k8s://brokoli-secrets/encryption-key", "k8s://brokoli/brokoli-secrets/db-url"} {
		_, err := k.Resolve(context.Background(), ref)
		if err == nil || !strings.Contains(err.Error(), K8sRefAllowEnv) {
			t.Errorf("%s: err = %v, want a refusal naming %s", ref, err, K8sRefAllowEnv)
		}
	}
	// A listed secret gets past the allowlist and on to kubectl.
	t.Setenv(K8sRefAllowEnv, "brokoli/warehouse-creds")
	_, err := k.Resolve(context.Background(), "k8s://warehouse-creds/password")
	if err == nil || strings.Contains(err.Error(), K8sRefAllowEnv) {
		t.Fatalf("a listed secret was refused by the allowlist: %v", err)
	}
}

func TestK8sRefAllowedMatchesExactNames(t *testing.T) {
	t.Setenv(K8sRefAllowEnv, "brokoli/warehouse-creds, other/api-key")
	for ns, name := range map[string]string{"brokoli": "warehouse-creds", "other": "api-key"} {
		if !K8sRefAllowed(ns, name) {
			t.Errorf("%s/%s refused", ns, name)
		}
	}
	for _, c := range [][2]string{{"brokoli", "warehouse-creds-2"}, {"other", "warehouse-creds"}, {"brokoli", "api-key"}} {
		if K8sRefAllowed(c[0], c[1]) {
			t.Errorf("%s/%s allowed", c[0], c[1])
		}
	}
}
