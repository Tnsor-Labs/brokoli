//go:build adbc && duckdb && cgo

package engine

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/apache/arrow-adbc/go/adbc"
	"github.com/apache/arrow-adbc/go/adbc/drivermgr"
	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
)

// TestDuckDBADBCIngestStream proves the native driver manager accepts a Go
// Arrow RecordReader through ArrowArrayStream. No common.DataSet or row map is
// constructed on this path.
func TestDuckDBADBCIngestStream(t *testing.T) {
	library := os.Getenv("BROKOLI_TEST_DUCKDB_LIBRARY")
	if library == "" {
		t.Skip("set BROKOLI_TEST_DUCKDB_LIBRARY to libduckdb.so")
	}

	var driver drivermgr.Driver
	db, err := driver.NewDatabase(map[string]string{
		"driver":     library,
		"entrypoint": "duckdb_adbc_init",
	})
	if err != nil {
		t.Fatalf("open DuckDB ADBC driver: %v", err)
	}
	defer db.Close()
	conn, err := db.Open(context.Background())
	if err != nil {
		t.Fatalf("open DuckDB ADBC connection: %v", err)
	}
	defer conn.Close()

	schema := arrow.NewSchema([]arrow.Field{
		{Name: "id", Type: arrow.PrimitiveTypes.Int64, Nullable: false},
		{Name: "name", Type: arrow.BinaryTypes.String, Nullable: true},
	}, nil)
	builder := array.NewRecordBuilder(memory.DefaultAllocator, schema)
	defer builder.Release()
	builder.Field(0).(*array.Int64Builder).AppendValues([]int64{1, 2, 3}, nil)
	builder.Field(1).(*array.StringBuilder).AppendValues([]string{"one", "two", "three"}, nil)
	record := builder.NewRecordBatch()
	defer record.Release()
	reader, err := array.NewRecordReader(schema, []arrow.RecordBatch{record})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Release()

	rows, err := adbc.IngestStream(context.Background(), conn, reader, "ingested", adbc.OptionValueIngestModeCreate, adbc.IngestStreamOptions{})
	if err != nil {
		t.Fatalf("ingest Arrow stream: %v", err)
	}
	if rows != 3 {
		t.Fatalf("ingested rows = %d, want 3", rows)
	}

	statement, err := conn.NewStatement()
	if err != nil {
		t.Fatal(err)
	}
	defer statement.Close()
	if err := statement.SetSqlQuery("SELECT count(*) AS total FROM ingested"); err != nil {
		t.Fatal(err)
	}
	result, _, err := statement.ExecuteQuery(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer result.Release()
	if !result.Next() {
		t.Fatalf("count query returned no record: %v", result.Err())
	}
	got := result.RecordBatch().Column(0).(*array.Int64).Value(0)
	if got != 3 {
		t.Errorf("count = %d, want 3", got)
	}
}

// BenchmarkDuckDBADBCIngestStream measures the native ArrowArrayStream handoff
// into DuckDB. Record batches are built before timing; the measured section is
// only RecordReader export, native ingestion and DuckDB table creation.
func BenchmarkDuckDBADBCIngestStream(b *testing.B) {
	library := os.Getenv("BROKOLI_TEST_DUCKDB_LIBRARY")
	if library == "" {
		b.Skip("set BROKOLI_TEST_DUCKDB_LIBRARY to libduckdb.so")
	}

	var driver drivermgr.Driver
	db, err := driver.NewDatabase(map[string]string{"driver": library, "entrypoint": "duckdb_adbc_init"})
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Open(context.Background())
	if err != nil {
		b.Fatal(err)
	}
	defer conn.Close()

	const rows = 1_000_000
	schema := arrow.NewSchema([]arrow.Field{
		{Name: "id", Type: arrow.PrimitiveTypes.Int64, Nullable: false},
		{Name: "amount", Type: arrow.PrimitiveTypes.Float64, Nullable: false},
		{Name: "active", Type: arrow.FixedWidthTypes.Boolean, Nullable: false},
		{Name: "created_at", Type: &arrow.TimestampType{Unit: arrow.Microsecond, TimeZone: "UTC"}, Nullable: false},
		{Name: "name", Type: arrow.BinaryTypes.String, Nullable: false},
	}, nil)
	builder := array.NewRecordBuilder(memory.DefaultAllocator, schema)
	defer builder.Release()
	for i := 0; i < rows; i++ {
		builder.Field(0).(*array.Int64Builder).Append(int64(i))
		builder.Field(1).(*array.Float64Builder).Append(float64(i) / 10)
		builder.Field(2).(*array.BooleanBuilder).Append(i%2 == 0)
		builder.Field(3).(*array.TimestampBuilder).Append(arrow.Timestamp(time.UnixMicro(int64(i)).UnixMicro()))
		builder.Field(4).(*array.StringBuilder).Append("customer-" + strconv.Itoa(i))
	}
	record := builder.NewRecordBatch()
	defer record.Release()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Table cleanup and constructing a reader around already-built Arrow
		// buffers are setup, not the native ArrowArrayStream handoff.
		b.StopTimer()
		if err := execADBC(context.Background(), conn, "DROP TABLE IF EXISTS ingest_bench"); err != nil {
			b.Fatal(err)
		}
		reader, err := array.NewRecordReader(schema, []arrow.RecordBatch{record})
		if err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		started := time.Now()
		affected, err := adbc.IngestStream(context.Background(), conn, reader, "ingest_bench", adbc.OptionValueIngestModeCreate, adbc.IngestStreamOptions{})
		reader.Release()
		if err != nil {
			b.Fatal(err)
		}
		if affected != rows {
			b.Fatalf("affected rows = %d, want %d", affected, rows)
		}
		b.ReportMetric(float64(rows)/time.Since(started).Seconds(), "rows/s")
	}
}

