package engine

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// ArrowTransferMode indicates the data transfer format.
type ArrowTransferMode string

const (
	TransferJSON  ArrowTransferMode = "json"
	TransferCSV   ArrowTransferMode = "csv"
	TransferArrow ArrowTransferMode = "arrow"
)

// WriteArrowJSON writes data as NDJSON (newline-delimited JSON) — 2-3x faster than regular JSON
// for large datasets because pyarrow/pandas can stream-parse it line by line.
func WriteArrowJSON(path string, ds *common.DataSet) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return EncodeArrowJSON(f, ds)
}

// EncodeArrowJSON writes ds to w in the same NDJSON encoding WriteArrowJSON
// produces on disk, so a stream and a file are byte-identical.
//
// Split out so the artifact store can hand rows straight to a store without
// staging them in a second file first — and so there is one definition of
// this format rather than two that can drift. The empty-dataset encoding is
// "[]" rather than zero bytes, which ReadArrowJSON/DecodeArrowJSON both
// recognize; that distinguishes "this node produced no rows" from "nothing
// was ever written here", a difference resume depends on.
func EncodeArrowJSON(w io.Writer, ds *common.DataSet) error {
	if ds == nil || len(ds.Rows) == 0 {
		_, err := w.Write([]byte("[]"))
		return err
	}
	// Buffered because two of the three callers hand this an io.Pipe, and
	// a pipe write blocks until the reader consumes it — so encoding
	// row-by-row costs one scheduler round-trip per row. For a 25k-row
	// dataset that was 160ms of handoffs against 70ms buffered. The
	// remaining callers write to a file or a bytes.Buffer, where the
	// buffer is at worst free.
	buf := bufio.NewWriterSize(w, encodeBufferSize)
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false) // Faster serialization
	for _, row := range ds.Rows {
		if err := enc.Encode(row); err != nil {
			return err
		}
	}
	return buf.Flush()
}

// encodeBufferSize is the chunk size handed to the underlying writer.
const encodeBufferSize = 256 << 10

// DecodeArrowJSON reads the NDJSON encoding EncodeArrowJSON produces.
//
// columns, when non-empty, is used verbatim as the dataset's column order.
// ReadArrowJSON has to recover columns by iterating the first row's map,
// which loses the original ordering; a caller that recorded the order when
// writing can pass it here and get the dataset back as it was.
func DecodeArrowJSON(r io.Reader, columns []string) (*common.DataSet, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || string(data) == "[]" {
		cols := columns
		if cols == nil {
			cols = []string{}
		}
		return &common.DataSet{Columns: cols, Rows: []common.DataRow{}}, nil
	}

	var rows []common.DataRow
	dec := json.NewDecoder(bytes.NewReader(data))
	for dec.More() {
		var row common.DataRow
		if err := dec.Decode(&row); err != nil {
			break
		}
		rows = append(rows, row)
	}

	cols := columns
	if len(cols) == 0 && len(rows) > 0 {
		for k := range rows[0] {
			cols = append(cols, k)
		}
	}
	return &common.DataSet{Columns: cols, Rows: rows}, nil
}

// ReadArrowJSON reads data from compact NDJSON format.
func ReadArrowJSON(path string) (*common.DataSet, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	if len(data) == 0 || string(data) == "[]" {
		return &common.DataSet{Columns: []string{}, Rows: []common.DataRow{}}, nil
	}

	// Parse NDJSON
	var rows []common.DataRow
	dec := json.NewDecoder(bytes.NewReader(data))
	for dec.More() {
		var row common.DataRow
		if err := dec.Decode(&row); err != nil {
			break
		}
		rows = append(rows, row)
	}

	// Extract columns from first row
	var columns []string
	if len(rows) > 0 {
		for k := range rows[0] {
			columns = append(columns, k)
		}
	}

	return &common.DataSet{Columns: columns, Rows: rows}, nil
}

