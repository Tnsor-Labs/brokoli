package secrets

import (
	"context"
	"os"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/crypto"
)

func TestEnvResolver(t *testing.T) {
	os.Setenv("TEST_SECRET_123", "hunter2")
	defer os.Unsetenv("TEST_SECRET_123")

	r := EnvResolver{}
	val, err := r.Resolve(context.Background(), "env://TEST_SECRET_123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != "hunter2" {
		t.Fatalf("expected 'hunter2', got %q", val)
	}
}

func TestEnvResolver_Missing(t *testing.T) {
	r := EnvResolver{}
	_, err := r.Resolve(context.Background(), "env://NONEXISTENT_VAR_XYZ_999")
	if err == nil {
		t.Fatal("expected error for missing var")
	}
}

func TestEncryptedResolver(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	cr := &crypto.Config{Key: key}

	ciphertext, err := cr.Encrypt("my-password")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	r := NewEncryptedResolver(cr)

	// With scheme prefix
	val, err := r.Resolve(context.Background(), "encrypted://"+ciphertext)
	if err != nil {
		t.Fatalf("resolve with scheme: %v", err)
	}
	if val != "my-password" {
		t.Fatalf("expected 'my-password', got %q", val)
	}

	// Without scheme prefix (legacy fallback)
	val, err = r.Resolve(context.Background(), ciphertext)
	if err != nil {
		t.Fatalf("resolve legacy: %v", err)
	}
	if val != "my-password" {
		t.Fatalf("expected 'my-password', got %q", val)
	}
}

func TestChain_DispatchesByScheme(t *testing.T) {
	os.Setenv("CHAIN_TEST_PW", "secret123")
	defer os.Unsetenv("CHAIN_TEST_PW")

	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 10)
	}
	cr := &crypto.Config{Key: key}
	enc := NewEncryptedResolver(cr)

	chain := NewChain(enc, EnvResolver{}, enc)

	// env:// ref
	val, err := chain.Resolve(context.Background(), "env://CHAIN_TEST_PW")
	if err != nil {
		t.Fatalf("env resolve: %v", err)
	}
	if val != "secret123" {
		t.Fatalf("expected 'secret123', got %q", val)
	}

	// encrypted:// ref
	ct, _ := cr.Encrypt("enc-value")
	val, err = chain.Resolve(context.Background(), "encrypted://"+ct)
	if err != nil {
		t.Fatalf("encrypted resolve: %v", err)
	}
	if val != "enc-value" {
		t.Fatalf("expected 'enc-value', got %q", val)
	}

	// Legacy bare ciphertext (no scheme) — should hit fallback
	ct2, _ := cr.Encrypt("legacy-pw")
	val, err = chain.Resolve(context.Background(), ct2)
	if err != nil {
		t.Fatalf("fallback resolve: %v", err)
	}
	if val != "legacy-pw" {
		t.Fatalf("expected 'legacy-pw', got %q", val)
	}
}

func TestChain_UnsupportedScheme(t *testing.T) {
	chain := NewChain(nil, EnvResolver{})
	_, err := chain.Resolve(context.Background(), "vault://secret/path#key")
	if err == nil {
		t.Fatal("expected error for unsupported scheme")
	}
}

func TestChain_EmptyRef(t *testing.T) {
	chain := NewChain(nil)
	val, err := chain.Resolve(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != "" {
		t.Fatalf("expected empty, got %q", val)
	}
}

func TestK8sResolver_NamespaceIsolation(t *testing.T) {
	r := NewK8sResolver()
	r.AllowedNamespaces = map[string]bool{"brokoli": true}

	_, err := r.Resolve(context.Background(), "k8s://kube-system/admin-secret/token")
	if err == nil {
		t.Fatal("expected error for disallowed namespace")
	}
	if !strings.Contains(err.Error(), "not in allowed list") {
		t.Fatalf("expected namespace error, got: %v", err)
	}
}

func TestK8sResolver_InvalidChars(t *testing.T) {
	r := NewK8sResolver()

	_, err := r.Resolve(context.Background(), "k8s://ns/secret/key; echo pwned")
	if err == nil {
		t.Fatal("expected error for invalid characters")
	}
}

func TestVaultResolver_PathTraversal(t *testing.T) {
	v := &VaultResolver{
		addr:   "http://localhost:8200",
		token:  "test",
		client: &http.Client{Timeout: time.Second},
	}
	_, err := v.Resolve(context.Background(), "vault://../../sys/policy#data")
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
	if !strings.Contains(err.Error(), "traversal") {
		t.Fatalf("expected traversal error, got: %v", err)
	}
}

func TestParseRef(t *testing.T) {
	tests := []struct {
		input  string
		scheme string
		body   string
		ok     bool
	}{
		{"env://MY_VAR", "env", "MY_VAR", true},
		{"vault://secret/data/prod#password", "vault", "secret/data/prod#password", true},
		{"k8s://brokoli/db-creds/password", "k8s", "brokoli/db-creds/password", true},
		{"encrypted://abc123==", "encrypted", "abc123==", true},
		{"bare-ciphertext-no-scheme", "", "bare-ciphertext-no-scheme", false},
		{"://missing-scheme", "", "://missing-scheme", false},
	}
	for _, tt := range tests {
		scheme, body, ok := parseRef(tt.input)
		if scheme != tt.scheme || body != tt.body || ok != tt.ok {
			t.Errorf("parseRef(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tt.input, scheme, body, ok, tt.scheme, tt.body, tt.ok)
		}
	}
}
