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
		// Set by an integer too large to survive a widening to Float64.
		// Tracked per column rather than checked at the moment of
		// widening, because the integer that breaks it may be seen before
		// the float that forces the decision.
		lossyIfWidened := false
		widened := false
		for _, row := range ds.Rows {
			v, present := row[col]
			if !present || v == nil {
				continue
			}
			var this arrow.DataType
			switch n := v.(type) {
			case bool:
				this = arrow.FixedWidthTypes.Boolean
			case string:
				this = arrow.BinaryTypes.String
			case float64:
				this = arrow.PrimitiveTypes.Float64
			case int64, int:
				var i int64
				if v64, ok := n.(int64); ok {
					i = v64
				} else {
					i = int64(n.(int))
				}
				if !exactAsFloat64(i) {
					lossyIfWidened = true
				}
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
				continue
			}
			if dt.ID() == this.ID() {
				continue
			}
			// A column holding both int64 and float64 is the one mixture
			// that is exactly representable, and it is not exotic: it is
			// what every decoded dataset looks like. numericValue returns
			// int64 for any whole number and float64 otherwise, so a
			// float column with some whole values comes back mixed from
			// BOTH codecs, and re-encoding it was falling back to NDJSON
			// forever after.
			//
			// Float64 holds them all, and the decoder narrows the whole
			// ones back to int64 on the way out, so a row that went in as
			// int64(3) comes back as int64(3). Only integers past 2^53
			// break that, and those are refused by checking every one of
			// them rather than assuming.
			if isNumericID(dt.ID()) && isNumericID(this.ID()) {
				dt = arrow.PrimitiveTypes.Float64
				widened = true
				continue
			}
			return nil, false // mixed types in one column
		}
		if dt == nil {
			return nil, false // all-null column: no type to infer
		}
		// Re-checked after the whole column is scanned, not only where the
		// widening was decided: the integer that cannot survive it may
		// appear after the float that forces it.
		if widened && lossyIfWidened {
			return nil, false
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

	// The appender, the batching and the flush all live in
	// arrowStreamEncoder, which the streaming producers need anyway.
	// Keeping a second copy here is how the two would drift: this one
	// would learn a type the streaming one did not, and a dataset would
	// encode differently depending on which path it took.
	//
	// Batches are flushed every arrowRecordRows rather than written as
	// one record, which is what this did. A single record meant the
	// reader's "one record batch becomes one batch of rows" contract
	// resolved to the whole dataset, so ArrowBatchReader streamed
	// correctly over a stream that had nothing to stream: an Arrow ref
	// materialised on read no matter how carefully the reader was
	// written.
	enc := newArrowStreamEncoder(w, schema)
	if err := enc.WriteBatch(ds); err != nil {
		return err
	}
	return enc.Close()
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

// isNumericID reports whether an Arrow type is one of the two numeric
// kinds this encoder infers, which are the only pair it will widen.
func isNumericID(id arrow.Type) bool {
	return id == arrow.INT64 || id == arrow.FLOAT64
}

// exactAsFloat64 reports whether an integer survives a round trip
// through float64.
//
// The round trip is the criterion, not a magnitude bound. Past 2^53 the
// representable integers thin out but do not stop: 2^54 is exact and
// 2^53+1 is not, so a bound would refuse values that are fine and a
// reader would have to take the number on trust. This asks the question
// being relied on.
func exactAsFloat64(v int64) bool {
	return int64(float64(v)) == v
}
