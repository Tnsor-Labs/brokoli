//go:build adbc && cgo

package engine

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow/array"
)

// BenchmarkNativeFlightSQLReceiveOnly compares Apache's native Flight SQL
// driver with BenchmarkFlightSQLReceiveOnly using the same server and query.
// Set BROKOLI_BENCH_NATIVE_FLIGHTSQL_LIBRARY to the extracted official driver.
func BenchmarkNativeFlightSQLReceiveOnly(b *testing.B) {
	library := os.Getenv("BROKOLI_BENCH_NATIVE_FLIGHTSQL_LIBRARY")
	uri := os.Getenv("BROKOLI_BENCH_FLIGHTSQL_URI")
	query := os.Getenv("BROKOLI_BENCH_FLIGHTSQL_QUERY")
	expectedText := os.Getenv("BROKOLI_BENCH_FLIGHTSQL_EXPECTED_ROWS")
	if library == "" || uri == "" || query == "" || expectedText == "" {
		b.Skip("set BROKOLI_BENCH_NATIVE_FLIGHTSQL_LIBRARY and BROKOLI_BENCH_FLIGHTSQL_URI, _QUERY, and _EXPECTED_ROWS")
	}
	expectedRows, err := strconv.ParseInt(expectedText, 10, 64)
	if err != nil || expectedRows < 1 {
		b.Fatalf("BROKOLI_BENCH_FLIGHTSQL_EXPECTED_ROWS = %q, want a positive integer", expectedText)
	}
	headers := map[string]string{}
	if token := os.Getenv("BROKOLI_BENCH_FLIGHTSQL_BEARER_TOKEN"); token != "" {
		headers["authorization"] = "Bearer " + token
	}
	consume := func(array.RecordReader) error { return nil }
	if rows, err := ConsumeNativeFlightSQL(context.Background(), library, uri, query, headers, consume); err != nil || rows != expectedRows {
		b.Fatalf("warm-up: rows=%d, want %d, err=%v", rows, expectedRows, err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	started := time.Now()
	var totalRows int64
	for i := 0; i < b.N; i++ {
		rows, err := ConsumeNativeFlightSQL(context.Background(), library, uri, query, headers, consume)
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
