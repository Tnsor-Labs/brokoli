package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/drivers"
	"github.com/Tnsor-Labs/brokoli/pkg/secrets"
)

type countingCredentialResolver struct{ calls int }

func (r *countingCredentialResolver) Scheme() string { return "count" }
func (r *countingCredentialResolver) Resolve(context.Context, secrets.Scope, string) (string, error) {
	r.calls++
	return "secret", nil
}

func installedFlightSQLManager(t *testing.T) (*drivers.Manager, *drivers.DriverIdentity) {
	t.Helper()
	root := t.TempDir()
	name, library := "adbc-flightsql", []byte("native Flight SQL driver")
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(dir, "lib"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lib", "driver.so"), library, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(library)
	// ArchiveSHA256 is metadata for an already installed driver, but the strict
	// manifest loader still requires a syntactically valid digest.
	manifest := drivers.Manifest{Name: name, Version: "1.0.0", OS: runtime.GOOS, Arch: runtime.GOARCH,
		Library: "lib/driver.so", Entrypoint: "AdbcDriverFlightSQLInit", LibrarySHA256: hex.EncodeToString(digest[:]), ArchiveSHA256: "0000000000000000000000000000000000000000000000000000000000000000"}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := drivers.NewManager(root)
	if err != nil {
		t.Fatal(err)
	}
	return manager, &drivers.DriverIdentity{Name: name, Version: manifest.Version, LibrarySHA256: manifest.LibrarySHA256}
}

func TestFlightSQLResolverValidatesIdentityBeforeCredentials(t *testing.T) {
	manager, identity := installedFlightSQLManager(t)
	credentials := &countingCredentialResolver{}
	conn := &models.Connection{ConnID: "flight", Type: models.ConnTypeFlightSQL, DriverIdentity: identity, PasswordRef: "count://password"}
	resolver := NewConnectionResolver(&oneConnStore{conn: conn}, secrets.NewChain(nil, credentials))
	resolver.SetDriverManager(manager)

	_, err := resolver.Resolve(map[string]interface{}{"conn_id": "flight"}, models.NodeTypeSourceDB)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if credentials.calls != 1 {
		t.Fatalf("credential resolver calls = %d, want 1", credentials.calls)
	}
}

func TestFlightSQLResolverRejectsMissingOrMismatchedIdentity(t *testing.T) {
	manager, identity := installedFlightSQLManager(t)
	for name, driverIdentity := range map[string]*drivers.DriverIdentity{
		"missing":  nil,
		"mismatch": {Name: identity.Name, Version: "9.9.9", LibrarySHA256: identity.LibrarySHA256},
	} {
		t.Run(name, func(t *testing.T) {
			credentials := &countingCredentialResolver{}
			resolver := NewConnectionResolver(&oneConnStore{conn: &models.Connection{ConnID: "flight", Type: models.ConnTypeFlightSQL, DriverIdentity: driverIdentity, PasswordRef: "count://password"}}, secrets.NewChain(nil, credentials))
			resolver.SetDriverManager(manager)
			_, err := resolver.Resolve(map[string]interface{}{"conn_id": "flight"}, models.NodeTypeSourceDB)
			if err == nil || errors.Is(err, ErrNativeWorkerUnavailable) {
				t.Fatalf("Resolve() error = %v, want identity rejection", err)
			}
			if credentials.calls != 0 {
				t.Fatalf("credential resolver calls = %d, want 0", credentials.calls)
			}
		})
	}
}

func TestDatabaseResolverUsesNativeADBCOnlyWhenPinned(t *testing.T) {
	manager, identity := installedFlightSQLManager(t)
	for _, tc := range []struct {
		name string
		conn *models.Connection
	}{
		{"postgres", &models.Connection{ConnID: "postgres", Type: models.ConnTypePostgres, Host: "db.example.com", Schema: "analytics", DriverIdentity: identity, Extra: `{"adbc_options":{"adbc.postgresql.read_only":"true"}}`}},
		{"sqlite", &models.Connection{ConnID: "sqlite", Type: models.ConnTypeSQLite, Host: "/data/app.db", DriverIdentity: identity}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolver := NewConnectionResolver(&oneConnStore{conn: tc.conn}, nil)
			resolver.SetDriverManager(manager)
			resolved, err := resolver.Resolve(map[string]interface{}{"conn_id": tc.conn.ConnID}, models.NodeTypeSourceDB)
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if got, _ := resolved["native_adbc_entrypoint"].(string); got != "AdbcDriverFlightSQLInit" {
				t.Fatalf("entrypoint = %q", got)
			}
			if tc.conn.Type == models.ConnTypePostgres {
				if got, _ := resolved["native_adbc_options"].(map[string]string); got["adbc.postgresql.read_only"] != "true" {
					t.Fatalf("options = %#v", got)
				}
			}

			tc.conn.DriverIdentity = nil
			resolved, err = resolver.Resolve(map[string]interface{}{"conn_id": tc.conn.ConnID}, models.NodeTypeSourceDB)
			if err != nil {
				t.Fatalf("legacy Resolve() error = %v", err)
			}
			if _, ok := resolved["native_adbc_library"]; ok {
				t.Fatalf("unpinned %s was routed to native ADBC", tc.name)
			}
			if tc.conn.Type == models.ConnTypeSQLite && resolved["uri"] != "/data/app.db" {
				t.Fatalf("unpinned SQLite URI = %q, want file path", resolved["uri"])
			}
		})
	}
}

func TestSQLiteNativeADBCSourceRequiresPinnedDriverCapability(t *testing.T) {
	manager, identity := installedFlightSQLManager(t)
	resolver := NewConnectionResolver(&oneConnStore{conn: &models.Connection{ConnID: "sqlite", Type: models.ConnTypeSQLite, Host: "/data/app.db", DriverIdentity: identity}}, nil)
	resolver.SetDriverManager(manager)

	capabilities, err := resolver.PipelineRequiredCapabilities(&models.Pipeline{Nodes: []models.Node{{Type: models.NodeTypeSourceDB, Config: map[string]interface{}{"conn_id": "sqlite"}}}})
	if err != nil {
		t.Fatalf("PipelineRequiredCapabilities() error = %v", err)
	}
	want, err := manager.RequiredCapabilities(*identity)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(capabilities, want) {
		t.Fatalf("capabilities = %v, want %v", capabilities, want)
	}
}
