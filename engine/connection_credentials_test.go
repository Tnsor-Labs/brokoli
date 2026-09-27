package engine

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/crypto"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/secrets"
	"github.com/Tnsor-Labs/brokoli/store"
)

// #751: a credential reference that cannot be resolved used to be logged
// to the server and skipped, so the node ran with an empty password (or,
// for an encrypted:// value that failed to decrypt, with the ciphertext as
// the password), and the author saw the target's authentication error with
// nothing in the run log to say why.

const (
	missingEnvVar   = "BROKOLI_TEST_751_MISSING_PASSWORD"
	missingExtraVar = "BROKOLI_TEST_751_MISSING_EXTRA"
)

func testKey(b byte) *crypto.Config {
	k := make([]byte, 32)
	for i := range k {
		k[i] = b
	}
	return &crypto.Config{Key: k}
}

// credentialStore holds connections whose references fail, each in a
// different way, plus one that resolves.
func credentialStore(t *testing.T) (*store.SQLiteStore, string) {
	t.Helper()
	st, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "creds.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// Encrypted under a key the resolver below does not hold, the state a
	// deployment is in after its key changed.
	ciphertext, err := testKey(1).Encrypt("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	good, err := testKey(2).Encrypt("s3cret")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []models.Connection{
		{ID: "1", ConnID: "env-password", Type: models.ConnTypePostgres, Host: "db.invalid", Port: 5432, Schema: "db", Login: "u",
			PasswordRef: "env://" + missingEnvVar},
		{ID: "2", ConnID: "wrong-key", Type: models.ConnTypePostgres, Host: "db.invalid", Port: 5432, Schema: "db", Login: "u",
			Password: ciphertext, PasswordRef: "encrypted://" + ciphertext},
		{ID: "3", ConnID: "env-extra", Type: models.ConnTypeHTTP, Host: "api.invalid", Login: "u",
			ExtraRef: "env://" + missingExtraVar},
		{ID: "4", ConnID: "resolves", Type: models.ConnTypePostgres, Host: "db.invalid", Port: 5432, Schema: "db", Login: "u",
			Password: good, PasswordRef: "encrypted://" + good},
	} {
		c := c
		c.CreatedAt, c.UpdatedAt = time.Now().UTC(), time.Now().UTC()
		if err := st.CreateConnection(&c); err != nil {
			t.Fatal(err)
		}
	}
	return st, ciphertext
}

// The chain a server builds: env references allowed by name, encrypted
// values under the server's key (here key 2).
func credentialResolver(t *testing.T, st store.Store) *ConnectionResolver {
	t.Helper()
	t.Setenv("BROKOLI_SECRET_ENV_ALLOW", missingEnvVar+","+missingExtraVar)
	enc := secrets.NewEncryptedResolver(testKey(2))
	return NewConnectionResolver(st, secrets.NewChain(enc, secrets.EnvResolver{}, enc))
}

func TestAnUnresolvableCredentialIsAnError(t *testing.T) {
	st, ciphertext := credentialStore(t)
	cr := credentialResolver(t, st)

	for _, tc := range []struct {
		connID   string
		nodeType models.NodeType
		want     []string
	}{
		{"env-password", models.NodeTypeSourceDB, []string{`connection "env-password"`, "password", "env://" + missingEnvVar, "not set"}},
		{"wrong-key", models.NodeTypeSourceDB, []string{`connection "wrong-key"`, "password", "its stored encrypted value", "decrypt"}},
		{"env-extra", models.NodeTypeSourceAPI, []string{`connection "env-extra"`, "extra settings", "env://" + missingExtraVar}},
	} {
		t.Run(tc.connID, func(t *testing.T) {
			cfg := map[string]interface{}{"conn_id": tc.connID}
			resolved, _, err := cr.ResolveWithWarningsIn(cfg, tc.nodeType, "")
			if err == nil {
				t.Fatalf("an unresolvable credential resolved without error: %v", resolved)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not name %q", err, w)
				}
			}
			if strings.Contains(err.Error(), ciphertext) {
				t.Errorf("the error quotes the ciphertext: %q", err)
			}
			for _, key := range []string{"uri", "auth_password", "headers"} {
				if _, ok := resolved[key]; ok {
					t.Errorf("credentials were injected despite the error: %s = %v", key, resolved[key])
				}
			}

			if _, err := cr.ResolveConnectionIn(tc.connID, ""); err == nil {
				t.Error("ResolveConnectionIn returned a connection with an unresolved credential")
			}
			if _, err := cr.ResolveConnectionByID(tc.connID); err == nil {
				t.Error("ResolveConnectionByID returned a connection with an unresolved credential")
			}
			if _, err := cr.ResolveIn(cfg, tc.nodeType, ""); err == nil {
				t.Error("ResolveIn resolved an unresolvable credential without error")
			}
		})
	}

	// And a reference that does resolve still does.
	cfg, _, err := cr.ResolveWithWarningsIn(map[string]interface{}{"conn_id": "resolves"}, models.NodeTypeSourceDB, "")
	if err != nil {
		t.Fatal(err)
	}
	if uri, _ := cfg["uri"].(string); !strings.Contains(uri, "s3cret") {
		t.Fatalf("a resolvable reference did not reach the URI: %q", uri)
	}
}

