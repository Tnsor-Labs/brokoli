package engine

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
	"github.com/Tnsor-Labs/brokoli/pkg/secrets"
	"github.com/Tnsor-Labs/brokoli/pkg/secretstore"
	"github.com/Tnsor-Labs/brokoli/store"
)

// End to end through the real vault provider and a real OpenBao dev server:
// a run's source_api node sends a header whose value is
// secret://<vault store>/<path>#field, the target receives the value read
// from OpenBao, and the run's logs carry it only masked.
//
// Skips unless BROKOLI_TEST_VAULT_ADDR is set (docker-compose.test.yml,
// service "openbao").
func TestRunResolvesVaultSecretStoreReference(t *testing.T) {
	addr := strings.TrimRight(os.Getenv("BROKOLI_TEST_VAULT_ADDR"), "/")
	if addr == "" {
		t.Skip("BROKOLI_TEST_VAULT_ADDR is not set; start the openbao service in docker-compose.test.yml")
	}
	root := os.Getenv("BROKOLI_TEST_VAULT_TOKEN")
	if root == "" {
		root = "brokoli-test-root"
	}
	t.Cleanup(netguard.SetOutboundForTesting(netguard.Policy{AllowLoopback: true}))

	b := make([]byte, 4)
	_, _ = rand.Read(b)
	path := "brokoli-engine-" + hex.EncodeToString(b) + "/api"
	const value = "vault-api-token-0123456789"
	body, _ := json.Marshal(map[string]interface{}{"data": map[string]string{"token": value}})
	req, _ := http.NewRequest(http.MethodPost, addr+"/v1/secret/data/"+path, bytes.NewReader(body))
	req.Header.Set("X-Vault-Token", root)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	rb, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("write the secret: %d %s", resp.StatusCode, rb)
	}

	// The store presents a token of its own, not the root token: a fault
	// that revokes what a store presents must not take the root token with
	// it.
	req, _ = http.NewRequest(http.MethodPost, addr+"/v1/auth/token/create", strings.NewReader(`{"policies":["root"],"ttl":"10m"}`))
	req.Header.Set("X-Vault-Token", root)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var created struct {
		Auth struct {
			ClientToken string `json:"client_token"`
		} `json:"auth"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if created.Auth.ClientToken == "" {
		t.Fatalf("create a token: status %d", resp.StatusCode)
	}

	st, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	key := testKey(9)
	enc := secrets.NewEncryptedResolver(key)
	chain := secrets.NewChain(enc, enc)
	chain.Register(NewSecretStoreResolver(st, secretstore.NewRegistry(secretstore.Builtin()...), chain))

	now := time.Now().UTC()
	sealedRoot, _ := key.Encrypt(created.Auth.ClientToken)
	if err := st.CreateSecretStore(&models.SecretStore{ID: "st-vault", Name: "vault-dev", WorkspaceID: "ws-v", Provider: "vault",
		Settings: map[string]string{"address": addr}, AuthMethod: "token", CredentialRef: "encrypted://" + sealedRoot,
		CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}

	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Api-Token")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":1}]`))
	}))
	t.Cleanup(srv.Close)
	extra, _ := json.Marshal(map[string]interface{}{"headers": map[string]string{"X-Api-Token": "secret://vault-dev/" + path + "#token"}})
	sealed, _ := key.Encrypt(string(extra))
	if err := st.CreateConnection(&models.Connection{ID: "c-vault", ConnID: "vault-api", Type: models.ConnTypeHTTP, Host: "unused",
		WorkspaceID: "ws-v", Extra: sealed, ExtraRef: "encrypted://" + sealed, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	pipe := &models.Pipeline{ID: "vault-run", Name: "vault-run", Enabled: true, WorkspaceID: "ws-v",
		Nodes: []models.Node{{ID: "api", Type: models.NodeTypeSourceAPI, Name: "API",
			Config: map[string]interface{}{"conn_id": "vault-api", "url": srv.URL, "method": "GET"}}},
		CreatedAt: now, UpdatedAt: now}
	if err := st.CreatePipeline(pipe); err != nil {
		t.Fatal(err)
	}

	eng := drainEngineOnCleanup(t, NewEngine(st))
	eng.ConnResolver = NewConnectionResolver(st, chain)
	run, err := eng.RunPipeline(pipe.ID)
	if err != nil || run.Status != models.RunStatusSuccess {
		t.Fatalf("run: %v %+v", err, run)
	}
	if gotHeader != value {
		t.Fatalf("the target received %q, want the value stored in OpenBao", gotHeader)
	}
	logs, err := st.GetLogs(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range logs {
		if strings.Contains(l.Message, value) {
			t.Fatalf("a run log carries the secret: %s", l.Message)
		}
	}

	// A field the secret does not have fails the run, naming the reference.
	extra, _ = json.Marshal(map[string]interface{}{"headers": map[string]string{"X-Api-Token": "secret://vault-dev/" + path + "#nope"}})
	sealed, _ = key.Encrypt(string(extra))
	conn, err := st.GetConnection("vault-api")
	if err != nil {
		t.Fatal(err)
	}
	conn.Extra, conn.ExtraRef = sealed, "encrypted://"+sealed
	if err := st.UpdateConnection(conn); err != nil {
		t.Fatal(err)
	}
	gotHeader = ""
	run, _ = eng.RunPipeline(pipe.ID)
	if run == nil || run.Status != models.RunStatusFailed || gotHeader != "" {
		t.Fatalf("a missing field: run %+v, target got %q", run, gotHeader)
	}
	logs, _ = st.GetLogs(run.ID)
	var named bool
	for _, l := range logs {
		named = named || strings.Contains(l.Message, `no field "nope"`)
	}
	if !named {
		t.Fatalf("the failed run's logs do not say which field was missing: %+v", logs)
	}
}
