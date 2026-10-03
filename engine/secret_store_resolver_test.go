package engine

import (
	"context"
	"encoding/json"
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
	"github.com/Tnsor-Labs/brokoli/pkg/secrets"
	"github.com/Tnsor-Labs/brokoli/pkg/secretstore"
	"github.com/Tnsor-Labs/brokoli/pkg/secretstore/secretstoretest"
	"github.com/Tnsor-Labs/brokoli/store"
)

type recordingTokens struct {
	mu   sync.Mutex
	reqs []identity.TokenRequest
}

func (r *recordingTokens) Token(_ context.Context, req identity.TokenRequest) (string, error) {
	r.mu.Lock()
	r.reqs = append(r.reqs, req)
	r.mu.Unlock()
	return "jwt-for-" + req.SubjectID, nil
}

type storeFixture struct {
	st       *store.SQLiteStore
	key      interface{ Encrypt(string) (string, error) }
	kv       *secretstoretest.Provider
	ssm      *secretstoretest.Provider
	chain    *secrets.Chain
	resolver *SecretStoreResolver
	tokens   *recordingTokens
}

func newStoreFixture(t *testing.T) *storeFixture {
	t.Helper()
	st, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "stores.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	key := testKey(5)
	kv := secretstoretest.New("fakekv", secretstore.ShapeMap, map[string]interface{}{
		"warehouse/loader": map[string]string{"password": "warehouse-pw-0123", "user": "loader"},
		"aws/prod":         map[string]string{"secret_key": "aws-secret-key-0123"},
	})
	ssm := secretstoretest.New("fakessm", secretstore.ShapeString, map[string]interface{}{
		"/prod/api-token": "api-token-value-0123",
	})
	enc := secrets.NewEncryptedResolver(key)
	chain := secrets.NewChain(enc, enc)
	res := NewSecretStoreResolver(st, secretstore.NewRegistry(kv, ssm), chain)
	tokens := &recordingTokens{}
	res.SetTokenSource(tokens)
	chain.Register(res)
	sealed, _ := key.Encrypt("good-token")
	now := time.Now().UTC()
	for _, s := range []models.SecretStore{
		{ID: "st-kv", Name: "vault-prod", WorkspaceID: "ws-a", Provider: "fakekv", Settings: map[string]string{"address": "https://v"},
			AuthMethod: "token", CredentialRef: "encrypted://" + sealed},
		{ID: "st-ssm", Name: "ssm", WorkspaceID: "ws-a", Provider: "fakessm", Settings: map[string]string{"address": "eu"},
			AuthMethod: "oidc", AuthSettings: map[string]string{"role": "r"}},
		{ID: "st-amb", Name: "ambient", WorkspaceID: "ws-a", Provider: "fakessm", Settings: map[string]string{"address": "eu"},
			AuthMethod: "ambient"},
		{ID: "st-b", Name: "vault-prod", WorkspaceID: "ws-b", Provider: "fakekv", Settings: map[string]string{"address": "https://other"},
			AuthMethod: "token", CredentialRef: "encrypted://" + sealed},
	} {
		s := s
		s.CreatedAt, s.UpdatedAt = now, now
		if err := st.CreateSecretStore(&s); err != nil {
			t.Fatal(err)
		}
	}
	return &storeFixture{st: st, key: key, kv: kv, ssm: ssm, chain: chain, resolver: res, tokens: tokens}
}

func TestSecretStoreReferencesResolve(t *testing.T) {
	f := newStoreFixture(t)
	ctx := context.Background()
	a := secrets.Scope{WorkspaceID: "ws-a", RunID: "run-res-1", NodeID: "n1"}
	t.Cleanup(func() { dropRunSecretState("run-res-1") })

	if v, err := f.chain.ResolveIn(ctx, a, "secret://vault-prod/warehouse/loader#password"); err != nil || v != "warehouse-pw-0123" {
		t.Fatalf("map field: %q %v", v, err)
	}
	if got := f.kv.Opened(); len(got) != 1 || got[0].Method != secretstore.AuthToken || got[0].Token != "good-token" {
		t.Fatalf("token store opened with %+v; want the decrypted stored token", got)
	}
	if v, err := f.chain.ResolveIn(ctx, a, "secret://ssm//prod/api-token"); err != nil || v != "api-token-value-0123" {
		t.Fatalf("string secret: %q %v", v, err)
	}
	// oidc: a token naming the store by its immutable ID, the run and node.
	if len(f.tokens.reqs) != 1 {
		t.Fatalf("token requests = %d", len(f.tokens.reqs))
	}
	req := f.tokens.reqs[0]
	if req.SubjectKind != "store" || req.SubjectID != "st-ssm" || req.WorkspaceID != "ws-a" || req.RunID != "run-res-1" ||
		req.NodeID != "n1" || req.Audience != "fake:eu" {
		t.Fatalf("token request = %+v", req)
	}
	if got := f.ssm.Opened(); got[0].Method != secretstore.AuthOIDC || got[0].Token != "jwt-for-st-ssm" {
		t.Fatalf("oidc store opened with %+v", got)
	}
}