// With no secrets chain the connection is used as it is: that is how a
// worker receives connections its control plane already resolved, with
// the plaintext in Password and the original reference still set.
func TestWithoutASecretsChainAConnectionIsUsedAsItIs(t *testing.T) {
	st, _ := credentialStore(t)
	c, err := st.GetConnection("wrong-key")
	if err != nil {
		t.Fatal(err)
	}
	c.Password = "already-resolved"
	cr := NewConnectionResolver(&oneConnStore{conn: c}, nil)

	cfg, _, err := cr.ResolveWithWarningsIn(map[string]interface{}{"conn_id": "wrong-key"}, models.NodeTypeSourceDB, "")
	if err != nil {
		t.Fatalf("a resolver with no secrets chain refused an already-resolved connection: %v", err)
	}
	if uri, _ := cfg["uri"].(string); !strings.Contains(uri, "already-resolved") {
		t.Fatalf("uri = %q, want the already-resolved password", uri)
	}
}

// A password with no reference is the legacy shape. Failing to decrypt it
// is how plaintext from before references is recognised, so it is kept.
func TestALegacyPlaintextPasswordIsKept(t *testing.T) {
	cr := credentialResolver(t, &oneConnStore{conn: &models.Connection{
		ConnID: "legacy", Type: models.ConnTypePostgres, Host: "db.invalid", Port: 5432, Schema: "db", Login: "u",
		Password: "plain-old-password",
	}})
	cfg, _, err := cr.ResolveWithWarningsIn(map[string]interface{}{"conn_id": "legacy"}, models.NodeTypeSourceDB, "")
	if err != nil {
		t.Fatalf("a legacy plaintext password was refused: %v", err)
	}
	if uri, _ := cfg["uri"].(string); !strings.Contains(uri, "plain-old-password") {
		t.Fatalf("uri = %q, want the legacy password", uri)
	}
}

// End to end: the run fails at the node, and both the node's log and the
// run's error say which reference could not be resolved. The node never
// reaches its target.
func TestARunFailsOnAnUnresolvableCredentialAndSaysWhy(t *testing.T) {
	for _, tc := range []struct {
		name string
		node models.Node
		want string
	}{
		{"source_db", models.Node{ID: "q", Name: "q", Type: models.NodeTypeSourceDB,
			Config: map[string]interface{}{"conn_id": "env-password", "query": "select 1"}}, "env://" + missingEnvVar},
		{"migrate", models.Node{ID: "m", Name: "m", Type: models.NodeTypeMigrate,
			Config: map[string]interface{}{"source_conn_id": "wrong-key", "dest_conn_id": "resolves",
				"source_query": "select 1", "dest_table": "t"}}, "source: connection \"wrong-key\""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, _ := credentialStore(t)
			eng := drainEngineOnCleanup(t, NewEngine(st))
			eng.ConnResolver = credentialResolver(t, st)
			p := &models.Pipeline{ID: "p-" + tc.name, Name: tc.name, Enabled: true, Nodes: []models.Node{tc.node}}
			if err := st.CreatePipeline(p); err != nil {
				t.Fatal(err)
			}
			run, _ := eng.RunPipeline(p.ID)
			if run == nil {
				t.Fatal("no run")
			}
			logs, _ := st.GetLogs(run.ID)
			var all strings.Builder
			for _, l := range logs {
				all.WriteString(string(l.Level) + " " + l.Message + "\n")
			}
			if run.Status != models.RunStatusFailed {
				t.Fatalf("status %s, want failed:\n%s", run.Status, all.String())
			}
			if !strings.Contains(run.Error, tc.want) {
				t.Errorf("run error %q does not name %q", run.Error, tc.want)
			}
			if !strings.Contains(all.String(), tc.want) {
				t.Errorf("the node log does not name %q:\n%s", tc.want, all.String())
			}
			if strings.Contains(all.String(), "db.invalid") {
				t.Errorf("the node went on to reach its target:\n%s", all.String())
			}
		})
	}
}
