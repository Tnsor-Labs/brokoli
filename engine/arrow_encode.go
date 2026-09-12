package engine

// Writing Arrow IPC datasets (the producing half of ADR-033 section 8).
//
// Reading Arrow was the easy direction: the stream carries its own
// schema. Writing has to invent one, because common.DataRow is
// map[string]interface{} and the types live only in the values. That
// inference is the whole risk here -- get it wrong and a dataset comes
// back differently than it went in, which is precisely what section 8
// forbids ("transport choice ... does not change the logical dataset
// contract").
//
// So the rule is conservative on purpose: Arrow is used ONLY when every
// column is uniformly typed and of a kind that round-trips exactly.
// Anything else -- a column holding a string in one row and a number in
// another, a nested object, an unsupported kind -- falls back to NDJSON,
// which represents any JSON value faithfully. Arrow is an optimization
// that must never be a semantic change, so when it cannot promise that,
// it declines.
//
// The payoff is on the read side: a spilled dataset written as Arrow is
// decoded 6-8x faster with a quarter of the allocations (measured in
// task_dataset_codec_bench_test.go), and every consumer of that ref gets
// it for free.

import (
	"fmt"
	"io"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// arrowEncodableSchema infers an Arrow schema from a dataset's values,
// reporting ok=false when any column cannot be represented exactly.
//
// A nil is not a type: it only makes a column nullable, so a column of
// nulls and strings is a nullable string column, while a column of
// strings and numbers has no single type and disqualifies the dataset.
// A column that is entirely null has no inferable type at all and also
// disqualifies it -- guessing one would be inventing a contract.
func arrowEncodableSchema(ds *common.DataSet) (*arrow.Schema, bool) {
	if ds == nil || len(ds.Columns) == 0 || len(ds.Rows) == 0 {
		return nil, false
	}
	fields := make([]arrow.Field, 0, len(ds.Columns))
	for _, col := range ds.Columns {
		var dt arrow.DataType
		for _, row := range ds.Rows {
			v, present := row[col]
			if !present || v == nil {
				continue
			}
			var this arrow.DataType
			switch v.(type) {
			case bool:
				this = arrow.FixedWidthTypes.Boolean
			case string:
				this = arrow.BinaryTypes.String
			case float64:
				this = arrow.PrimitiveTypes.Float64
			case int64, int:
				// Integers were excluded here on the grounds that mapping
				// them back had not been decided. It has now: Arrow Int64
				// holds every int64 exactly and the decoder returns int64
				// for it, which is the same type the NDJSON path yields
				// for a whole number.
				//
				// This exclusion was expensive. Essentially every real
				// table has an integer id, one such column disqualified
				// the whole dataset, and the fallback was silent, so
				// Arrow almost never ran (#521). Measured on the columns
				// it did accept: 6.8x faster to decode, 4.5x to encode.
				this = arrow.PrimitiveTypes.Int64
			default:
				// json.Number, nested maps and slices still land here:
				// representable in principle, not without deciding how
				// they map back, so NDJSON keeps them.
				return nil, false
			}
			if dt == nil {
				dt = this
			} else if dt.ID() != this.ID() {
				return nil, false // mixed types in one column
			}
		}
		if dt == nil {
			return nil, false // all-null column: no type to infer
		}
		fields = append(fields, arrow.Field{Name: col, Type: dt, Nullable: true})
	}
	return arrow.NewSchema(fields, nil), true
}

// EncodeArrowIPC writes ds as an Arrow IPC stream with typed columns.
//
// Callers should check arrowEncodableSchema first; this returns an error
// rather than guessing if the dataset turns out not to be encodable,
// so a caller that skipped the check fails loudly instead of writing
// something that reads back wrong.
func EncodeArrowIPC(w io.Writer, ds *common.DataSet) error {
	schema, ok := arrowEncodableSchema(ds)
	if !ok {
		return fmt.Errorf("dataset is not exactly representable as arrow: use %s", artifactFormatNDJSONName)
	}

	wr := ipc.NewWriter(w, ipc.WithSchema(schema))
	b := array.NewRecordBuilder(memory.DefaultAllocator, schema)
	defer b.Release()

	// Flush the builder as one record batch and start the next.
	//
	// Called every arrowRecordRows rows rather than once at the end,
	// which is what this did. A single record meant the reader's
	// "one record batch becomes one batch of rows" contract resolved to
	// the whole dataset, so ArrowBatchReader streamed correctly over a
	// stream that had nothing to stream: an Arrow ref materialised on
	// read no matter how carefully the reader was written.
	flush := func() error {
		rec := b.NewRecord()
		defer rec.Release()
		if rec.NumRows() == 0 {
			return nil
		}
		if err := wr.Write(rec); err != nil {
			return fmt.Errorf("arrow encode: %w", err)
		}
		return nil
	}

	pending := 0
	for _, row := range ds.Rows {
		for i, col := range ds.Columns {
			v, present := row[col]
			if !present || v == nil {
				b.Field(i).AppendNull()
				continue
			}
			switch fb := b.Field(i).(type) {
			case *array.BooleanBuilder:
				fb.Append(v.(bool))
			case *array.StringBuilder:
				fb.Append(v.(string))
			case *array.Float64Builder:
				fb.Append(v.(float64))
			case *array.Int64Builder:
				// Both spellings reach the same Arrow type. The schema
				// pass already established the column is integral, so a
				// value of any other kind here is a bug in that pass
				// rather than data to coerce.
				switch n := v.(type) {
				case int64:
					fb.Append(n)
				case int:
					fb.Append(int64(n))
				default:
					return fmt.Errorf("arrow encode: column %q inferred as int64 holds %T", col, v)
				}
			default:
				// Unreachable: arrowEncodableSchema only ever produces
				// the builder kinds handled above. Named rather
				// than ignored so a future type added there without a
				// case here fails instead of silently dropping a column.
				return fmt.Errorf("arrow encode: no builder for column %q", col)
			}
		}
		pending++
		if pending >= arrowRecordRows {
			if err := flush(); err != nil {
				return err
			}
			pending = 0
		}
	}
	if err := flush(); err != nil {
		return err
	}
	return wr.Close()
}

// arrowRecordRows is how many rows go into one Arrow record batch.
//
// Matched to streamBatchRows so a reader sees the same batch shape
// whichever codec produced the blob, and so the memory a reader holds
// for an Arrow ref is the same order as for an NDJSON one. The number
// is a memory bound, not a tuning knob: larger batches compress and
// decode marginally better and cost proportionally more resident rows.
const arrowRecordRows = streamBatchRows

// artifactFormatNDJSONName is the name used in the message above,
// spelled out rather than imported to keep this file free of a
// dependency it otherwise would not need.
const artifactFormatNDJSONName = "ndjson"
