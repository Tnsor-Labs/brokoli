package engine

import (
	"bytes"
	"math"
	"strings"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/arrow/memory"
)

// The Arrow decoder converted every integer through float64 before
// narrowing it back, so anything past 2^53 lost precision on the way
// in: 9007199254740993 decoded as ...992, and MaxInt64 decoded as a
// float64. Same defect class as #479 on the Go task path and #492 in
// the Node harness, one layer further out.
//
// These go through decodeArrowIPCRows, the entry point a harness's
// bytes actually reach, rather than the value helper underneath it.

func arrowIPCWith(t *testing.T, field arrow.Field, append func(b array.Builder)) []byte {
	t.Helper()
	schema := arrow.NewSchema([]arrow.Field{field}, nil)
	b := array.NewRecordBuilder(memory.DefaultAllocator, schema)
	defer b.Release()
	append(b.Field(0))
	rec := b.NewRecord()
	defer rec.Release()

	var buf bytes.Buffer
	w := ipc.NewWriter(&buf, ipc.WithSchema(schema))
	if err := w.Write(rec); err != nil {
		t.Fatalf("write arrow: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close arrow: %v", err)
	}
	return buf.Bytes()
}

func TestArrowDecodeKeeps64BitIntegersExact(t *testing.T) {
	// Every one of these is a whole number that float64 cannot hold.
	want := []int64{
		9007199254740993,  // 2^53 + 1, the smallest one that breaks
		-9007199254740993, // and its negative
		math.MaxInt64,
		math.MinInt64,
		1234567890123456789,
	}
	raw := arrowIPCWith(t, arrow.Field{Name: "id", Type: arrow.PrimitiveTypes.Int64, Nullable: true},
		func(b array.Builder) { b.(*array.Int64Builder).AppendValues(want, nil) })

	rows, cols, err := decodeArrowIPCRows(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(cols) != 1 || cols[0] != "id" {
		t.Fatalf("columns = %v", cols)
	}
	if len(rows) != len(want) {
		t.Fatalf("rows = %d, want %d", len(rows), len(want))
	}
	for i, w := range want {
		got, ok := rows[i]["id"].(int64)
		if !ok {
			t.Errorf("row %d id is %T (%v), want int64", i, rows[i]["id"], rows[i]["id"])
			continue
		}
		if got != w {
			t.Errorf("row %d id = %d, want %d (off by %d)", i, got, w, got-w)
		}
	}
}

// The narrower integer widths cannot lose precision, but they must still
// arrive as int64 rather than float64, because downstream code type
// switches on these values and the NDJSON path yields int64.
func TestArrowDecodeNarrowIntegersArriveAsInt64(t *testing.T) {
	for _, tc := range []struct {
		name  string
		field arrow.Field
		fill  func(b array.Builder)
	}{
		{"int8", arrow.Field{Name: "v", Type: arrow.PrimitiveTypes.Int8, Nullable: true},
			func(b array.Builder) { b.(*array.Int8Builder).Append(7) }},
		{"int32", arrow.Field{Name: "v", Type: arrow.PrimitiveTypes.Int32, Nullable: true},
			func(b array.Builder) { b.(*array.Int32Builder).Append(-70000) }},
		{"uint32", arrow.Field{Name: "v", Type: arrow.PrimitiveTypes.Uint32, Nullable: true},
			func(b array.Builder) { b.(*array.Uint32Builder).Append(4294967295) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, _, err := decodeArrowIPCRows(bytes.NewReader(arrowIPCWith(t, tc.field, tc.fill)))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if _, ok := rows[0]["v"].(int64); !ok {
				t.Errorf("value is %T, want int64", rows[0]["v"])
			}
		})
	}
}

// uint64 is the one integer int64 cannot hold. It is refused by name
// rather than wrapped, so the caller learns what happened and can write
// the column as a string or fall back to NDJSON. Silently returning a
// negative number would be the exact defect this file is fixing.
func TestArrowDecodeRefusesUint64BeyondInt64(t *testing.T) {
	raw := arrowIPCWith(t, arrow.Field{Name: "big", Type: arrow.PrimitiveTypes.Uint64, Nullable: true},
		func(b array.Builder) { b.(*array.Uint64Builder).Append(math.MaxUint64) })

	_, _, err := decodeArrowIPCRows(bytes.NewReader(raw))
	if err == nil {
		t.Fatal("a uint64 above MaxInt64 was accepted; it cannot be represented exactly")
	}
	for _, want := range []string{"big", "18446744073709551615", "exceeds int64"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
}

// A uint64 that does fit is ordinary data and must still work.
func TestArrowDecodeAcceptsUint64WithinInt64(t *testing.T) {
	raw := arrowIPCWith(t, arrow.Field{Name: "n", Type: arrow.PrimitiveTypes.Uint64, Nullable: true},
		func(b array.Builder) { b.(*array.Uint64Builder).Append(math.MaxInt64) })

	rows, _, err := decodeArrowIPCRows(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := rows[0]["n"]; got != int64(math.MaxInt64) {
		t.Errorf("n = %v (%T), want int64 %d", got, got, int64(math.MaxInt64))
	}
}
