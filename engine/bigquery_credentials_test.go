package engine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/identity"
	"github.com/Tnsor-Labs/brokoli/store"
)

// A service-account key as the catalogue hint describes it: the whole extra
// document. The private key is a marker, not a real key.
const bigQueryTestKey = `{"type":"service_account","project_id":"acme","private_key_id":"k1",` +
	`"private_key":"-----BEGIN PRIVATE KEY-----\nMARKER-bq-private-key\n-----END PRIVATE KEY-----\n",` +
	`"client_email":"loader@acme.iam.gserviceaccount.com"}`

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

	got, err := bigQueryRunner(st, "ws-a").bigQuerySettings(map[string]interface{}{"conn_id": "warehouse"})
	if err != nil || got != bigQueryTestKey {
		t.Fatalf("own workspace: settings %q, err %v", got, err)
	}

	if _, err := bigQueryRunner(st, "ws-b").bigQuerySettings(map[string]interface{}{"conn_id": "warehouse"}); err == nil ||
		!strings.Contains(err.Error(), "not found in this pipeline's workspace") {
		t.Fatalf("another workspace's connection: err = %v", err)
	}

	if _, err := bigQueryRunner(st, "ws-a").bigQuerySettings(map[string]interface{}{"conn_id": "pg"}); err == nil ||
		!strings.Contains(err.Error(), "not bigquery") {
		t.Fatalf("a non-BigQuery connection: err = %v", err)
	}

	if got, err := bigQueryRunner(st, "ws-a").bigQuerySettings(map[string]interface{}{}); err != nil || got != "" {
		t.Fatalf("no conn_id: settings %q, err %v; want none, for the machine's identity", got, err)
	}

	r := &Runner{pipe: &models.Pipeline{WorkspaceID: "ws-a"}}
	if _, err := r.bigQuerySettings(map[string]interface{}{"conn_id": "warehouse"}); err == nil {
		t.Fatal("a runner with no resolver produced settings")
	}
}

// With no key, the backend would use the machine's own identity. Where
// ambient identity is denied it is refused by name, before any request.
func TestBigQueryRefusesTheMachineIdentityWhereAmbientIsDenied(t *testing.T) {
	t.Setenv("BROKOLI_BIGQUERY_ENDPOINT", "")
	t.Setenv(identity.AmbientEnv, "deny")
	_, err := bigQueryClient(context.Background(), "bigquery://acme/analytics", "")
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
		if _, err := bigQueryClient(context.Background(), "bigquery://acme/analytics", settings); err == nil ||
			!strings.Contains(err.Error(), "service_account") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
