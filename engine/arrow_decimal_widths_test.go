package engine

import (
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/decimal"
	"github.com/apache/arrow-go/v18/arrow/decimal256"
	"github.com/apache/arrow-go/v18/arrow/memory"
)

// Every Arrow decimal width decodes to the same exact string. A native
// driver chooses the width from the column's precision (the MySQL driver
// returns DECIMAL(10,2) as decimal64), and only decimal128 used to be read:
// a live MySQL run read its rows and then failed on "decimal64(10, 2) is
// not supported".
func TestArrowDecimalWidthsDecodeExactly(t *testing.T) {
	pool := memory.NewGoAllocator()
	want := []string{"12.50", "-7.25", "0.05"}

	b32 := array.NewDecimal32Builder(pool, &arrow.Decimal32Type{Precision: 9, Scale: 2})
	b64 := array.NewDecimal64Builder(pool, &arrow.Decimal64Type{Precision: 10, Scale: 2})
	b256 := array.NewDecimal256Builder(pool, &arrow.Decimal256Type{Precision: 40, Scale: 2})
	for _, v := range []int64{1250, -725, 5} {
		b32.Append(decimal.Decimal32(v))
		b64.Append(decimal.Decimal64(v))
		b256.Append(decimal256.FromI64(v))
	}
	for name, arr := range map[string]arrow.Array{"decimal32": b32.NewArray(), "decimal64": b64.NewArray(), "decimal256": b256.NewArray()} {
		for i, w := range want {
			got, err := arrowValue(arr, i)
			if err != nil {
				t.Fatalf("%s[%d]: %v", name, i, err)
			}
			if got != w {
				t.Errorf("%s[%d] = %v, want %s", name, i, got, w)
			}
		}
		arr.Release()
	}
}