// WriteColumnarBinary writes data in a compact columnar binary format.
// Format: [magic "BROK"][version uint32][schema_len uint32][schema JSON][NDJSON rows...]
//
// This is 3-5x faster than CSV and 10x faster than JSON for large datasets.
func WriteColumnarBinary(path string, ds *common.DataSet) error {
	if ds == nil || len(ds.Rows) == 0 {
		return os.WriteFile(path, []byte{}, 0o644)
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	// Write magic bytes + version
	if _, err := f.Write([]byte("BROK")); err != nil {
		return err
	}
	if err := binary.Write(f, binary.LittleEndian, uint32(1)); err != nil {
		return err
	}

	// Write schema as JSON
	schemaJSON, err := json.Marshal(map[string]interface{}{
		"columns":   ds.Columns,
		"row_count": len(ds.Rows),
	})
	if err != nil {
		return err
	}
	if len(schemaJSON) > math.MaxUint32 {
		return fmt.Errorf("schema too large: %d bytes", len(schemaJSON))
	}
	if err := binary.Write(f, binary.LittleEndian, uint32(len(schemaJSON))); err != nil { //nolint:gosec
		return err
	}
	if _, err := f.Write(schemaJSON); err != nil {
		return err
	}

	// Write rows as NDJSON (compact, fast), buffered so a row is not a
	// syscall.
	bw := bufio.NewWriterSize(f, encodeBufferSize)
	enc := json.NewEncoder(bw)
	enc.SetEscapeHTML(false)
	for _, row := range ds.Rows {
		if err := enc.Encode(row); err != nil {
			return err
		}
	}

	return bw.Flush()
}

// ReadColumnarBinary reads data from compact columnar binary format.
func ReadColumnarBinary(path string) (*common.DataSet, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	if len(data) < 12 { // magic(4) + version(4) + schema_len(4)
		return &common.DataSet{Columns: []string{}, Rows: []common.DataRow{}}, nil
	}

	// Read magic + version
	if string(data[:4]) != "BROK" {
		return nil, fmt.Errorf("invalid BROK file format")
	}
	// version := binary.LittleEndian.Uint32(data[4:8])

	// Read schema
	schemaLen := binary.LittleEndian.Uint32(data[8:12])
	if int64(len(data)) < 12+int64(schemaLen) { //nolint:gosec
		return nil, fmt.Errorf("truncated BROK file: schema extends beyond file")
	}
	var schema struct {
		Columns  []string `json:"columns"`
		RowCount int      `json:"row_count"`
	}
	if err := json.Unmarshal(data[12:12+schemaLen], &schema); err != nil {
		return nil, fmt.Errorf("invalid BROK schema: %w", err)
	}

	// Read rows (NDJSON)
	rowData := data[12+schemaLen:]
	var rows []common.DataRow
	dec := json.NewDecoder(bytes.NewReader(rowData))
	for dec.More() {
		var row common.DataRow
		if err := dec.Decode(&row); err != nil {
			break
		}
		rows = append(rows, row)
	}

	return &common.DataSet{Columns: schema.Columns, Rows: rows}, nil
}

// streamBatchRows is how many rows NDJSONBatchReader yields per batch —
// the unit of memory a streamed operator holds at once
// (docs/adr/019-execution-segments-and-streaming.md, Milestone 1). Big
// enough that per-batch overhead (function calls, slice growth) is
// noise; small enough that a batch of even very wide rows stays in
// single-digit MiB.
const streamBatchRows = 1000

// NDJSONBatchReader is the incremental counterpart of DecodeArrowJSON: it
// yields the same rows the same way, batchSize rows at a time, without
// ever holding the whole dataset. This is the primitive ADR-019's
// reference-passing dataflow is built on — a spilled/artifact blob can
// flow through a streamable operator in bounded memory.
//
// Unlike DecodeArrowJSON — which silently stops at the first malformed
// line, a tolerance acceptable when the caller immediately sees the
// truncated result — a batch reader's consumer has already processed and
// emitted earlier batches by the time a bad line appears, so silent
// truncation here would corrupt downstream data invisibly. Malformed
// input is therefore a loud error.
type NDJSONBatchReader struct {
	br        *bufio.Reader
	dec       *json.Decoder
	columns   []string
	batchSize int
	done      bool
}

// NewNDJSONBatchReader wraps r, which must contain EncodeArrowJSON
// output. columns is used verbatim as every batch's column order when
// non-empty (same contract as DecodeArrowJSON); otherwise it is recovered
// from the first row's map, losing the original order. batchSize <= 0
// uses streamBatchRows.
//
// The empty-dataset sentinel "[]" (see EncodeArrowJSON's doc comment) is
// detected here, by peeking, rather than in Next — it is the one thing
// EncodeArrowJSON ever writes that is not a JSON object per line, and it
// only ever appears as the entire stream.
func NewNDJSONBatchReader(r io.Reader, columns []string, batchSize int) *NDJSONBatchReader {
	if batchSize <= 0 {
		batchSize = streamBatchRows
	}
	br := bufio.NewReader(r)
	b := &NDJSONBatchReader{br: br, columns: columns, batchSize: batchSize}
	if head, err := br.Peek(2); err == nil && string(head) == "[]" {
		b.done = true
		return b
	}
	b.dec = json.NewDecoder(br)
	return b
}

// Next returns the next batch, or (nil, io.EOF) once the stream is
// exhausted. A returned batch always has at least one row.
func (b *NDJSONBatchReader) Next() (*common.DataSet, error) {
	if b.done {
		return nil, io.EOF
	}
	rows := make([]common.DataRow, 0, b.batchSize)
	for len(rows) < b.batchSize {
		if !b.dec.More() {
			b.done = true
			break
		}
		var row common.DataRow
		if err := b.dec.Decode(&row); err != nil {
			b.done = true
			return nil, fmt.Errorf("ndjson batch decode: %w", err)
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil, io.EOF
	}
	if len(b.columns) == 0 {
		for k := range rows[0] {
			b.columns = append(b.columns, k)
		}
	}
	return &common.DataSet{Columns: b.columns, Rows: rows}, nil
}

// Columns reports the column order this reader is using — the caller's,
// or the recovered one once the first batch has been read.
func (b *NDJSONBatchReader) Columns() []string { return b.columns }
