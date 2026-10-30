package engine

import (
	"io"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// benchBulkRows is a batch shaped like a decoded source: integers,
// floats, short strings (one in eight needing an escape), booleans,
// times and nulls.
func benchBulkRows(n int) (*common.DataSet, []string) {
	cols := []string{"id", "amount", "name", "note", "active", "at", "missing"}
	ts := time.Date(2026, 10, 30, 12, 34, 56, 789000000, time.UTC)
	rows := make([]common.DataRow, n)
	for i := range rows {
		note := "plain text"
		if i%8 == 0 {
			note = "line one\nline\ttwo"
		}
		rows[i] = common.DataRow{
			"id": int64(i), "amount": float64(i) * 1.25, "name": "customer",
			"note": note, "active": i%2 == 0, "at": ts, "missing": nil,
		}
	}
	return &common.DataSet{Columns: cols, Rows: rows}, cols
}

func benchBulkReader(b *testing.B, reader func([]string, func() (*common.DataSet, error)) io.Reader) {
	ds, cols := benchBulkRows(10000)
	b.ReportAllocs()
	for b.Loop() {
		done := false
		r := reader(cols, func() (*common.DataSet, error) {
			if done {
				return nil, io.EOF
			}
			done = true
			return ds, nil
		})
		if _, err := io.Copy(io.Discard, r); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCopyReader(b *testing.B)      { benchBulkReader(b, copyReader) }
func BenchmarkMySQLLoadReader(b *testing.B) { benchBulkReader(b, mysqlLoadReader) }
