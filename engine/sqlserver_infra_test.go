package engine

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"
)

// TestSQLServerConnection proves the registered driver can authenticate and
// execute a query against a real SQL Server. It is environment-gated so local
// runs without the test service remain fast and deterministic.
func TestSQLServerConnection(t *testing.T) {
	uri := os.Getenv("BROKOLI_TEST_SQLSERVER_URL")
	if uri == "" {
		t.Skip("BROKOLI_TEST_SQLSERVER_URL not set")
	}

	driver, dsn, err := DetectDriver(uri)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}

	var got int
	if err := db.QueryRowContext(ctx, "SELECT 1").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != 1 {
		t.Fatalf("SELECT 1 = %d, want 1", got)
	}
}
