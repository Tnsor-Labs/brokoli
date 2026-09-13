package engine

// Streaming Arrow writes: the producing half of ADR-033 section 8 for
// outputs that never fit in memory.
//
// EncodeArrowIPC takes a *common.DataSet, so the dataset had to be
// resident before it could be written, which is the one thing the
// streaming paths exist to avoid. PutStream and streamTransformToRef
// therefore wrote NDJSON unconditionally, and the streaming producers,
// which by construction handle the largest datasets in the product,
// were the only ones that never got Arrow.
//
// The obstacle is that Arrow commits to a schema in its first bytes
// while common.DataRow carries no types at all. This resolves it the
// only way that cannot change the data: infer from the first batch, and
// decide the codec before a single byte is written. A stream whose
// first batch is not exactly representable is NDJSON from the start,
// which is what the whole stream would have been anyway, so nothing
// that works today stops working.
//
// What that leaves is a stream whose first batch is uniformly typed and
// whose later rows are not. By then the schema is on the wire and there
// is no honest recovery: widening the column would change the logical
// dataset contract section 8 forbids, and re-encoding would mean
// re-reading a source that may have moved underneath us. So it fails,
// naming the column, both types and the row ordinal. That is #529's
// rule -- a degradation path must log or fail, never quietly succeed --
// applied to the one case where degrading is not available.

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/Tnsor-Labs/brokoli/pkg/artifact"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// arrowStreamEncoder writes record batches into an Arrow IPC stream
// against a schema fixed at construction.
//
// The schema is an argument rather than something inferred here, because
// only the caller knows whether it holds the whole dataset (EncodeArrowIPC)
// or just the first batch of one (the streaming producers), and the
// consequences of a mismatch differ between them.
type arrowStreamEncoder struct {
	w       *ipc.Writer
	b       *array.RecordBuilder
	schema  *arrow.Schema
	columns []string
	pending int
	rows    int64
}

func newArrowStreamEncoder(w io.Writer, schema *arrow.Schema) *arrowStreamEncoder {
	columns := make([]string, 0, len(schema.Fields()))
	for _, f := range schema.Fields() {
		columns = append(columns, f.Name)
	}
	return &arrowStreamEncoder{
		w:       ipc.NewWriter(w, ipc.WithSchema(schema)),
		b:       array.NewRecordBuilder(memory.DefaultAllocator, schema),
		schema:  schema,
		columns: columns,
	}
}

