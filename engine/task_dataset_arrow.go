package engine

// Arrow IPC dataset decoding (ADR-033 section 8's "preferred optional
// dataset transport when both sides advertise it").
//
// The governing constraint is section 8's own: "Transport choice is
// physical-plan metadata and does not change the logical dataset
// contract." A dataset read over Arrow must therefore produce exactly
// the DataSet the same data produces over NDJSON -- same column names,
// same Go value types -- or the codec would quietly change semantics
// downstream, and every consumer would have to know which transport
// happened to be chosen. That equivalence is what
// TestArrowAndNDJSONProduceIdenticalDataSets pins down.
//
// Concretely that means numbers arrive as float64 and integers as
// whole float64s, because that is what encoding/json produces for the
// NDJSON path. Arrow's richer type system is deliberately narrowed to
// the JSON value space on the way out rather than preserved: preserving
// it would be the thing that changes the contract.

import (
	"bytes"
	"fmt"
	"io"
	"math/big"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// CodecArrowIPC is ADR-033 section 8's columnar transport. Optional and
// negotiated: the engine advertises that it can read it, and a harness
// uses it only when its own Arrow library is actually available (see
// the adapters' invocation descriptors). NDJSON stays the baseline that
// always works.
const CodecArrowIPC = "arrow-ipc/v1"

// decodeArrowIPCRows reads an Arrow IPC stream into the same row shape
// decodeNDJSONRows produces.
//
// The row cap is enforced across record batches, not per batch: a
// producer could otherwise split an unbounded dataset into many small
// batches and slip past a per-batch check.
func decodeArrowIPCRows(r io.Reader) ([]common.DataRow, []string, error) {
	rdr, err := ipc.NewReader(r, ipc.WithAllocator(memory.DefaultAllocator))
	if err != nil {
		return nil, nil, fmt.Errorf("not a readable arrow ipc stream: %w", err)
	}
	defer rdr.Release()

	var (
		rows    []common.DataRow
		columns []string
	)
	for _, f := range rdr.Schema().Fields() {
		columns = append(columns, f.Name)
	}

	for rdr.Next() {
		rec := rdr.Record()
		if int64(len(rows))+rec.NumRows() > int64(maxTaskDatasetRows) {
			return nil, nil, fmt.Errorf("more than %d rows, over this server's cap", maxTaskDatasetRows)
		}
		for i := int64(0); i < rec.NumRows(); i++ {
			row := make(common.DataRow, rec.NumCols())
			for c, col := range rec.Columns() {
				v, err := arrowValue(col, int(i))
				if err != nil {
					return nil, nil, fmt.Errorf("column %q row %d: %w", columns[c], len(rows), err)
				}
				row[columns[c]] = v
			}
			rows = append(rows, row)
		}
	}
	if err := rdr.Err(); err != nil && err != io.EOF {
		return nil, nil, fmt.Errorf("read arrow ipc stream: %w", err)
	}
	return rows, columns, nil
}

// arrowValue narrows one Arrow cell into the JSON value space, so the
// result matches what the NDJSON path yields for the same data.
//
// An unsupported column type is named rather than approximated: a
// silently mangled value is far worse than a refused dataset, and the
// producer can always fall back to NDJSON, which is exactly why the
// baseline exists.
func arrowValue(col arrow.Array, i int) (interface{}, error) {
	num := func(f float64) interface{} { return numericValue(f) }
	if col.IsNull(i) {
		return nil, nil
	}
	switch c := col.(type) {
	case *array.Boolean:
		return c.Value(i), nil
	case *array.String:
		return c.Value(i), nil
	case *array.LargeString:
		return c.Value(i), nil
	case *array.Int8:
		return num(float64(c.Value(i))), nil
	case *array.Int16:
		return num(float64(c.Value(i))), nil
	case *array.Int32:
		return num(float64(c.Value(i))), nil
	case *array.Int64:
		return num(float64(c.Value(i))), nil
	case *array.Uint8:
		return num(float64(c.Value(i))), nil
	case *array.Uint16:
		return num(float64(c.Value(i))), nil
	case *array.Uint32:
		return num(float64(c.Value(i))), nil
	case *array.Uint64:
		return num(float64(c.Value(i))), nil
	case *array.Float32:
		return num(float64(c.Value(i))), nil
	case *array.Float64:
		return num(c.Value(i)), nil
	case *array.Date32:
		// RFC 3339 full-date, matching ADR-032 section 4's date encoding
		// -- the same string an NDJSON producer would have written.
		return c.Value(i).ToTime().Format("2006-01-02"), nil
	case *array.Timestamp:
		unit := arrow.Second
		if t, ok := col.DataType().(*arrow.TimestampType); ok {
			unit = t.Unit
		}
		return c.Value(i).ToTime(unit).UTC().Format("2006-01-02T15:04:05.999999999Z"), nil
	case *array.Decimal128:
		// Rendered, not float-converted: ADR-032 section 4 keeps decimals
		// exact as canonical strings precisely because float64 cannot.
		t, _ := col.DataType().(*arrow.Decimal128Type)
		var scale int32
		if t != nil {
			scale = t.Scale
		}
		return decimal128String(c.Value(i), scale), nil
	default:
		return nil, fmt.Errorf("arrow column type %s is not supported by this server's reader (use %s instead)", col.DataType(), CodecNDJSON)
	}
}

// numericValue mirrors normalizeJSONNumbers (engine/ndjson_transfer.go)
// so every codec returns the SAME Go type for the same value.
//
// NDJSON decodes with UseNumber and then prefers int64 for any integer
// that fits -- so a whole number written by an NDJSON producer comes
// back as int64, not float64. Arrow knows its column is Float64 and
// would naturally return float64, and that difference is exactly what
// ADR-033 section 8 forbids: downstream code type-switches on these
// values, so the transport would be changing behaviour.
//
// NDJSON is the incumbent, so Arrow matches it rather than the reverse.
// An equivalence test that compares rendered strings cannot see this --
// int64(2) and float64(2) both print as "2" -- so the test that guards
// it compares types.
func numericValue(f float64) interface{} {
	if i := int64(f); float64(i) == f {
		return i
	}
	return f
}

// decimal128String renders an Arrow decimal exactly, as the canonical
// decimal string ADR-032 section 4 rule 4 defines.
func decimal128String(v interface{ BigInt() *big.Int }, scale int32) string {
	unscaled := v.BigInt()
	if scale <= 0 {
		return unscaled.String()
	}
	neg := unscaled.Sign() < 0
	abs := new(big.Int).Abs(unscaled).String()
	// Widened to int rather than narrowing len() to int32: the digit
	// count is an int by definition, and converting it down is the
	// conversion that can overflow (gosec G115). scale comes from the
	// Arrow schema and is small in every real decimal, but "small in
	// practice" is not a bound -- this way there is nothing to overflow.
	width := int(scale)
	for len(abs) <= width {
		abs = "0" + abs
	}
	point := len(abs) - width
	out := abs[:point] + "." + abs[point:]
	if neg {
		out = "-" + out
	}
	return out
}

// arrowIPCFromRows encodes rows as an Arrow IPC stream. Test-facing:
// production only ever DECODES Arrow, because the producer is a task
// harness in its own language. Keeping the encoder here rather than in
// a test file means the round-trip test exercises this package's own
// understanding of the format rather than a second, hand-rolled one.
func arrowIPCFromRows(columns []string, rows []common.DataRow) ([]byte, error) {
	fields := make([]arrow.Field, 0, len(columns))
	for _, c := range columns {
		fields = append(fields, arrow.Field{Name: c, Type: arrow.BinaryTypes.String, Nullable: true})
	}
	schema := arrow.NewSchema(fields, nil)
	b := array.NewRecordBuilder(memory.DefaultAllocator, schema)
	defer b.Release()
	for _, row := range rows {
		for i, c := range columns {
			fb := b.Field(i).(*array.StringBuilder)
			v, ok := row[c]
			if !ok || v == nil {
				fb.AppendNull()
				continue
			}
			fb.Append(fmt.Sprintf("%v", v))
		}
	}
	rec := b.NewRecord()
	defer rec.Release()

	var buf bytes.Buffer
	w := ipc.NewWriter(&buf, ipc.WithSchema(schema))
	if err := w.Write(rec); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
