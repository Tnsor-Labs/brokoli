package secretstore

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
)

func TestVaultValidateSettings(t *testing.T) {
	p := Vault()
	ok := Settings{"address": "https://vault.example.com:8200"}
	if err := p.ValidateSettings(ok, AuthToken, Settings{}); err != nil {
		t.Fatalf("valid token store: %v", err)
	}
	if err := p.ValidateSettings(Settings{"address": "http://127.0.0.1:8200"}, AuthToken, nil); err != nil {
		t.Fatalf("loopback dev server: %v", err)
	}
	for name, tc := range map[string]struct {
		s    Settings
		auth AuthMethod
		a    Settings
		want string
	}{
		"no address":        {Settings{}, AuthToken, nil, "settings.address is required"},
		"plain http remote": {Settings{"address": "http://vault.example.com"}, AuthToken, nil, "plain http is allowed only for a loopback"},
		"other scheme":      {Settings{"address": "ftp://vault.example.com"}, AuthToken, nil, "must be https"},
		"credentials":       {Settings{"address": "https://u:p@vault.example.com"}, AuthToken, nil, "may not carry credentials"},
		"query":             {Settings{"address": "https://vault.example.com?x=1"}, AuthToken, nil, "may not carry"},
		"bad mount":         {Settings{"address": "https://v", "mount": "../sys"}, AuthToken, nil, "settings.mount"},
		"oidc without role": {ok, AuthOIDC, Settings{}, "auth_settings.role is required"},
		"k8s without role":  {ok, AuthAmbient, Settings{}, "auth_settings.role is required"},
	} {
		if err := p.ValidateSettings(tc.s, tc.auth, tc.a); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
	if p.Audience(nil, Settings{}) != "vault" || p.Audience(nil, Settings{"audience": "brokoli"}) != "brokoli" {
		t.Error("audience defaults to vault and honours auth_settings.audience")
	}
}

// fakeVault answers a Kubernetes login, a KV v2 read and a revoke, and
// records what it was sent.
type fakeVault struct {
	mu        sync.Mutex
	loginBody map[string]string
	loginPath string
	namespace string
	readToken string
	revoked   string
}

func (f *fakeVault) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/login"):
			f.loginPath = r.URL.Path
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &f.loginBody)
			if f.loginBody["jwt"] != "sa-token-from-file" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"errors":["permission denied"]}`))
				return
			}
			_, _ = w.Write([]byte(`{"auth":{"client_token":"session-token-1"}}`))
		case r.URL.Path == "/v1/kv/data/app/db":
			f.namespace = r.Header.Get("X-Vault-Namespace")
			f.readToken = r.Header.Get("X-Vault-Token")
			_, _ = w.Write([]byte(`{"data":{"data":{"password":"db-pw-0123","port":5432},"metadata":{"version":4}}}`))
		case r.URL.Path == "/v1/kv/data/app/forbidden":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"errors":["1 error occurred:\n\t* permission denied\n\n"]}`))
		case r.URL.Path == "/v1/auth/token/revoke-self":
			f.revoked = r.Header.Get("X-Vault-Token")
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":[]}`))
		}
	})
}

func withLoopback(t *testing.T) {
	t.Helper()
	t.Cleanup(netguard.SetOutboundForTesting(netguard.Policy{AllowLoopback: true}))
}

