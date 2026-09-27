package engine

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

func TestSQLServerBatchLimit(t *testing.T) {
	rows := make([]common.DataRow, 1001)
	for i := range rows {
		rows[i] = common.DataRow{"id": i}
	}

	sqlText, err := GenerateSQL(SQLGenConfig{
		Dialect: "sqlserver", Table: "rows", BatchSize: 5000,
	}, &common.DataSet{Columns: []string{"id"}, Rows: rows})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(sqlText, "INSERT INTO"); got != 2 {
		t.Fatalf("generated %d INSERT statements for 1,001 rows, want 2", got)
	}
}

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

// TestSQLServerSourceToSink proves the connector is usable by a pipeline, not
// only that the driver can answer SELECT 1. The migration node exercises the
// same source query and destination write paths used by source_db and sink_db.
func TestSQLServerSourceToSink(t *testing.T) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}

	const sourceTable = "brokoli_connector_src"
	const destinationTable = "brokoli_connector_dst"
	for _, statement := range []string{
		"DROP TABLE IF EXISTS [" + destinationTable + "]",
		"DROP TABLE IF EXISTS [" + sourceTable + "]",
		"CREATE TABLE [" + sourceTable + "] ([id] INT NOT NULL, [name] NVARCHAR(100) NOT NULL, [amount] DECIMAL(12, 2) NOT NULL)",
		"CREATE TABLE [" + destinationTable + "] ([id] INT NOT NULL, [name] NVARCHAR(100) NOT NULL, [amount] DECIMAL(12, 2) NOT NULL)",
		"INSERT INTO [" + sourceTable + "] ([id], [name], [amount]) VALUES (1, N'Alice', 10.25), (2, N'Björk', 20.50)",
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("setup %q: %v", statement, err)
		}
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), "DROP TABLE IF EXISTS ["+destinationTable+"]")
		_, _ = db.ExecContext(context.Background(), "DROP TABLE IF EXISTS ["+sourceTable+"]")
	})

	run := runMigratePipeline(t, uri, "SELECT id, name, amount FROM ["+sourceTable+"]", uri, destinationTable, "append", nil, "sqlserver-source-to-sink")
	if run.Status != "success" {
		t.Fatalf("migration status = %s, want success (error: %s)", run.Status, run.Error)
	}

	rows, err := db.QueryContext(ctx, "SELECT [id], [name], [amount] FROM ["+destinationTable+"] ORDER BY [id]")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	wantNames := []string{"Alice", "Björk"}
	for i, wantName := range wantNames {
		if !rows.Next() {
			t.Fatalf("destination ended after %d rows, want %d", i, len(wantNames))
		}
		var id int
		var name string
		var amount string
		if err := rows.Scan(&id, &name, &amount); err != nil {
			t.Fatal(err)
		}
		if id != i+1 || name != wantName {
			t.Fatalf("row %d = (%d, %q), want (%d, %q)", i, id, name, i+1, wantName)
		}
	}
	if rows.Next() {
		t.Fatal("destination contains more rows than the source")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

// TestSQLServerLoadAndFailureEdges exercises cases that a connectivity smoke
// test cannot catch: a 10,000-row migration, overwrite removing stale rows,
// NULL and Unicode values, and an empty result set retaining its columns.
func TestSQLServerLoadAndFailureEdges(t *testing.T) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}

	const sourceTable = "brokoli_connector_load_src"
	const destinationTable = "brokoli_connector_load_dst"
	for _, statement := range []string{
		"DROP TABLE IF EXISTS [" + destinationTable + "]",
		"DROP TABLE IF EXISTS [" + sourceTable + "]",
		"CREATE TABLE [" + sourceTable + "] ([id] INT NOT NULL, [payload] NVARCHAR(200) NOT NULL, [amount] DECIMAL(18, 4) NOT NULL, [optional] NVARCHAR(200) NULL)",
		"CREATE TABLE [" + destinationTable + "] ([id] INT NOT NULL, [payload] NVARCHAR(200) NOT NULL, [amount] DECIMAL(18, 4) NOT NULL, [optional] NVARCHAR(200) NULL)",
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("setup %q: %v", statement, err)
		}
	}
	seedSQLServerRows(t, ctx, db, sourceTable, 10000)
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), "DROP TABLE IF EXISTS ["+destinationTable+"]")
		_, _ = db.ExecContext(context.Background(), "DROP TABLE IF EXISTS ["+sourceTable+"]")
	})

	started := time.Now()
	run := runMigratePipeline(t, uri,
		"SELECT id, payload, amount, optional FROM ["+sourceTable+"]",
		uri, destinationTable, "append", nil, "sqlserver-load-append")
	if run.Status != "success" {
		t.Fatalf("10,000-row append status = %s, want success (error: %s)", run.Status, run.Error)
	}
	t.Logf("10,000-row append completed in %s", time.Since(started).Round(time.Millisecond))

	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM ["+destinationTable+"]").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 10000 {
		t.Fatalf("destination count = %d, want 10,000", count)
	}
	var payload, optional string
	var optionalValue sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT [payload], [optional] FROM ["+destinationTable+"] WHERE [id] = 7777").Scan(&payload, &optionalValue); err != nil {
		t.Fatal(err)
	}
	if payload != "Björk / 7777 'quoted' \\ slash" || optionalValue.Valid {
		t.Fatalf("edge row = (%q, %q), want Unicode/quotes/backslash and NULL", payload, optional)
	}

	if _, err := db.ExecContext(ctx, "INSERT INTO ["+destinationTable+"] ([id], [payload], [amount], [optional]) VALUES (99999, N'ghost', 1, NULL)"); err != nil {
		t.Fatal(err)
	}
	overwrite := runMigratePipeline(t, uri,
		"SELECT id, payload, amount, optional FROM ["+sourceTable+"]",
		uri, destinationTable, "overwrite", nil, "sqlserver-load-overwrite")
	if overwrite.Status != "success" {
		t.Fatalf("10,000-row overwrite status = %s, want success (error: %s)", overwrite.Status, overwrite.Error)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM ["+destinationTable+"] WHERE [id] = 99999").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("overwrite retained a stale destination row")
	}

	empty, err := QueryDatabase(uri, "SELECT id, payload, amount, optional FROM ["+sourceTable+"] WHERE 1 = 0")
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Rows) != 0 || len(empty.Columns) != 4 {
		t.Fatalf("empty result = %d rows and %d columns, want 0 rows and 4 columns", len(empty.Rows), len(empty.Columns))
	}
}

