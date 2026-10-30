package engine

import (
	"fmt"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

func filterBenchRows(n int) []common.DataRow {
	rows := make([]common.DataRow, n)
	for i := range rows {
		rows[i] = common.DataRow{"id": int64(i), "status": fmt.Sprintf("s%d", i%4), "amount": float64(i%1000) / 10}
	}
	return rows
}

func BenchmarkFilterRows(b *testing.B) {
	for _, cond := range []string{"amount >= 50", "status = s1", "status in [s1, s3]"} {
		rows := filterBenchRows(100_000)
		b.Run(cond, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				ds := &common.DataSet{Columns: []string{"id", "status", "amount"}, Rows: rows}
				if err := filterRows(TransformRule{Type: "filter_rows", Condition: cond}, ds); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
