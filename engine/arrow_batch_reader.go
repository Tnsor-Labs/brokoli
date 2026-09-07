package engine

// Streaming Arrow IPC datasets (ADR-033 section 8, ADR-012's DatasetRef).
//
// The materializing read path got Arrow first, and measured 6-8x faster
// than NDJSON there. But that path is capped -- maxTaskDatasetBytes and
// maxTaskDatasetRows stop it well before the sizes where a columnar
// transport matters most, on purpose, because it holds the whole
// DataSet in memory. Real volume travels a different route: above
// DefaultStreamThresholdBytes a dataset becomes an artifact.DatasetRef
// and is consumed in bounded batches, which is how a million rows fit
// through a worker that could never hold them at once.
//
// That route was NDJSON-only, so the format's advantage stopped exactly
// where the data got big. This is the same decode win applied there.
//
// Arrow IPC is natively a sequence of record batches, so the mapping is
// direct: one Arrow record batch becomes one batch of rows, and the
// reader's memory profile is the producer's batch size rather than
// anything chosen here. Rows are still materialized into
// common.DataRow, exactly as the NDJSON reader does -- that boxing is a
// cost both formats pay, and the measurement showed it was never what
// separated them. Avoiding it entirely would mean a columnar DataSet,
// which is a change to the engine's data model rather than to a codec.

import (
	"fmt"
	"io"

	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// ArrowBatchReader is the arrow-ipc counterpart of NDJSONBatchReader,
// and deliberately mirrors its contract: Next returns io.EOF when the
// stream is exhausted, an empty stream yields io.EOF immediately, and
// Columns reports the order rows are keyed by.
type ArrowBatchReader struct {
	rdr     *ipc.Reader
	columns []string
	done    bool
}

// NewArrowBatchReader wraps r, which must contain an Arrow IPC stream.
//
// columns, when non-empty, is the caller's declared order (a
// DatasetRef.Columns); it is used verbatim so a ref's recorded order
// wins over the file's, matching NewNDJSONBatchReader. Otherwise the
// schema's field order is used, which -- unlike NDJSON, where order has
// to be recovered from a map and is lost -- Arrow actually carries.
func NewArrowBatchReader(r io.Reader, columns []string) (*ArrowBatchReader, error) {
	rdr, err := ipc.NewReader(r, ipc.WithAllocator(memory.DefaultAllocator))
	if err != nil {
		return nil, fmt.Errorf("not a readable arrow ipc stream: %w", err)
	}
	b := &ArrowBatchReader{rdr: rdr, columns: columns}
	if len(b.columns) == 0 {
		for _, f := range rdr.Schema().Fields() {
			b.columns = append(b.columns, f.Name)
		}
	}
	return b, nil
}

// Next returns the next record batch as rows, or io.EOF when the stream
// ends.
func (b *ArrowBatchReader) Next() (*common.DataSet, error) {
	if b.done {
		return nil, io.EOF
	}
	if !b.rdr.Next() {
		b.done = true
		if err := b.rdr.Err(); err != nil && err != io.EOF {
			return nil, fmt.Errorf("arrow batch decode: %w", err)
		}
		return nil, io.EOF
	}
	rec := b.rdr.Record()

	// Field order comes from the schema, but the caller's declared
	// columns may name them in a different order; index by schema
	// position and key by the caller's name for that position, so a
	// ref's Columns stays authoritative without reordering the data.
	schemaNames := make([]string, rec.NumCols())
	for i, f := range b.rdr.Schema().Fields() {
		if i < len(schemaNames) {
			schemaNames[i] = f.Name
		}
	}

	rows := make([]common.DataRow, 0, rec.NumRows())
	for i := int64(0); i < rec.NumRows(); i++ {
		row := make(common.DataRow, rec.NumCols())
		for c, col := range rec.Columns() {
			// preferInt: this path stands in for decodeRow, which
			// prefers int64 for whole numbers -- see arrowValueAs.
			v, err := arrowValueAs(col, int(i), true)
			if err != nil {
				b.done = true
				return nil, fmt.Errorf("arrow batch decode: column %q: %w", schemaNames[c], err)
			}
			row[schemaNames[c]] = v
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		// A zero-row batch is legal in Arrow and is not the end of the
		// stream; skip it rather than reporting EOF early.
		return b.Next()
	}
	return &common.DataSet{Columns: b.columns, Rows: rows}, nil
}

// Columns reports the column order this reader keys rows by.
func (b *ArrowBatchReader) Columns() []string { return b.columns }

// Release frees the Arrow reader's buffers. Callers that finish early
// must call it; the io.Closer returned alongside the reader handles the
// underlying blob.
func (b *ArrowBatchReader) Release() { b.rdr.Release() }

// DatasetBatchReader is what OpenBatches hands back: bounded batches of
// rows, whichever encoding the ref names.
//
// An interface rather than a concrete type because the format is now a
// property of the data, not of the code path -- callers stream batches
// and should not care which codec produced them, which is exactly
// ADR-033 section 8's rule that transport choice "does not change the
// logical dataset contract".
type DatasetBatchReader interface {
	// Next returns the next batch, or io.EOF when the stream ends.
	Next() (*common.DataSet, error)
	// Columns reports the order rows are keyed by.
	Columns() []string
}

var (
	_ DatasetBatchReader = (*NDJSONBatchReader)(nil)
	_ DatasetBatchReader = (*ArrowBatchReader)(nil)
)
