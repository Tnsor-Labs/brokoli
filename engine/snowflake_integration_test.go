package engine

import (
	"os"
	"testing"
)

// TestSnowflakeLive is opt-in because CI has no shared Snowflake account.
// BROKOLI_TEST_SNOWFLAKE_URI must contain the complete secret-bearing URI for
// a disposable account/database/schema.
func TestSnowflakeLive(t *testing.T) {
	uri := os.Getenv("BROKOLI_TEST_SNOWFLAKE_URI")
	if uri == "" {
		t.Skip("set BROKOLI_TEST_SNOWFLAKE_URI to run the Snowflake integration test")
	}
	ds, err := QueryDatabase(uri, "SELECT 1 AS brokoli_smoke")
	if err != nil {
		t.Fatalf("Snowflake query: %v", err)
	}
	if len(ds.Rows) != 1 || len(ds.Columns) != 1 {
		t.Fatalf("Snowflake result = %#v, want one row and one column", ds)
	}
}
