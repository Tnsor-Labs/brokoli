package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/identity"
	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
	"github.com/Tnsor-Labs/brokoli/store"
)

// A service-account key as the catalogue hint describes it: the whole extra
// document. The private key is a marker, not a real key.
const bigQueryTestKey = `{"type":"service_account","project_id":"acme","private_key_id":"k1",` +
	`"private_key":"-----BEGIN PRIVATE KEY-----\nMARKER-bq-private-key\n-----END PRIVATE KEY-----\n",` +
	`"client_email":"loader@acme.iam.gserviceaccount.com"}`

type bigQueryTokenSource struct{}

func (bigQueryTokenSource) Token(context.Context, identity.TokenRequest) (string, error) {
	return "token", nil
}

func bigQueryStore(t *testing.T) *store.SQLiteStore {
	t.Helper()
	st, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "bq.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := time.Now().UTC()
	for _, c := range []models.Connection{
		{ID: "1", ConnID: "warehouse", Type: models.ConnTypeBigQuery, Schema: "acme.analytics", Extra: bigQueryTestKey, WorkspaceID: "ws-a"},
		{ID: "2", ConnID: "pg", Type: models.ConnTypePostgres, Host: "db.invalid", Port: 5432, Schema: "db", Login: "u", WorkspaceID: "ws-a"},
	} {
		c := c
		c.CreatedAt, c.UpdatedAt = now, now
		if err := st.CreateConnection(&c); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

// ADR-042 section 1: the node config carries the secret-free URI and the
// conn_id, never the key. It used to carry the resolved extra document --
// the whole service-account key -- as "bigquery_extra".
func TestABigQueryNodeConfigCarriesNoCredential(t *testing.T) {
	cr := NewConnectionResolver(bigQueryStore(t), nil)
	for _, nodeType := range []models.NodeType{models.NodeTypeSourceDB, models.NodeTypeSinkDB} {
		resolved, _, err := cr.ResolveWithWarningsIn(map[string]interface{}{"conn_id": "warehouse", "query": "SELECT 1"}, nodeType, "ws-a")
		if err != nil {
			t.Fatal(err)
		}
		if uri, _ := resolved["uri"].(string); !strings.HasPrefix(uri, "bigquery://") {
			t.Fatalf("%s: uri = %q, want a bigquery:// URI", nodeType, uri)
		}
		for key, value := range resolved {
			if strings.Contains(fmt.Sprint(value), "MARKER-bq-private-key") || strings.Contains(fmt.Sprint(value), "service_account") {
				t.Errorf("%s: node config %q carries the connection's key", nodeType, key)
			}
		}
	}
}

func bigQueryRunner(st store.Store, workspace string) *Runner {
	return &Runner{
		connResolver: NewConnectionResolver(st, nil),
		pipe:         &models.Pipeline{WorkspaceID: workspace},
	}
}

// The backend resolves the key itself, by conn_id, in the run's workspace.
func TestBigQuerySettingsAreResolvedWhereTheNodeRuns(t *testing.T) {
	st := bigQueryStore(t)

	got, err := bigQueryRunner(st, "ws-a").bigQuerySettings(map[string]interface{}{"conn_id": "warehouse"}, "node")
	if err != nil || got.settings != bigQueryTestKey {
		t.Fatalf("own workspace: settings %q, err %v", got.settings, err)
	}

	if _, err := bigQueryRunner(st, "ws-b").bigQuerySettings(map[string]interface{}{"conn_id": "warehouse"}, "node"); err == nil ||
		!strings.Contains(err.Error(), "not found in this pipeline's workspace") {
		t.Fatalf("another workspace's connection: err = %v", err)
	}

	if _, err := bigQueryRunner(st, "ws-a").bigQuerySettings(map[string]interface{}{"conn_id": "pg"}, "node"); err == nil ||
		!strings.Contains(err.Error(), "not bigquery") {
		t.Fatalf("a non-BigQuery connection: err = %v", err)
	}

	if got, err := bigQueryRunner(st, "ws-a").bigQuerySettings(map[string]interface{}{}, "node"); err != nil || got.settings != "" {
		t.Fatalf("no conn_id: settings %q, err %v; want none, for the machine's identity", got.settings, err)
	}

	r := &Runner{pipe: &models.Pipeline{WorkspaceID: "ws-a"}}
	if _, err := r.bigQuerySettings(map[string]interface{}{"conn_id": "warehouse"}, "node"); err == nil {
		t.Fatal("a runner with no resolver produced settings")
	}
}

// With no key, the backend would use the machine's own identity. Where
// ambient identity is denied it is refused by name, before any request.
func TestBigQueryRefusesTheMachineIdentityWhereAmbientIsDenied(t *testing.T) {
	t.Setenv("BROKOLI_BIGQUERY_ENDPOINT", "")
	t.Setenv(identity.AmbientEnv, "deny")
	_, err := bigQueryClient(context.Background(), "bigquery://acme/analytics", googleAuth{})
	if !errors.Is(err, identity.ErrAmbientDenied) {
		t.Fatalf("err = %v, want ErrAmbientDenied", err)
	}
}

// A credential document that is not a service-account key is refused: an
// external-account file can make Google's library fetch any URL or run a
// command (ADR-042 section 2).
func TestBigQueryRefusesACredentialThatIsNotAServiceAccountKey(t *testing.T) {
	t.Setenv("BROKOLI_BIGQUERY_ENDPOINT", "")
	external := `{"type":"external_account","credential_source":{"executable":{"command":"/bin/true"}}}`
	for name, settings := range map[string]string{
		"external account":            external,
		"external account, nested in": `{"credentials":` + fmt.Sprintf("%q", external) + `}`,
	} {
		if _, err := bigQueryClient(context.Background(), "bigquery://acme/analytics", googleAuth{settings: settings}); err == nil ||
			!strings.Contains(err.Error(), "service_account") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestBigQueryOIDCRequiresDeploymentTokenSource(t *testing.T) {
	t.Setenv("BROKOLI_BIGQUERY_ENDPOINT", "")
	settings := `{"auth_method":"oidc","provider":"//iam.googleapis.com/projects/123456789/locations/global/workloadIdentityPools/brokoli/providers/runs"}`
	_, err := bigQueryClient(context.Background(), "bigquery://acme/analytics", googleAuth{settings: settings})
	if !errors.Is(err, identity.ErrNoTokenSource) {
		t.Fatalf("err = %v, want ErrNoTokenSource", err)
	}
}

const bigQueryTestProvider = "//iam.googleapis.com/projects/123456789/locations/global/workloadIdentityPools/brokoli/providers/runs"

// recordingTokenSource returns a fixed subject token and records requests.
type recordingTokenSource struct {
	mu   sync.Mutex
	reqs []identity.TokenRequest
}

func (s *recordingTokenSource) Token(_ context.Context, req identity.TokenRequest) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqs = append(s.reqs, req)
	return "subject-jwt", nil
}

// fakeGoogleFederation answers Google's token exchange and IAM Credentials
// impersonation, and records what each was sent.
func fakeGoogleFederation(t *testing.T) (url string, exchange map[string]string, impersonated *string) {
	t.Helper()
	exchange = map[string]string{}
	var who string
	var mu sync.Mutex
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		for k := range r.PostForm {
			exchange[k] = r.PostForm.Get(k)
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"federated","issued_token_type":"urn:ietf:params:oauth:token-type:access_token","token_type":"Bearer","expires_in":3600}`))
	})
	mux.HandleFunc("/v1/projects/-/serviceAccounts/", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		who = r.URL.Path
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"accessToken":"impersonated","expireTime":%q}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	prev := googleFederationEndpoints
	googleFederationEndpoints.tokenURL = srv.URL + "/v1/token"
	googleFederationEndpoints.impersonationURL = srv.URL
	t.Cleanup(func() { googleFederationEndpoints = prev })
	return srv.URL, exchange, &who
}

// End to end from the node's view: the runner resolves an oidc connection,
// and the credentials it produces exchange a token that names the run's
// workspace, the connection's immutable ID, the run and the node, then
// impersonate the configured service account.
func TestBigQueryOIDCExchangesATokenForTheRun(t *testing.T) {
	_, exchange, impersonated := fakeGoogleFederation(t)
	st, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "bq-oidc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := time.Now().UTC()
	settings := `{"auth_method":"oidc","provider":"` + bigQueryTestProvider + `","service_account":"loader@acme.iam.gserviceaccount.com"}`
	if err := st.CreateConnection(&models.Connection{ID: "conn-uuid-1", ConnID: "warehouse-oidc", Type: models.ConnTypeBigQuery,
		Schema: "acme.analytics", Extra: settings, WorkspaceID: "ws-a", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	tokens := &recordingTokenSource{}
	cr := NewConnectionResolver(st, nil)
	cr.SetTokenSource(tokens)
	r := &Runner{connResolver: cr, pipe: &models.Pipeline{WorkspaceID: "ws-a"}, run: &models.Run{ID: "run-9"}}

	auth, err := r.bigQuerySettings(map[string]interface{}{"conn_id": "warehouse-oidc"}, "node-3")
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(auth.settings), &raw); err != nil {
		t.Fatal(err)
	}
	creds, err := googleOIDCCredentials("BigQuery", raw, auth, netguard.Policy{AllowLoopback: true}.Client(10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := creds.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok.Value != "impersonated" {
		t.Fatalf("access token = %q, want the impersonated one", tok.Value)
	}

	if len(tokens.reqs) != 1 {
		t.Fatalf("the token source was asked %d times, want 1", len(tokens.reqs))
	}
	want := identity.TokenRequest{Audience: "https:" + bigQueryTestProvider, WorkspaceID: "ws-a",
		SubjectKind: "connection", SubjectID: "conn-uuid-1", RunID: "run-9", NodeID: "node-3"}
	if got := tokens.reqs[0]; got != want {
		t.Errorf("token request = %+v\nwant           %+v", got, want)
	}
	if exchange["audience"] != bigQueryTestProvider || exchange["subject_token"] != "subject-jwt" {
		t.Errorf("token exchange got audience %q, subject token %q", exchange["audience"], exchange["subject_token"])
	}
	if !strings.Contains(*impersonated, "loader@acme.iam.gserviceaccount.com:generateAccessToken") {
		t.Errorf("impersonated %q, want the configured service account", *impersonated)
	}
}

// A configured token_audience replaces Google's default.
func TestBigQueryOIDCUsesAConfiguredTokenAudience(t *testing.T) {
	fakeGoogleFederation(t)
	tokens := &recordingTokenSource{}
	raw := map[string]interface{}{"auth_method": "oidc", "provider": bigQueryTestProvider, "token_audience": "brokoli-runs"}
	creds, err := googleOIDCCredentials("BigQuery", raw, googleAuth{tokens: tokens}, netguard.Policy{AllowLoopback: true}.Client(10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := creds.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	if tokens.reqs[0].Audience != "brokoli-runs" {
		t.Fatalf("audience = %q", tokens.reqs[0].Audience)
	}
}

// An unknown auth_method is refused by name, not read as a key.
func TestBigQueryRefusesAnUnknownAuthMethod(t *testing.T) {
	t.Setenv("BROKOLI_BIGQUERY_ENDPOINT", "")
	_, err := bigQueryClient(context.Background(), "bigquery://acme/analytics", googleAuth{settings: `{"auth_method":"odic","provider":"x"}`})
	if err == nil || !strings.Contains(err.Error(), `auth_method "odic" is not supported`) {
		t.Fatalf("err = %v", err)
	}
}

// The credentials reach the request. option.WithHTTPClient takes precedence
// over option.WithAuthCredentials, so passing both sent every BigQuery
// request without credentials -- a 401 from real Google that the emulator
// (which runs without authentication) could never show. A fake BigQuery API
// records the Authorization header an oidc connection's request carries.
func TestBigQueryRequestsCarryTheConnectionsCredentials(t *testing.T) {
	t.Setenv("BROKOLI_BIGQUERY_ENDPOINT", "")
	fakeGoogleFederation(t)
	var mu sync.Mutex
	var authorization []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		authorization = append(authorization, r.Header.Get("Authorization"))
		mu.Unlock()
		http.Error(w, `{"error":{"code":400,"message":"fake"}}`, http.StatusBadRequest)
	}))
	t.Cleanup(api.Close)
	prev := bigQueryAPIEndpoint
	bigQueryAPIEndpoint = api.URL + "/"
	t.Cleanup(func() { bigQueryAPIEndpoint = prev })
	t.Cleanup(netguard.SetOutboundForTesting(netguard.Policy{AllowLoopback: true}))

	settings := `{"auth_method":"oidc","provider":"` + bigQueryTestProvider + `","service_account":"loader@acme.iam.gserviceaccount.com"}`
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = CheckBigQueryConnection(ctx, "bigquery://acme/analytics", nil, settings, &recordingTokenSource{},
		identity.TokenRequest{WorkspaceID: "ws-a", SubjectKind: "connection", SubjectID: "conn-uuid-1"})

	mu.Lock()
	defer mu.Unlock()
	if len(authorization) == 0 {
		t.Fatal("no request reached the BigQuery API")
	}
	for i, h := range authorization {
		if h != "Bearer impersonated" {
			t.Errorf("request %d carried Authorization %q, want the connection's impersonated token", i, h)
		}
	}
}

func TestBigQueryQuotaProjectComesFromURI(t *testing.T) {
	for uri, want := range map[string]string{
		"bigquery://analytics/events?billing_project=billing-prod":             "billing-prod",
		"bigquery://analytics/events?billing_project=example.com:billing-prod": "example.com:billing-prod",
		"bigquery://analytics/events":                                          "",
	} {
		got, err := bigQueryQuotaProject(uri)
		if err != nil || got != want {
			t.Errorf("bigQueryQuotaProject(%q) = %q, %v; want %q", uri, got, err, want)
		}
	}
	// It becomes a request header, so anything that is not a project ID is
	// refused.
	for _, bad := range []string{"Billing", "bp", "billing prod", "billing-prod%0D%0AX-Evil:1"} {
		if _, err := bigQueryQuotaProject("bigquery://analytics/events?billing_project=" + bad); err == nil {
			t.Errorf("billing_project %q was accepted", bad)
		}
	}
}

// The billing project reaches the request as the quota-project header. It
// was passed as option.WithQuotaProject beside WithHTTPClient, which the
// library refuses outright ("WithHTTPClient is incompatible with
// QuotaProject"), so every call on such a connection failed.
func TestBigQueryRequestsCarryTheBillingProject(t *testing.T) {
	t.Setenv("BROKOLI_BIGQUERY_ENDPOINT", "")
	fakeGoogleFederation(t)
	var mu sync.Mutex
	var quota []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		quota = append(quota, r.Header.Get("X-Goog-User-Project"))
		mu.Unlock()
		http.Error(w, `{"error":{"code":400,"message":"fake"}}`, http.StatusBadRequest)
	}))
	t.Cleanup(api.Close)
	prev := bigQueryAPIEndpoint
	bigQueryAPIEndpoint = api.URL + "/"
	t.Cleanup(func() { bigQueryAPIEndpoint = prev })
	t.Cleanup(netguard.SetOutboundForTesting(netguard.Policy{AllowLoopback: true}))
	settings := `{"auth_method":"oidc","provider":"` + bigQueryTestProvider + `"}`

	for uri, want := range map[string]string{
		"bigquery://acme/analytics?billing_project=billing-prod": "billing-prod",
		"bigquery://acme/analytics":                              "",
	} {
		mu.Lock()
		quota = nil
		mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		err := CheckBigQueryConnection(ctx, uri, nil, settings, &recordingTokenSource{}, identity.TokenRequest{SubjectKind: "connection", SubjectID: "c"})
		cancel()
		mu.Lock()
		got := append([]string(nil), quota...)
		mu.Unlock()
		if len(got) == 0 {
			t.Fatalf("%s: no request reached the BigQuery API (err: %v)", uri, err)
		}
		for i, h := range got {
			if h != want {
				t.Errorf("%s: request %d carried X-Goog-User-Project %q, want %q", uri, i, h, want)
			}
		}
	}
}
