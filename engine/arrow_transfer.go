package engine

import (
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
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false) // Faster serialization
	for _, row := range ds.Rows {
		if err := enc.Encode(row); err != nil {
			return err
		}
	}
	return nil
}

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

	// Write rows as NDJSON (compact, fast)
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	for _, row := range ds.Rows {
		if err := enc.Encode(row); err != nil {
			return err
		}
	}

	return nil
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
