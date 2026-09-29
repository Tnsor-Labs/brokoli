package api

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
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
