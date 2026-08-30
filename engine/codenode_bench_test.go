//go:build unix

package engine

import (
	"fmt"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// The expansion-shaped benchmark ADR-029 was scoped against: many
// small executions, where spawn-per-invocation pays interpreter boot
// every time and the pool pays it once. Run with:
//
//	go test ./engine/ -bench BenchmarkCodeNodeExpansionShape -run XXX -benchtime 20x
func BenchmarkCodeNodeExpansionShape(b *testing.B) {
	script := `output_data = {"columns": columns, "rows": [{"a": r["a"] * 2} for r in rows]}`
	input := &common.DataSet{Columns: []string{"a"}, Rows: []common.DataRow{{"a": float64(21)}}}
	for _, mode := range []string{"0", "1"} {
		name := map[string]string{"0": "spawn-per-invocation", "1": "warm-pool"}[mode]
		b.Run(name, func(b *testing.B) {
			b.Setenv("BROKOLI_CODE_POOL", mode)
			for i := 0; i < b.N; i++ {
				_, _, err := ExecuteCodeNode(script, input, map[string]interface{}{}, nil, 30)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
	_ = fmt.Sprintf
}
