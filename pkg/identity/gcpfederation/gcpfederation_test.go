package gcpfederation

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/identity"
	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
)

const (
	provider       = "//iam.googleapis.com/projects/123456789/locations/global/workloadIdentityPools/brokoli/providers/runs"
	serviceAccount = "loader@acme-analytics.iam.gserviceaccount.com"
	subjectJWT     = "header.payload.signature"
)

// fakeGoogle stands in for Google's STS token exchange and the IAM
// Credentials impersonation endpoint, and records what it was sent.
type fakeGoogle struct {
	mu            sync.Mutex
	exchange      map[string]string
	impersonation string
	impersonateTo string
}

func (g *fakeGoogle) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("exchange form: %v", err)
		}
		g.mu.Lock()
		g.exchange = map[string]string{}
		for k := range r.PostForm {
			g.exchange[k] = r.PostForm.Get(k)
		}
		g.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token":      "federated-access-token",
			"issued_token_type": "urn:ietf:params:oauth:token-type:access_token",
			"token_type":        "Bearer",
			"expires_in":        3600,
		})
	})
	mux.HandleFunc("/v1/projects/-/serviceAccounts/", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.impersonation = r.Header.Get("Authorization")
		g.impersonateTo = r.URL.Path
		g.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"accessToken": "impersonated-access-token",
			"expireTime":  time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		})
	})
	return mux
}

// recordingSource serves a fixed token and records each request.
type recordingSource struct {
	mu   sync.Mutex
	reqs []identity.TokenRequest
}

func (s *recordingSource) Token(_ context.Context, req identity.TokenRequest) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqs = append(s.reqs, req)
	return subjectJWT, nil
}

type machineSource struct{ recordingSource }

func (*machineSource) MachineIdentity() bool { return true }

func loopbackClient() *http.Client {
	return netguard.Policy{AllowLoopback: true}.Client(10 * time.Second)
}

func request() identity.TokenRequest {
	return identity.TokenRequest{Audience: "ignored", WorkspaceID: "ws-1", SubjectKind: "connection", SubjectID: "conn-1", RunID: "run-1", NodeID: "node-1"}
}

func TestFederationExchangesTheTokenWithGoogle(t *testing.T) {
	g := &fakeGoogle{}
	srv := httptest.NewServer(g.handler(t))
	defer srv.Close()
	src := &recordingSource{}

	creds, err := Credentials(src, request(), Config{Provider: provider, TokenURL: srv.URL + "/v1/token"}, loopbackClient())
	if err != nil {
		t.Fatal(err)
	}
	tok, err := creds.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok.Value != "federated-access-token" {
		t.Fatalf("access token = %q", tok.Value)
	}

	for k, want := range map[string]string{
		"grant_type":         "urn:ietf:params:oauth:grant-type:token-exchange",
		"audience":           provider,
		"subject_token":      subjectJWT,
		"subject_token_type": "urn:ietf:params:oauth:token-type:jwt",
		"scope":              DefaultScope,
	} {
		if got := g.exchange[k]; got != want {
			t.Errorf("exchange %s = %q, want %q", k, got, want)
		}
	}
	if len(src.reqs) != 1 {
		t.Fatalf("the source was asked %d times, want 1", len(src.reqs))
	}
	got := src.reqs[0]
	if got.Audience != "https:"+provider {
		t.Errorf("token audience = %q, want Google's default for the provider", got.Audience)
	}
	if got.WorkspaceID != "ws-1" || got.SubjectID != "conn-1" || got.RunID != "run-1" || got.NodeID != "node-1" {
		t.Errorf("the source did not get whose work the token is for: %+v", got)
	}
}