func TestSecretStoreReferencesAreRefusedWithTheReason(t *testing.T) {
	f := newStoreFixture(t)
	ctx := context.Background()
	a := secrets.Scope{WorkspaceID: "ws-a"}
	for ref, want := range map[string]string{
		"secret://vault-prod/warehouse/loader":      "name one with #<field>",
		"secret://ssm//prod/api-token#value":        "single values; remove #value",
		"secret://nope/x#y":                         `no secret store named "nope" in this workspace`,
		"secret://vault-prod/missing#password":      "secret not found",
		"secret://vault-prod/warehouse/loader#nope": `no field "nope"`,
	} {
		if _, err := f.chain.ResolveIn(ctx, a, ref); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", ref, err, want)
		}
	}
	// No workspace, no store.
	if _, err := f.chain.ResolveIn(ctx, secrets.Scope{}, "secret://vault-prod/warehouse/loader#password"); err == nil ||
		!strings.Contains(err.Error(), "resolves only for a workspace") {
		t.Errorf("unscoped: %v", err)
	}
	// Ambient is refused by name where denied, and allowed otherwise.
	t.Setenv(identity.AmbientEnv, "deny")
	if _, err := f.chain.ResolveIn(ctx, a, "secret://ambient//prod/api-token"); err == nil || !strings.Contains(err.Error(), "ambient identity is disabled") {
		t.Errorf("ambient denied: %v", err)
	}
	t.Setenv(identity.AmbientEnv, "")
	if _, err := f.chain.ResolveIn(ctx, a, "secret://ambient//prod/api-token"); err != nil {
		t.Errorf("ambient allowed: %v", err)
	}
}

