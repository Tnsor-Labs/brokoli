package api

import (
	"context"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
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

func TestUnsupportedDatabaseTestNamesMissingDriver(t *testing.T) {
	for _, kind := range []models.ConnectionType{
		models.ConnTypeSnowflake,
		models.ConnTypeDatabricks,
	} {
		result := unsupportedDatabaseTest(kind)
		if result["success"] != false {
			t.Errorf("%s: success = %v, want false", kind, result["success"])
		}
		if result["error"] != string(kind)+" has no driver in this build" {
			t.Errorf("%s: error = %v", kind, result["error"])
		}
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
