//go:build adbc

package engine

import (
	"context"
	"io"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
)

// BenchmarkFlightSQLToArrowIPC measures a real Flight SQL server returning a
// production-sized result. It intentionally writes to io.Discard: this is the
// connector's receive-and-Arrow-encode cost, without a local disk benchmark
// hiding it. Run the same command under /usr/bin/time -v to record peak RSS.
//
// The query and expected row count are explicit so the benchmark cannot report
// a faster number after a server-side schema or fixture change:
//
//	BROKOLI_BENCH_FLIGHTSQL_URI=grpc+tcp://127.0.0.1:32010 \
//	BROKOLI_BENCH_FLIGHTSQL_BEARER_TOKEN=... \
//	BROKOLI_BENCH_FLIGHTSQL_QUERY='SELECT ...' \
//	BROKOLI_BENCH_FLIGHTSQL_EXPECTED_ROWS=6001215 \
//	go test -tags adbc ./engine -run '^$' -bench '^BenchmarkFlightSQLToArrowIPC$' -benchmem -benchtime=1x
func BenchmarkFlightSQLToArrowIPC(b *testing.B) {
	uri := os.Getenv("BROKOLI_BENCH_FLIGHTSQL_URI")
	query := os.Getenv("BROKOLI_BENCH_FLIGHTSQL_QUERY")
	expectedText := os.Getenv("BROKOLI_BENCH_FLIGHTSQL_EXPECTED_ROWS")
	if uri == "" || query == "" || expectedText == "" {
		b.Skip("set BROKOLI_BENCH_FLIGHTSQL_URI, BROKOLI_BENCH_FLIGHTSQL_QUERY, and BROKOLI_BENCH_FLIGHTSQL_EXPECTED_ROWS")
	}
	expectedRows, err := strconv.ParseInt(expectedText, 10, 64)
	if err != nil || expectedRows < 1 {
		b.Fatalf("BROKOLI_BENCH_FLIGHTSQL_EXPECTED_ROWS = %q, want a positive integer", expectedText)
	}
	headers := map[string]string{}
	if token := os.Getenv("BROKOLI_BENCH_FLIGHTSQL_BEARER_TOKEN"); token != "" {
		headers["authorization"] = "Bearer " + token
	}

	// Warm server plans, metadata and filesystem caches. Its result is checked
	// but excluded from both time and allocation accounting.
	if _, rows, err := StreamFlightSQLToArrowIPC(context.Background(), uri, query, headers, io.Discard); err != nil || rows != expectedRows {
		b.Fatalf("warm-up: rows=%d, want %d, err=%v", rows, expectedRows, err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	started := time.Now()
	var totalRows, totalBytes int64
	for i := 0; i < b.N; i++ {
		out := &arrowByteCounter{Writer: io.Discard}
		_, rows, err := StreamFlightSQLToArrowIPC(context.Background(), uri, query, headers, out)
		if err != nil {
			b.Fatal(err)
		}
		if rows != expectedRows {
			b.Fatalf("rows=%d, want %d", rows, expectedRows)
		}
		totalRows += rows
		totalBytes += out.n
	}
	elapsed := time.Since(started)
	b.StopTimer()
	b.SetBytes(totalBytes / int64(b.N))
	b.ReportMetric(float64(totalRows)/elapsed.Seconds(), "rows/s")
	b.ReportMetric(float64(totalBytes)/elapsed.Seconds(), "arrow-B/s")
}

// BenchmarkFlightSQLReceiveOnly separates Flight/ADBC receive cost from IPC
// serialization. Comparing it with BenchmarkFlightSQLToArrowIPC identifies
// the allocation and throughput cost of creating a durable Arrow artifact.
func BenchmarkFlightSQLReceiveOnly(b *testing.B) {
	uri := os.Getenv("BROKOLI_BENCH_FLIGHTSQL_URI")
	query := os.Getenv("BROKOLI_BENCH_FLIGHTSQL_QUERY")
	expectedText := os.Getenv("BROKOLI_BENCH_FLIGHTSQL_EXPECTED_ROWS")
	if uri == "" || query == "" || expectedText == "" {
		b.Skip("set BROKOLI_BENCH_FLIGHTSQL_URI, BROKOLI_BENCH_FLIGHTSQL_QUERY, and BROKOLI_BENCH_FLIGHTSQL_EXPECTED_ROWS")
	}
	expectedRows, err := strconv.ParseInt(expectedText, 10, 64)
	if err != nil || expectedRows < 1 {
		b.Fatalf("BROKOLI_BENCH_FLIGHTSQL_EXPECTED_ROWS = %q, want a positive integer", expectedText)
	}
	headers := map[string]string{}
	if token := os.Getenv("BROKOLI_BENCH_FLIGHTSQL_BEARER_TOKEN"); token != "" {
		headers["authorization"] = "Bearer " + token
	}
	consume := func(arrow.RecordBatch) error { return nil }
	if _, rows, err := ConsumeFlightSQL(context.Background(), uri, query, headers, consume); err != nil || rows != expectedRows {
		b.Fatalf("warm-up: rows=%d, want %d, err=%v", rows, expectedRows, err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	started := time.Now()
	var totalRows int64
	for i := 0; i < b.N; i++ {
		_, rows, err := ConsumeFlightSQL(context.Background(), uri, query, headers, consume)
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

type arrowByteCounter struct {
	io.Writer
	n int64
}

func (w *arrowByteCounter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	w.n += int64(n)
	return n, err
}