// WriteBatch appends every row in ds, flushing a record batch each time
// arrowRecordRows have accumulated.
//
// Column order comes from the schema, not from ds, so a producer that
// reorders its column list mid-stream still writes each value into the
// field it belongs to. A producer that adds or drops a column is a
// different matter and is refused below: a value with nowhere to go
// would otherwise vanish silently.
func (e *arrowStreamEncoder) WriteBatch(ds *common.DataSet) error {
	if ds == nil || len(ds.Rows) == 0 {
		return nil
	}
	if err := e.checkColumns(ds.Columns); err != nil {
		return err
	}
	for _, row := range ds.Rows {
		for i, col := range e.columns {
			if err := e.appendValue(i, col, row); err != nil {
				return err
			}
		}
		e.rows++
		e.pending++
		if e.pending >= arrowRecordRows {
			if err := e.flush(); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkColumns refuses a batch whose column set differs from the schema.
//
// Only the set matters, not the order: the appender indexes by name.
// Reported by naming the difference rather than the two full lists,
// which for a wide table would bury the one column that changed.
func (e *arrowStreamEncoder) checkColumns(cols []string) error {
	if len(cols) == 0 {
		return nil // a batch that declares no columns inherits the schema's
	}
	known := make(map[string]bool, len(e.columns))
	for _, c := range e.columns {
		known[c] = true
	}
	var added []string
	for _, c := range cols {
		if !known[c] {
			added = append(added, c)
		}
		delete(known, c)
	}
	var dropped []string
	for c := range known {
		dropped = append(dropped, c)
	}
	if len(added) == 0 && len(dropped) == 0 {
		return nil
	}
	return fmt.Errorf(
		"arrow stream: batch at row %d changes the column set fixed by the first batch (added %v, dropped %v); "+
			"an arrow stream cannot change schema once its first bytes are written",
		e.rows, added, dropped)
}

// appendValue writes one cell, or the typed error that says why it could
// not. The type switch mirrors arrowEncodableSchema's, and the two must
// stay in step: a kind accepted there without a case here would panic on
// the type assertion instead of being reported.
func (e *arrowStreamEncoder) appendValue(i int, col string, row common.DataRow) error {
	v, present := row[col]
	if !present || v == nil {
		e.b.Field(i).AppendNull()
		return nil
	}
	switch fb := e.b.Field(i).(type) {
	case *array.BooleanBuilder:
		n, ok := v.(bool)
		if !ok {
			return e.driftError(col, "bool", v)
		}
		fb.Append(n)
	case *array.StringBuilder:
		n, ok := v.(string)
		if !ok {
			return e.driftError(col, "string", v)
		}
		fb.Append(n)
	case *array.Float64Builder:
		// An integer in a Float64 column is the widened case
		// arrowEncodableSchema allows: numericValue hands back int64 for
		// every whole number, so a decoded float column is mixed, and
		// Float64 is what holds both. Converted only when the conversion
		// is exact, since past 2^53 it silently would not be.
		switch n := v.(type) {
		case float64:
			fb.Append(n)
		case int64:
			if !exactAsFloat64(n) {
				return e.lossyWideningError(col, n)
			}
			fb.Append(float64(n))
		case int:
			if !exactAsFloat64(int64(n)) {
				return e.lossyWideningError(col, int64(n))
			}
			fb.Append(float64(n))
		default:
			return e.driftError(col, "float64", v)
		}
	case *array.Int64Builder:
		// Both Go spellings of an integer reach the same Arrow type.
		switch n := v.(type) {
		case int64:
			fb.Append(n)
		case int:
			fb.Append(int64(n))
		default:
			return e.driftError(col, "int64", v)
		}
	default:
		// Unreachable while this switch covers arrowEncodableSchema's.
		// Named rather than ignored so a type added there without a case
		// here fails instead of silently dropping a column.
		return fmt.Errorf("arrow stream: no builder for column %q", col)
	}
	return nil
}

// driftError reports a value that does not match the committed schema.
//
// It carries the row ordinal because the offending row is otherwise
// unfindable in a stream that may be millions long, and it names the
// escape hatch because the operator's realistic next move is to stop
// choosing arrow for this pipeline rather than to fix the source.
func (e *arrowStreamEncoder) driftError(col, want string, got interface{}) error {
	return fmt.Errorf(
		"arrow stream: column %q was %s in the first batch and is %T at row %d; "+
			"an arrow stream cannot widen a column once its schema is written. "+
			"Set %s=ndjson to write this pipeline's streamed outputs as ndjson, which represents any value",
		col, want, got, e.rows, streamCodecEnv)
}

// lossyWideningError reports an integer that cannot be written into a
// column the schema pass widened to Float64.
//
// Distinct from driftError because the cause is different and so is the
// remedy: the column's type did not change, the value is simply too
// large for the representation the first batch justified. It can only
// happen on a streaming write, where the schema was inferred from one
// batch and this integer was in a later one.
func (e *arrowStreamEncoder) lossyWideningError(col string, v int64) error {
	return fmt.Errorf(
		"arrow stream: column %q holds both integers and floats, so it was written as float64, "+
			"and %d at row %d cannot be represented exactly that way (integers past 2^53 lose precision). "+
			"Set %s=ndjson to write this pipeline's streamed outputs as ndjson, which keeps it exact",
		col, v, e.rows, streamCodecEnv)
}

func (e *arrowStreamEncoder) flush() error {
	rec := e.b.NewRecord()
	defer rec.Release()
	if rec.NumRows() == 0 {
		return nil
	}
	if err := e.w.Write(rec); err != nil {
		return fmt.Errorf("arrow encode: %w", err)
	}
	e.pending = 0
	return nil
}

// Close flushes the final partial batch and ends the stream. The builder
// is released here rather than by the caller so a single defer suffices.
func (e *arrowStreamEncoder) Close() error {
	defer e.b.Release()
	if err := e.flush(); err != nil {
		return err
	}
	return e.w.Close()
}

// Rows is how many rows were actually written, which is what the ref
// reports and what every downstream consumer treats as the node's row
// count. Counted here rather than trusted from the producer.
func (e *arrowStreamEncoder) Rows() int64 { return e.rows }

// streamCodecEnv selects the codec for streamed writes.
//
// It exists for two reasons, neither of them tuning. "ndjson" is the
// escape hatch the drift error points at, and restores byte-for-byte the
// behaviour of every release before this one. "arrow" makes the choice
// observable: without it a benchmark cannot prove which codec ran, and a
// measurement that cannot tell the two apart is not a measurement.
const streamCodecEnv = "BROKOLI_STREAM_CODEC"

type streamCodec int

const (
	// streamCodecAuto writes Arrow when the first batch is exactly
	// representable and NDJSON otherwise.
	streamCodecAuto streamCodec = iota
	// streamCodecNDJSON never writes Arrow.
	streamCodecNDJSON
	// streamCodecArrow requires Arrow and fails a stream whose first
	// batch cannot be represented, rather than falling back.
	streamCodecArrow
)

func streamCodecFromEnv() streamCodec {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(streamCodecEnv))) {
	case artifact.FormatNDJSON:
		return streamCodecNDJSON
	case artifact.FormatArrowIPC, "arrow", "arrow_ipc":
		// The underscore spelling is accepted because the canonical
		// format name is hyphenated and that slip should not silently
		// select the other codec. It is the same word, not a guess at a
		// different one.
		return streamCodecArrow
	default:
		// Including an unrecognised value: this selects a codec, and
		// guessing at a misspelling would be choosing one on the
		// operator's behalf. Auto is what no setting means.
		return streamCodecAuto
	}
}

// datasetStreamWriter writes a sequence of batches to w in one codec,
// chosen from the first batch and reported before any byte is written.
//
// Both streaming producers need exactly this and had a hand-rolled
// NDJSON half of it each. The codec has to be settled up front because
// the blob's media type is an argument to Put, which starts consuming
// the pipe this writes into: by the time a byte exists it is too late to
// label it.
type datasetStreamWriter struct {
	w        io.Writer
	codec    streamCodec
	decided  bool
	arrow    *arrowStreamEncoder
	ndjson   *ndjsonStreamEncoder
	rows     int64
	firstErr error
}

func newDatasetStreamWriter(w io.Writer, codec streamCodec) *datasetStreamWriter {
	return &datasetStreamWriter{w: w, codec: codec}
}

// Decide fixes the codec from the first batch and returns the format and
// media type the blob must be labelled with.
//
// Separate from WriteBatch because the caller has to learn the answer
// before it can start the Put that drains this writer. Calling WriteBatch
// first is a programming error, not a fallback: it would write bytes
// under a label nobody has chosen yet.
func (d *datasetStreamWriter) Decide(first *common.DataSet) (format, mediaType string, err error) {
	if d.decided {
		return "", "", fmt.Errorf("dataset stream: codec already decided")
	}
	d.decided = true

	schema, encodable := arrowEncodableSchema(first)
	switch {
	case d.codec == streamCodecNDJSON, d.codec == streamCodecAuto && !encodable:
		d.ndjson = newNDJSONStreamEncoder(d.w)
		return artifact.FormatNDJSON, artifact.MediaTypeNDJSON, nil
	case d.codec == streamCodecArrow && !encodable:
		return "", "", fmt.Errorf(
			"%s=arrow, but this stream's first batch is not exactly representable as arrow "+
				"(a column with mixed types, a column that is entirely null, or a nested value); "+
				"unset %s to fall back to ndjson automatically",
			streamCodecEnv, streamCodecEnv)
	default:
		d.arrow = newArrowStreamEncoder(d.w, schema)
		return artifact.FormatArrowIPC, artifact.MediaTypeArrowIPC, nil
	}
}

func (d *datasetStreamWriter) WriteBatch(ds *common.DataSet) error {
	if !d.decided {
		return fmt.Errorf("dataset stream: WriteBatch before Decide")
	}
	if ds == nil || len(ds.Rows) == 0 {
		return nil
	}
	if d.arrow != nil {
		if err := d.arrow.WriteBatch(ds); err != nil {
			return err
		}
		d.rows = d.arrow.Rows()
		return nil
	}
	n, err := d.ndjson.WriteBatch(ds)
	d.rows += n
	return err
}

// Close finishes the stream and reports how many rows went into it.
//
// A stream that produced nothing still has to be closed, and the two
// codecs disagree about what empty looks like: NDJSON has the "[]"
// sentinel every other writer emits, while an Arrow stream with a
// schema and no batches is already a valid empty dataset.
//
// Deciding and installing an encoder are not the same thing: a Decide
// that refuses leaves neither encoder built, and this is still called on
// that path by a caller unwinding. Checked rather than assumed, because
// assuming it panicked.
func (d *datasetStreamWriter) Close() (int64, error) {
	switch {
	case d.arrow != nil:
		return d.rows, d.arrow.Close()
	case d.ndjson != nil:
		return d.rows, d.ndjson.Close(d.rows == 0)
	default:
		return 0, nil
	}
}
