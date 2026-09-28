package engine

import (
	"os"
	"testing"
)

// TestOracleLive is opt-in because CI has no shared Oracle account.
// BROKOLI_TEST_ORACLE_URI must contain the complete secret-bearing URI for a
// disposable account/database/service.
func TestOracleLive(t *testing.T) {
	uri := os.Getenv("BROKOLI_TEST_ORACLE_URI")
	if uri == "" {
		t.Skip("set BROKOLI_TEST_ORACLE_URI to run the Oracle integration test")
	}
	ds, err := QueryDatabase(uri, "SELECT 1 AS brokoli_smoke FROM dual")
	if err != nil {
		t.Fatalf("Oracle query: %v", err)
	}
	if len(ds.Rows) != 1 || len(ds.Columns) != 1 {
		t.Fatalf("Oracle result = %#v, want one row and one column", ds)
	}
}