func TestVaultAmbientKubernetesLogin(t *testing.T) {
	withLoopback(t)
	f := &fakeVault{}
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("sa-token-from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	prev := vaultServiceAccountJWTPath
	vaultServiceAccountJWTPath = tokenFile
	t.Cleanup(func() { vaultServiceAccountJWTPath = prev })

	s := Settings{"address": srv.URL, "mount": "kv", "namespace": "team-a"}
	st, err := Vault().Open(context.Background(), s, Identity{Method: AuthAmbient, Settings: Settings{"role": "brokoli", "auth_mount": "k8s-prod"}})
	if err != nil {
		t.Fatal(err)
	}
	if f.loginPath != "/v1/auth/k8s-prod/login" || f.loginBody["role"] != "brokoli" {
		t.Fatalf("login %s %v", f.loginPath, f.loginBody)
	}
	sec, err := st.Get(context.Background(), "app/db", "")
	if err != nil {
		t.Fatal(err)
	}
	if string(sec.Fields["password"]) != "db-pw-0123" || string(sec.Fields["port"]) != "5432" || sec.Version != "4" {
		t.Fatalf("secret = %+v", sec)
	}
	if f.readToken != "session-token-1" || f.namespace != "team-a" {
		t.Fatalf("read with token %q namespace %q", f.readToken, f.namespace)
	}
	_ = st.Close()
	if f.revoked != "session-token-1" {
		t.Fatal("a token logged in for the run was not revoked on Close")
	}

	// A token file the server refuses: a permission error, naming the role.
	if err := os.WriteFile(tokenFile, []byte("other"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = Vault().Open(context.Background(), s, Identity{Method: AuthAmbient, Settings: Settings{"role": "brokoli"}})
	if !errors.Is(err, ErrPermission) || !strings.Contains(err.Error(), `role "brokoli"`) || strings.Contains(err.Error(), "other") {
		t.Fatalf("refused login: %v", err)
	}
	if f.loginPath != "/v1/auth/kubernetes/login" {
		t.Fatalf("ambient without auth_mount logged in at %s, want the default kubernetes mount", f.loginPath)
	}
	// No token file: ambient says so.
	vaultServiceAccountJWTPath = filepath.Join(t.TempDir(), "missing")
	if _, err := Vault().Open(context.Background(), s, Identity{Method: AuthAmbient, Settings: Settings{"role": "b"}}); err == nil ||
		!strings.Contains(err.Error(), "service-account token") {
		t.Fatalf("missing token file: %v", err)
	}
}

func TestVaultErrorsNameThePathNotTheToken(t *testing.T) {
	withLoopback(t)
	f := &fakeVault{}
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	st, err := Vault().Open(context.Background(), Settings{"address": srv.URL, "mount": "kv"}, Identity{Method: AuthToken, Token: "static-token-secret"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.Get(context.Background(), "app/missing", "")
	if !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "kv/data/app/missing") {
		t.Fatalf("not found: %v", err)
	}
	_, err = st.Get(context.Background(), "app/forbidden", "")
	if !errors.Is(err, ErrPermission) || !strings.Contains(err.Error(), "kv/data/app/forbidden") || strings.Contains(err.Error(), "static-token-secret") {
		t.Fatalf("forbidden: %v", err)
	}
	for _, v := range []string{"latest", "0", "-1"} {
		if _, err := st.Get(context.Background(), "app/db", v); err == nil || !strings.Contains(err.Error(), "positive whole number") {
			t.Fatalf("version %q: %v", v, err)
		}
	}
	for _, p := range []string{"../sys/raw", "app/../../sys/raw", "/"} {
		if _, err := st.Get(context.Background(), p, ""); err == nil || !strings.Contains(err.Error(), "invalid secret path") {
			t.Fatalf("path %q: %v", p, err)
		}
	}
	_ = st.Close()
	if f.revoked != "" {
		t.Fatal("a static token was revoked: it is the customer's, not a session")
	}
}

// Every request goes through the outbound policy: the default refuses the
// loopback fake.
func TestVaultGoesThroughTheOutboundPolicy(t *testing.T) {
	t.Cleanup(netguard.SetOutboundForTesting(netguard.Policy{}))
	f := &fakeVault{}
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	st, err := Vault().Open(context.Background(), Settings{"address": srv.URL, "mount": "kv"}, Identity{Method: AuthToken, Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Get(context.Background(), "app/db", ""); err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("err = %v, want the outbound policy's refusal", err)
	}
}

// A redirect to another host is refused before it is followed: Go would
// copy X-Vault-Token to it.
func TestVaultRefusesARedirectAwayFromTheServer(t *testing.T) {
	withLoopback(t)
	var leaked string
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get("X-Vault-Token")
		_, _ = w.Write([]byte(`{"data":{"data":{"password":"x"},"metadata":{"version":1}}}`))
	}))
	t.Cleanup(elsewhere.Close)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirector.Close)
	st, err := Vault().Open(context.Background(), Settings{"address": redirector.URL}, Identity{Method: AuthToken, Token: "static-token-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Get(context.Background(), "app/db", ""); err == nil || !strings.Contains(err.Error(), "refusing a redirect") ||
		strings.Contains(err.Error(), "static-token-secret") {
		t.Fatalf("err = %v, want the redirect refused", err)
	}
	if leaked != "" {
		t.Fatal("the token followed a redirect to another host")
	}

	// A redirect within the same server is followed.
	same := httptest.NewServer(nil)
	same.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/secret/data/old" {
			http.Redirect(w, r, "/v1/secret/data/new", http.StatusTemporaryRedirect)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"data":{"password":"moved"},"metadata":{"version":2}}}`))
	})
	t.Cleanup(same.Close)
	st, err = Vault().Open(context.Background(), Settings{"address": same.URL}, Identity{Method: AuthToken, Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if sec, err := st.Get(context.Background(), "old", ""); err != nil || string(sec.Fields["password"]) != "moved" {
		t.Fatalf("same-server redirect: %+v %v", sec, err)
	}

	// The outbound policy's own redirect rules still apply on the same
	// server: a loop stops.
	loop := httptest.NewServer(nil)
	loop.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.Path, http.StatusTemporaryRedirect)
	})
	t.Cleanup(loop.Close)
	st, err = Vault().Open(context.Background(), Settings{"address": loop.URL}, Identity{Method: AuthToken, Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Get(context.Background(), "a", ""); err == nil || !strings.Contains(err.Error(), "stopped after 10 redirects") {
		t.Fatalf("redirect loop: %v", err)
	}
}

// Open checks the settings again: a stored row is not proof they were
// validated, and a mount reaches the request path.
func TestVaultOpenRevalidatesSettings(t *testing.T) {
	for name, s := range map[string]Settings{
		"bad mount":     {"address": "https://vault.example.com", "mount": "../sys"},
		"plain http":    {"address": "http://vault.example.com"},
		"bad namespace": {"address": "https://vault.example.com", "namespace": "a b"},
	} {
		if _, err := Vault().Open(context.Background(), s, Identity{Method: AuthToken, Token: "t"}); err == nil {
			t.Errorf("%s: opened", name)
		}
	}
	if _, err := Vault().Open(context.Background(), Settings{"address": "https://vault.example.com"},
		Identity{Method: AuthOIDC, Settings: Settings{"role": "r", "auth_mount": "../token"}, Token: "jwt"}); err == nil ||
		!strings.Contains(err.Error(), "auth_mount") {
		t.Errorf("bad auth_mount: %v", err)
	}
	if _, err := Vault().Open(context.Background(), Settings{"address": "https://vault.example.com"}, Identity{Method: AuthToken}); err == nil ||
		!strings.Contains(err.Error(), "no token") {
		t.Errorf("empty token: %v", err)
	}
}

func TestVaultReasonIsOneLine(t *testing.T) {
	for in, want := range map[string]string{
		`{"errors":["1 error occurred:\n\t* permission denied\n\n"]}`:      "permission denied",
		`{"errors":["2 errors occurred:\n\t* role not found\n\t* bad\n"]}`: "role not found; bad",
		`{"errors":[]}`: "no reason given",
		`not json`:      "no reason given",
	} {
		if got := vaultReason([]byte(in)); got != want {
			t.Errorf("vaultReason(%s) = %q, want %q", in, got, want)
		}
	}
}

// Responses the provider must not take for a secret: null data at a
// version, a body past the size cap, a login without a client token.
func TestVaultRefusesMalformedResponses(t *testing.T) {
	withLoopback(t)
	huge := `{"data":{"data":{"password":"` + strings.Repeat("a", vaultMaxResponse+1) + `"},"metadata":{"version":1}}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/secret/data/null":
			_, _ = w.Write([]byte(`{"data":{"data":null,"metadata":{"version":3}}}`))
		case "/v1/secret/data/huge":
			_, _ = w.Write([]byte(huge))
		case "/v1/auth/jwt/login":
			_, _ = w.Write([]byte(`{"auth":null}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	s := Settings{"address": srv.URL}
	st, err := Vault().Open(context.Background(), s, Identity{Method: AuthToken, Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Get(context.Background(), "null", ""); !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "deleted or destroyed") {
		t.Errorf("null data: %v", err)
	}
	if sec, err := st.Get(context.Background(), "huge", ""); err == nil {
		t.Errorf("a response past the %d-byte cap was read: %d bytes", vaultMaxResponse, len(sec.Fields["password"]))
	}
	if _, err := Vault().Open(context.Background(), s, Identity{Method: AuthOIDC, Settings: Settings{"role": "r"}, Token: "jwt"}); err == nil ||
		!strings.Contains(err.Error(), "no client token") {
		t.Errorf("login without a client token: %v", err)
	}
	if _, err := Vault().Open(context.Background(), s, Identity{Method: AuthOIDC, Settings: Settings{"role": "r"}}); err == nil ||
		!strings.Contains(err.Error(), "no token to present") {
		t.Errorf("oidc without a token: %v", err)
	}
}