// A store name is looked up only in the run's workspace: workspace B's
// store of the same name answers for B, never for A.
func TestSecretStoresAreWorkspaceScoped(t *testing.T) {
	f := newStoreFixture(t)
	ctx := context.Background()
	if _, err := f.chain.ResolveIn(ctx, secrets.Scope{WorkspaceID: "ws-a"}, "secret://vault-prod/warehouse/loader#password"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.chain.ResolveIn(ctx, secrets.Scope{WorkspaceID: "ws-c"}, "secret://vault-prod/warehouse/loader#password"); err == nil ||
		!strings.Contains(err.Error(), "no secret store") {
		t.Fatalf("workspace C resolved another workspace's store: %v", err)
	}
	f.kv.Secrets["warehouse/loader"] = map[string]string{"password": "warehouse-pw-0123"}
	_, _ = f.chain.ResolveIn(ctx, secrets.Scope{WorkspaceID: "ws-b"}, "secret://vault-prod/warehouse/loader#password")
	opened := f.kv.Opened()
	if len(opened) != 2 {
		t.Fatalf("opened %d stores, want one per workspace", len(opened))
	}
}

// One run reads each secret once; another run reads it again; nothing is
// kept after the run.
func TestSecretStoreValuesAreCachedPerRun(t *testing.T) {
	f := newStoreFixture(t)
	ctx := context.Background()
	ref := "secret://vault-prod/warehouse/loader#password"
	for i := 0; i < 5; i++ {
		if _, err := f.chain.ResolveIn(ctx, secrets.Scope{WorkspaceID: "ws-a", RunID: "run-cache-1"}, ref); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(f.kv.Reads()); n != 1 {
		t.Fatalf("one run made %d reads, want 1", n)
	}
	_, _ = f.chain.ResolveIn(ctx, secrets.Scope{WorkspaceID: "ws-a", RunID: "run-cache-2"}, ref)
	if n := len(f.kv.Reads()); n != 2 {
		t.Fatalf("a second run made no read of its own (%d reads)", n)
	}
	dropRunSecretState("run-cache-1")
	dropRunSecretState("run-cache-2")
	if _, ok := runSecretCaches.Load("run-cache-1"); ok {
		t.Fatal("a finished run's values are still cached")
	}
}

// A secret:// reference inside the extra document resolves in place, beside
// plain values, joins the run's redaction set, and an error names the field.
func TestExtraFieldReferencesResolve(t *testing.T) {
	f := newStoreFixture(t)
	now := time.Now().UTC()
	extra, _ := json.Marshal(map[string]interface{}{"bucket": "exports", "access_key": "AKIAPLAIN",
		"secret_key": "secret://vault-prod/aws/prod#secret_key"})
	sealed, _ := f.key.Encrypt(string(extra))
	bad, _ := f.key.Encrypt(`{"bucket":"b","secret_key":"secret://vault-prod/aws/prod#missing"}`)
	for _, c := range []models.Connection{
		{ID: "c1", ConnID: "s3-store", Type: models.ConnTypeS3, WorkspaceID: "ws-a", Extra: sealed, ExtraRef: "encrypted://" + sealed},
		{ID: "c2", ConnID: "s3-bad", Type: models.ConnTypeS3, WorkspaceID: "ws-a", Extra: bad, ExtraRef: "encrypted://" + bad},
	} {
		c := c
		c.CreatedAt, c.UpdatedAt = now, now
		if err := f.st.CreateConnection(&c); err != nil {
			t.Fatal(err)
		}
	}
	cr := NewConnectionResolver(f.st, f.chain)
	scope := secrets.Scope{WorkspaceID: "ws-a", RunID: "run-extra-1"}
	t.Cleanup(func() { dropRunSecretState("run-extra-1") })
	conn, err := cr.ResolveConnectionScoped("s3-store", scope)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]interface{}
	_ = json.Unmarshal([]byte(conn.Extra), &got)
	if got["secret_key"] != "aws-secret-key-0123" || got["access_key"] != "AKIAPLAIN" || got["bucket"] != "exports" {
		t.Fatalf("extra = %v", got)
	}
	if redactRun("run-extra-1", "key aws-secret-key-0123") != "key "+recordedSecretMask {
		t.Fatal("a resolved field value is not redacted")
	}
	if _, err := cr.ResolveConnectionScoped("s3-bad", scope); err == nil || !strings.Contains(err.Error(), `connection "s3-bad": extra.secret_key`) {
		t.Fatalf("a failed field reference must name the field: %v", err)
	}
}

// End to end: a run's source_api node sends a header whose value comes from
// a secret store; the target receives the value; the run's logs do not.
func TestRunResolvesSecretStoreReferences(t *testing.T) {
	f := newStoreFixture(t)
	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Api-Token")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":1}]`))
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(netguard.SetOutboundForTesting(netguard.Policy{AllowLoopback: true}))
	now := time.Now().UTC()
	extra, _ := json.Marshal(map[string]interface{}{"headers": map[string]string{"X-Api-Token": "secret://ssm//prod/api-token"}})
	sealed, _ := f.key.Encrypt(string(extra))
	if err := f.st.CreateConnection(&models.Connection{ID: "c-api", ConnID: "store-api", Type: models.ConnTypeHTTP, Host: "unused",
		WorkspaceID: "ws-a", Extra: sealed, ExtraRef: "encrypted://" + sealed, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	pipe := &models.Pipeline{ID: "store-run", Name: "store-run", Enabled: true, WorkspaceID: "ws-a",
		Nodes: []models.Node{{ID: "api", Type: models.NodeTypeSourceAPI, Name: "API",
			Config: map[string]interface{}{"conn_id": "store-api", "url": srv.URL, "method": "GET"}}},
		CreatedAt: now, UpdatedAt: now}
	if err := f.st.CreatePipeline(pipe); err != nil {
		t.Fatal(err)
	}
	eng := drainEngineOnCleanup(t, NewEngine(f.st))
	eng.ConnResolver = NewConnectionResolver(f.st, f.chain)
	run, err := eng.RunPipeline(pipe.ID)
	if err != nil || run.Status != models.RunStatusSuccess {
		t.Fatalf("run: %v %+v", err, run)
	}
	if gotHeader != "api-token-value-0123" {
		t.Fatalf("the target received %q, want the store's value", gotHeader)
	}
	if req := f.tokens.reqs[len(f.tokens.reqs)-1]; req.RunID != run.ID || req.NodeID != "api" {
		t.Fatalf("the token was not requested for this run and node: %+v", req)
	}
}
