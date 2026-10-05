//go:build adbc && cgo

package engine

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/apache/arrow-go/v18/arrow/array"
)

// BenchmarkPostgreSQLStreamReceive compares Brokoli's pgx streaming read path
// with Apache's native PostgreSQL ADBC driver over the same query. Both paths
// consume records without retaining batches. The driver path is set explicitly
// to keep native libraries optional.
func BenchmarkPostgreSQLStreamReceive(b *testing.B) {
	uri := os.Getenv("BROKOLI_BENCH_POSTGRES_URI")
	query := os.Getenv("BROKOLI_BENCH_POSTGRES_QUERY")
	expectedText := os.Getenv("BROKOLI_BENCH_POSTGRES_EXPECTED_ROWS")
	library := os.Getenv("BROKOLI_BENCH_NATIVE_POSTGRES_LIBRARY")
	if uri == "" || query == "" || expectedText == "" || library == "" {
		b.Skip("set BROKOLI_BENCH_POSTGRES_URI, _QUERY, _EXPECTED_ROWS, and BROKOLI_BENCH_NATIVE_POSTGRES_LIBRARY")
	}
	expectedRows, err := strconv.ParseInt(expectedText, 10, 64)
	if err != nil || expectedRows < 1 {
		b.Fatalf("BROKOLI_BENCH_POSTGRES_EXPECTED_ROWS = %q, want a positive integer", expectedText)
	}

	b.Run("pgx_stream", func(b *testing.B) {
		consume := func(*common.DataSet) error { return nil }
		if _, rows, err := StreamQueryDatabase(context.Background(), uri, query, streamBatchRows, consume); err != nil || rows != expectedRows {
			b.Fatalf("warm-up: rows=%d, want %d, err=%v", rows, expectedRows, err)
		}
		b.ReportAllocs()
		b.ResetTimer()
		started := time.Now()
		var totalRows int64
		for i := 0; i < b.N; i++ {
			_, rows, err := StreamQueryDatabase(context.Background(), uri, query, streamBatchRows, consume)
			if err != nil {
				b.Fatal(err)
			}
			if rows != expectedRows {
				b.Fatalf("rows=%d, want %d", rows, expectedRows)
			}
			totalRows += rows
		}
		b.ReportMetric(float64(totalRows)/time.Since(started).Seconds(), "rows/s")
	})

	b.Run("native_adbc", func(b *testing.B) {
		consume := func(array.RecordReader) error { return nil }
		if rows, err := ConsumeNativePostgreSQL(context.Background(), library, uri, query, consume); err != nil || rows != expectedRows {
			b.Fatalf("warm-up: rows=%d, want %d, err=%v", rows, expectedRows, err)
		}
		b.ReportAllocs()
		b.ResetTimer()
		started := time.Now()
		var totalRows int64
		for i := 0; i < b.N; i++ {
			rows, err := ConsumeNativePostgreSQL(context.Background(), library, uri, query, consume)
			if err != nil {
				b.Fatal(err)
			}
			if rows != expectedRows {
				b.Fatalf("rows=%d, want %d", rows, expectedRows)
			}
			totalRows += rows
		}
		b.ReportMetric(float64(totalRows)/time.Since(started).Seconds(), "rows/s")
	})
}
