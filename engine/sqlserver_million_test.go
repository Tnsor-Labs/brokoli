package engine

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"os"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

const (
	sqlServerMillionRows     = 1_000_000
	sqlServerMillionSrc      = "brokoli_connector_million_src"
	sqlServerMillionBenchDst = "brokoli_connector_million_bench_dst"
)

// BenchmarkSQLServerMillionArrow reports -benchmem allocations for the two
// production-sized paths. Use -benchtime=1x: repeating a million-row database
// scan automatically would benchmark the test machine's endurance, not the
// connector's behavior.
func BenchmarkSQLServerMillionArrow(b *testing.B) {
	uri := os.Getenv("BROKOLI_TEST_SQLSERVER_URL")
	if uri == "" {
		b.Skip("BROKOLI_TEST_SQLSERVER_URL not set")
	}
	db := openSQLServerForBenchmark(b, uri)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	// Closed by a cleanup, not a defer: a defer runs before the cleanups
	// below, which then drop nothing on a closed connection and leave the
	// tables to break the next run against the same server.
	b.Cleanup(func() { _ = db.Close() })
	prepareSQLServerMillionTable(b, ctx, db)
	b.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), "DROP TABLE IF EXISTS ["+sqlServerMillionBenchDst+"]")
		_, _ = db.ExecContext(context.Background(), "DROP TABLE IF EXISTS ["+sqlServerMillionSrc+"]")
	})
	if _, err := db.ExecContext(ctx, "CREATE TABLE ["+sqlServerMillionBenchDst+"] ([id] INT NOT NULL, [payload] VARCHAR(32) NOT NULL, [amount] DECIMAL(18, 4) NOT NULL)"); err != nil {
		b.Fatal(err)
	}

	query := "SELECT [id], [payload], [amount] FROM [" + sqlServerMillionSrc + "] ORDER BY [id]"
	b.Run("database_stream", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var rows int64
			started := time.Now()
			_, rows, err := StreamQueryDatabase(ctx, uri, query, 4096, func(batch *common.DataSet) error {
				return nil
			})
			if err != nil {
				b.Fatal(err)
			}
			b.ReportMetric(float64(rows)/time.Since(started).Seconds(), "rows/s")
		}
	})
	b.Run("database_to_arrow_and_back", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var encoded bytes.Buffer
			writer := newDatasetStreamWriter(&encoded, streamCodecArrow)
			var columns []string
			started := time.Now()
			_, rows, err := StreamQueryDatabase(ctx, uri, query, 4096, func(batch *common.DataSet) error {
				if writer.rows == 0 {
					var err error
					_, _, err = writer.Decide(batch)
					if err != nil {
						return err
					}
				}
				return writer.WriteBatch(batch)
			})
			if err != nil {
				b.Fatal(err)
			}
			if _, err := writer.Close(); err != nil {
				b.Fatal(err)
			}
			// Consume the IPC stream so decode allocations and CPU are measured.
			reader, err := NewArrowBatchReader(bytes.NewReader(encoded.Bytes()), columns)
			if err != nil {
				b.Fatal(err)
			}
			var decoded int64
			for {
				batch, err := reader.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					reader.Release()
					b.Fatal(err)
				}
				decoded += int64(len(batch.Rows))
			}
			reader.Release()
			if decoded != rows {
				b.Fatalf("decoded %d rows, want %d", decoded, rows)
			}
			b.ReportMetric(float64(rows)/time.Since(started).Seconds(), "rows/s")
		}
	})
	b.Run("materialized_to_sqlserver_bulk", func(b *testing.B) {
		b.ReportAllocs()
		cfg := SQLGenConfig{Dialect: "sqlserver", Table: sqlServerMillionBenchDst, Mode: ModeOverwrite, BatchSize: 5000}
		for i := 0; i < b.N; i++ {
			current, err := QueryDatabase(uri, query)
			if err != nil {
				b.Fatal(err)
			}
			sent := false
			started := time.Now()
			affected, err := copyBatchesToSQLServer(ctx, uri, cfg, current.Columns, func() (*common.DataSet, error) {
				if sent {
					return nil, io.EOF
				}
				sent = true
				return current, nil
			})
			if err != nil {
				b.Fatal(err)
			}
			if affected != sqlServerMillionRows {
				b.Fatalf("bulk write affected %d rows, want %d", affected, sqlServerMillionRows)
			}
			b.ReportMetric(float64(affected)/time.Since(started).Seconds(), "rows/s")
		}
	})
}

func openSQLServerForBenchmark(b *testing.B, uri string) *sql.DB {
	b.Helper()
	driver, dsn, err := DetectDriver(uri)
	if err != nil {
		b.Fatal(err)
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		b.Fatal(err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		b.Fatal(err)
	}
	return db
}

func prepareSQLServerMillionTable(tb testing.TB, ctx context.Context, db *sql.DB) {
	tb.Helper()
	if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS ["+sqlServerMillionSrc+"]"); err != nil {
		tb.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "CREATE TABLE ["+sqlServerMillionSrc+"] ([id] INT NOT NULL, [payload] VARCHAR(32) NOT NULL, [amount] DECIMAL(18, 4) NOT NULL)"); err != nil {
		tb.Fatal(err)
	}
	const seed = `
WITH e1(n) AS (SELECT n FROM (VALUES (1),(1),(1),(1),(1),(1),(1),(1),(1),(1)) v(n)),
e2(n) AS (SELECT 1 FROM e1 a CROSS JOIN e1 b),
e4(n) AS (SELECT 1 FROM e2 a CROSS JOIN e2 b),
e6(n) AS (SELECT 1 FROM e4 a CROSS JOIN e2 b),
numbers AS (SELECT TOP (1000000) ROW_NUMBER() OVER (ORDER BY (SELECT NULL)) AS n FROM e6)
INSERT INTO [brokoli_connector_million_src] ([id], [payload], [amount])
SELECT CONVERT(INT, n), CONCAT('row-', n), CONVERT(DECIMAL(18,4), n % 1000000 / 100.0)
FROM numbers;`
	started := time.Now()
	if _, err := db.ExecContext(ctx, seed); err != nil {
		tb.Fatalf("seed 1M rows: %v", err)
	}
	tb.Logf("seeded 1M SQL Server rows in %s", time.Since(started).Round(time.Millisecond))
}
