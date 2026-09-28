package api

import (
	"context"
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

func TestUnsupportedDatabaseTestNamesMissingDriver(t *testing.T) {
	for _, kind := range []models.ConnectionType{
		models.ConnTypeSnowflake,
		models.ConnTypeOracle,
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
	result := testGCS(context.Background(), map[string]interface{}{
		"bucket": "Bad Bucket",
	})
	if result["success"] != false {
		t.Fatalf("success = %v, want false for invalid GCS config", result["success"])
	}
	if result["error"] == "gcs has no driver in this build" {
		t.Fatal("GCS was routed through the generic unsupported path")
	}
}