// BenchmarkFlightSQLToDuckDB measures the complete columnar path from a real
// Flight SQL source into native DuckDB. It shares the Flight SQL benchmark's
// explicit fixture contract so a changed query cannot silently improve a
// throughput number by returning fewer rows.
func BenchmarkFlightSQLToDuckDB(b *testing.B) {
	library := os.Getenv("BROKOLI_TEST_DUCKDB_LIBRARY")
	uri := os.Getenv("BROKOLI_BENCH_FLIGHTSQL_URI")
	query := os.Getenv("BROKOLI_BENCH_FLIGHTSQL_QUERY")
	expectedText := os.Getenv("BROKOLI_BENCH_FLIGHTSQL_EXPECTED_ROWS")
	if library == "" || uri == "" || query == "" || expectedText == "" {
		b.Skip("set BROKOLI_TEST_DUCKDB_LIBRARY and the BROKOLI_BENCH_FLIGHTSQL_* variables")
	}
	expectedRows, err := strconv.ParseInt(expectedText, 10, 64)
	if err != nil || expectedRows < 1 {
		b.Fatalf("BROKOLI_BENCH_FLIGHTSQL_EXPECTED_ROWS = %q, want a positive integer", expectedText)
	}
	headers := map[string]string{}
	if token := os.Getenv("BROKOLI_BENCH_FLIGHTSQL_BEARER_TOKEN"); token != "" {
		headers["authorization"] = "Bearer " + token
	}

	if rows, err := IngestFlightSQLIntoDuckDB(context.Background(), uri, query, headers, library, "", "flight_input"); err != nil || rows != expectedRows {
		b.Fatalf("warm-up: rows=%d, want %d, err=%v", rows, expectedRows, err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	started := time.Now()
	var totalRows int64
	for i := 0; i < b.N; i++ {
		rows, err := IngestFlightSQLIntoDuckDB(context.Background(), uri, query, headers, library, "", "flight_input")
		if err != nil {
			b.Fatal(err)
		}
		if rows != expectedRows {
			b.Fatalf("rows=%d, want %d", rows, expectedRows)
		}
		totalRows += rows
	}
	elapsed := time.Since(started)
	b.StopTimer()
	b.ReportMetric(float64(totalRows)/elapsed.Seconds(), "rows/s")
}

func execADBC(ctx context.Context, conn adbc.Connection, query string) error {
	statement, err := conn.NewStatement()
	if err != nil {
		return err
	}
	defer statement.Close()
	if err := statement.SetSqlQuery(query); err != nil {
		return err
	}
	_, err = statement.ExecuteUpdate(ctx)
	return err
}