// BenchmarkSQLServerReadGenerateWrite measures the real connector in the
// three places where load tends to fail: fetching rows, rendering SQL, and
// executing the generated write. Run with BROKOLI_TEST_SQLSERVER_URL set and
// -benchtime=1x for a bounded smoke benchmark in CI or locally.
func BenchmarkSQLServerReadGenerateWrite(b *testing.B) {
	uri := os.Getenv("BROKOLI_TEST_SQLSERVER_URL")
	if uri == "" {
		b.Skip("BROKOLI_TEST_SQLSERVER_URL not set")
	}

	driver, dsn, err := DetectDriver(uri)
	if err != nil {
		b.Fatal(err)
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		b.Fatal(err)
	}

	const sourceTable = "brokoli_connector_bench_src"
	const destinationTable = "brokoli_connector_bench_dst"
	for _, statement := range []string{
		"DROP TABLE IF EXISTS [" + destinationTable + "]",
		"DROP TABLE IF EXISTS [" + sourceTable + "]",
		"CREATE TABLE [" + sourceTable + "] ([id] INT NOT NULL, [payload] NVARCHAR(200) NOT NULL, [amount] DECIMAL(18, 4) NOT NULL, [optional] NVARCHAR(200) NULL)",
		"CREATE TABLE [" + destinationTable + "] ([id] INT NOT NULL, [payload] NVARCHAR(200) NOT NULL, [amount] DECIMAL(18, 4) NOT NULL, [optional] NVARCHAR(200) NULL)",
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			b.Fatal(err)
		}
	}
	seedSQLServerRows(b, ctx, db, sourceTable, 5000)
	b.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), "DROP TABLE IF EXISTS ["+destinationTable+"]")
		_, _ = db.ExecContext(context.Background(), "DROP TABLE IF EXISTS ["+sourceTable+"]")
	})

	query := "SELECT id, payload, amount, optional FROM [" + sourceTable + "]"
	var sample *common.DataSet
	// Query once outside the timed sections to make the render-only benchmark
	// use the exact same shape as the read and end-to-end benchmarks.
	sample, err = QueryDatabase(uri, query)
	if err != nil {
		b.Fatal(err)
	}
	cfg := SQLGenConfig{Dialect: "sqlserver", Table: destinationTable, Mode: ModeOverwrite, BatchSize: 5000}

	b.Run("read", func(b *testing.B) {
		b.ReportMetric(5000, "rows/op")
		for i := 0; i < b.N; i++ {
			if _, err := QueryDatabase(uri, query); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("generate", func(b *testing.B) {
		b.ReportMetric(5000, "rows/op")
		for i := 0; i < b.N; i++ {
			if _, err := GenerateSQL(cfg, sample); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("end_to_end", func(b *testing.B) {
		b.ReportMetric(5000, "rows/op")
		for i := 0; i < b.N; i++ {
			current, err := QueryDatabase(uri, query)
			if err != nil {
				b.Fatal(err)
			}
			sqlText, err := GenerateSQL(cfg, current)
			if err != nil {
				b.Fatal(err)
			}
			if _, err := ExecuteSQL(uri, sqlText); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func seedSQLServerRows(tb testing.TB, ctx context.Context, db *sql.DB, table string, count int) {
	tb.Helper()
	for start := 1; start <= count; start += 500 {
		end := start + 500
		if end > count+1 {
			end = count + 1
		}
		var sqlText strings.Builder
		fmt.Fprintf(&sqlText, "INSERT INTO [%s] ([id], [payload], [amount], [optional]) VALUES ", table)
		for id := start; id < end; id++ {
			if id > start {
				sqlText.WriteString(", ")
			}
			payload := fmt.Sprintf("row-%d", id)
			if id == 7777 {
				payload = "Björk / 7777 'quoted' \\ slash"
			}
			optional := "NULL"
			if id%2 == 0 {
				optional = sqlServerStringLiteral(fmt.Sprintf("optional-%d", id))
			}
			fmt.Fprintf(&sqlText, "(%d, N%s, %d.%04d, %s)", id, sqlServerStringLiteral(payload), id%1000, id%10000, optional)
		}
		if _, err := db.ExecContext(ctx, sqlText.String()); err != nil {
			tb.Fatalf("seed rows %d-%d: %v", start, end-1, err)
		}
	}
}

func sqlServerStringLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
