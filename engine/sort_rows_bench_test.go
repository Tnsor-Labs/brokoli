package engine

import (
	"fmt"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

func BenchmarkSortRows(b *testing.B) {
	const n = 100_000
	cols := map[string]func(i int) interface{}{
		"int64":       func(i int) interface{} { return int64((i * 7919) % n) },
		"float64":     func(i int) interface{} { return float64((i*7919)%n) / 100 },
		"csv-numbers": func(i int) interface{} { return fmt.Sprintf("%d.%02d", (i*7919)%n, i%100) },
		"text":        func(i int) interface{} { return fmt.Sprintf("name-%06d", (i*7919)%n) },
	}
	for _, name := range []string{"int64", "float64", "csv-numbers", "text"} {
		gen := cols[name]
		rows := make([]common.DataRow, n)
		for i := range rows {
			rows[i] = common.DataRow{"k": gen(i)}
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				ds := &common.DataSet{Columns: []string{"k"}, Rows: rows}
				if err := sortRows(TransformRule{Type: "sort", Columns: []string{"k"}, Ascending: true}, ds); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
