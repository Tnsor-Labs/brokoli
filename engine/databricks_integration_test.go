package engine

import (
	"os"
	"testing"
)

// TestDatabricksLive is opt-in because CI has no shared SQL warehouse.
// BROKOLI_TEST_DATABRICKS_URI must contain the complete secret-bearing URI for
// a disposable workspace and warehouse.
func TestDatabricksLive(t *testing.T) {
	uri := os.Getenv("BROKOLI_TEST_DATABRICKS_URI")
	if uri == "" {
		t.Skip("set BROKOLI_TEST_DATABRICKS_URI to run the Databricks integration test")
	}
	ds, err := QueryDatabase(uri, "SELECT 1 AS brokoli_smoke")
	if err != nil {
		t.Fatalf("Databricks query: %v", err)
	}
	if len(ds.Rows) != 1 || len(ds.Columns) != 1 {
		t.Fatalf("Databricks result = %#v, want one row and one column", ds)
	}
}