func TestFederationImpersonatesTheServiceAccount(t *testing.T) {
	g := &fakeGoogle{}
	srv := httptest.NewServer(g.handler(t))
	defer srv.Close()

	creds, err := Credentials(&recordingSource{}, request(), Config{
		Provider: provider, ServiceAccount: serviceAccount,
		TokenURL: srv.URL + "/v1/token", ImpersonationURL: srv.URL,
	}, loopbackClient())
	if err != nil {
		t.Fatal(err)
	}
	tok, err := creds.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok.Value != "impersonated-access-token" {
		t.Fatalf("access token = %q, want the impersonated one", tok.Value)
	}
	if g.impersonation != "Bearer federated-access-token" {
		t.Errorf("impersonation was authorised with %q, want the federated token", g.impersonation)
	}
	if !strings.Contains(g.impersonateTo, serviceAccount+":generateAccessToken") {
		t.Errorf("impersonated %q, want %s", g.impersonateTo, serviceAccount)
	}
}

// A custom audience replaces Google's default, for a provider configured
// with its own allowed audiences.
func TestFederationUsesAConfiguredTokenAudience(t *testing.T) {
	g := &fakeGoogle{}
	srv := httptest.NewServer(g.handler(t))
	defer srv.Close()
	src := &recordingSource{}
	creds, err := Credentials(src, request(), Config{Provider: provider, TokenAudience: "brokoli", TokenURL: srv.URL + "/v1/token"}, loopbackClient())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := creds.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	if src.reqs[0].Audience != "brokoli" {
		t.Fatalf("audience = %q", src.reqs[0].Audience)
	}
}

// A machine's token is refused where ambient identity is denied, before
// anything reaches Google.
func TestFederationRefusesAMachineTokenWhereAmbientIsDenied(t *testing.T) {
	g := &fakeGoogle{}
	srv := httptest.NewServer(g.handler(t))
	defer srv.Close()
	t.Setenv(identity.AmbientEnv, "deny")

	creds, err := Credentials(&machineSource{}, request(), Config{Provider: provider, TokenURL: srv.URL + "/v1/token"}, loopbackClient())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := creds.Token(context.Background()); !errors.Is(err, identity.ErrAmbientDenied) {
		t.Fatalf("err = %v, want ErrAmbientDenied", err)
	}
	if g.exchange != nil {
		t.Fatal("a token exchange reached Google")
	}
}

// With no client given, the exchange goes through the deployment's
// outbound policy, which refuses loopback by default and allows it when
// the operator does. Both directions.
func TestFederationGoesThroughTheOutboundPolicy(t *testing.T) {
	g := &fakeGoogle{}
	srv := httptest.NewServer(g.handler(t))
	defer srv.Close()
	cfg := Config{Provider: provider, TokenURL: srv.URL + "/v1/token"}

	restore := netguard.SetOutboundForTesting(netguard.Policy{})
	creds, err := Credentials(&recordingSource{}, request(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = creds.Token(context.Background())
	restore()
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("the default policy did not refuse a loopback exchange: %v", err)
	}

	defer netguard.SetOutboundForTesting(netguard.Policy{AllowLoopback: true})()
	creds, err = Credentials(&recordingSource{}, request(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := creds.Token(context.Background()); err != nil {
		t.Fatalf("an allowed exchange failed: %v", err)
	}
}

func TestConfigValidation(t *testing.T) {
	for name, cfg := range map[string]Config{
		"empty provider":            {},
		"provider without pool":     {Provider: "//iam.googleapis.com/projects/1/locations/global/providers/x"},
		"provider with a query":     {Provider: provider + "?x=1"},
		"https provider":            {Provider: "https:" + provider},
		"service account not email": {Provider: provider, ServiceAccount: "loader"},
		"service account elsewhere": {Provider: provider, ServiceAccount: "loader@example.com"},
	} {
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: accepted %+v", name, cfg)
		}
	}
	for _, sa := range []string{"", serviceAccount, "123-compute@developer.gserviceaccount.com"} {
		if err := (Config{Provider: provider, ServiceAccount: sa}).Validate(); err != nil {
			t.Errorf("service account %q refused: %v", sa, err)
		}
	}
}

func TestCredentialsWithNoSource(t *testing.T) {
	if _, err := Credentials(nil, request(), Config{Provider: provider}, nil); !errors.Is(err, identity.ErrNoTokenSource) {
		t.Fatalf("err = %v", err)
	}
}
