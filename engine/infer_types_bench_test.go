package engine

import (
	"fmt"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

func BenchmarkInferTypes(b *testing.B) {
	const n = 100_000
	columns := []string{"id", "amount", "name", "created", "active", "id_text", "amount_text"}
	rows := make([]common.DataRow, n)
	for i := range rows {
		rows[i] = common.DataRow{
			"id": int64(i), "amount": float64(i) / 4, "name": fmt.Sprintf("customer-%06d", i),
			"created": fmt.Sprintf("2026-%02d-%02d", i%12+1, i%28+1), "active": i%2 == 0,
			"id_text": fmt.Sprint(i), "amount_text": fmt.Sprintf("%d.%02d", i, i%100),
		}
	}
	for _, col := range columns {
		b.Run(col, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				inferColumnType(col, rows)
			}
		})
	}
}
