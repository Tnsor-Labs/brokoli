package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/drivers"
	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
	"github.com/Tnsor-Labs/brokoli/store"
	"github.com/go-chi/chi/v5"
)

func TestMSSQLConnectionTestUsesCompiledDriver(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	result := testDBConnection(ctx, "sqlserver://sa:wrong@127.0.0.1:1?database=master&encrypt=disable")
	if result["success"] != false {
		t.Fatalf("success = %v, want false for an unreachable server", result["success"])
	}
	if result["driver"] != "sqlserver" {
		t.Fatalf("driver = %v, want sqlserver; SQL Server must use the real connection test", result["driver"])
	}
	if result["error"] == "mssql has no driver in this build" {
		t.Fatal("SQL Server was routed through the unsupported-driver path")
	}
}

// Against a real Oracle (BROKOLI_TEST_ORACLE_URL; see
// docker-compose.test.yml): the right password connects and a wrong one
// fails, through the compiled driver.
func TestOracleConnectionTestIsReal(t *testing.T) {
	uri := os.Getenv("BROKOLI_TEST_ORACLE_URL")
	if uri == "" {
		t.Skip("set BROKOLI_TEST_ORACLE_URL to run the Oracle connection test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if result := testDBConnection(ctx, uri); result["success"] != true {
		t.Fatalf("the right credentials failed: %v", result)
	}
	u, err := url.Parse(uri)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(u.User.Username(), "wrong-password")
	result := testDBConnection(ctx, u.String())
	if result["success"] != false || result["driver"] != "oracle" {
		t.Fatalf("a wrong password: %v, want a failure from the oracle driver", result)
	}
}

// Test connection routes a Databricks connection to its compiled driver, and
// a host saved with its port -- the input that made the upstream driver quote
// the token in its parse error -- fails without the token reaching the result
// or the server log.
func TestDatabricksConnectionTestUsesCompiledDriver(t *testing.T) {
	const token = "dapiSECRET0123456789abcdef"
	t.Cleanup(netguard.SetOutboundForTesting(netguard.Policy{}))
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	h := &ConnectionHandler{}
	for name, host := range map[string]string{
		"unreachable":    "127.0.0.1",
		"host with port": "workspace.cloud.databricks.com:443",
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			c := &models.Connection{Type: models.ConnTypeDatabricks, Host: host, Port: 1,
				Schema: "/sql/1.0/warehouses/wh", Password: token}
			result := h.testResolved(ctx, c, nil)
			if result["success"] != false {
				t.Fatalf("success = %v, want false", result["success"])
			}
			if result["driver"] != "brokoli-databricks" {
				t.Fatalf("result = %v, want the compiled Databricks driver", result)
			}
			if strings.Contains(fmt.Sprint(result), token) {
				t.Fatalf("the token reached the result: %v", result)
			}
		})
	}
	if !strings.Contains(logged.String(), "bare hostname") {
		t.Fatalf("the malformed host was not explained in the log: %s", logged.String())
	}
	if strings.Contains(logged.String(), token) {
		t.Fatalf("the token reached the server log: %s", logged.String())
	}
}

func TestGCSConnectionTestUsesCompiledTransport(t *testing.T) {
	h := &ConnectionHandler{}
	result := h.testGCS(context.Background(), &models.Connection{Type: models.ConnTypeGCS, Extra: `{"bucket":"Bad Bucket"}`})
	if result["success"] != false {
		t.Fatalf("success = %v, want false for invalid GCS config", result["success"])
	}
	if result["error"] == "gcs has no driver in this build" {
		t.Fatal("GCS was routed through the generic unsupported path")
	}
}

func TestFlightSQLConnectionTestReportsIdentityGateBeforeCredentials(t *testing.T) {
	s, err := store.NewSQLiteStore(t.TempDir() + "/connections.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	manager, identity := testFlightSQLManager(t)
	if err := s.CreateConnection(&models.Connection{ID: "c1", ConnID: "flight", Type: models.ConnTypeFlightSQL,
		Host: "flight.example.com", DriverIdentity: identity, PasswordRef: "unsupported://must-not-resolve", CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	h := NewConnectionHandler(s, nil)
	h.creds.SetDriverManager(manager)
	r := chi.NewRouter()
	r.Post("/connections/{connId}/test", h.Test)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/connections/flight/test", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "isolated native worker execution is not enabled") || strings.Contains(w.Body.String(), "unsupported scheme") {
		t.Fatalf("connection test response = %s, want pre-credential worker gate", w.Body.String())
	}
}

func testFlightSQLManager(t *testing.T) (*drivers.Manager, *drivers.DriverIdentity) {
	t.Helper()
	root, name, library := t.TempDir(), "adbc-flightsql", []byte("native Flight SQL driver")
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(dir, "lib"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lib", "driver.so"), library, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(library)
	manifest := drivers.Manifest{Name: name, Version: "1.0.0", OS: runtime.GOOS, Arch: runtime.GOARCH, Library: "lib/driver.so", Entrypoint: "AdbcDriverFlightSQLInit", LibrarySHA256: hex.EncodeToString(digest[:]), ArchiveSHA256: strings.Repeat("0", 64)}
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
