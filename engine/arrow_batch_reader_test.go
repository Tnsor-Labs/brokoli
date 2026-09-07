package engine

// Streaming Arrow datasets must behave exactly as streaming NDJSON ones
// do -- ADR-033 section 8: transport choice "does not change the logical
// dataset contract". That applies per batch here, not just per dataset.

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

func drainBatches(t *testing.T, r DatasetBatchReader) []common.DataRow {
	t.Helper()
	var all []common.DataRow
	for {
		ds, err := r.Next()
		if err == io.EOF {
			return all
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		all = append(all, ds.Rows...)
	}
}

// The same rows, streamed through either reader, must come out the same.
func TestArrowAndNDJSONBatchReadersAgree(t *testing.T) {
	columns := []string{"id", "name"}
	rows := []common.DataRow{
		{"id": "1", "name": "alpha"},
		{"id": "2", "name": "beta"},
		{"id": "3", "name": "gamma"},
	}

	arrowBytes, err := arrowIPCFromRows(columns, rows)
	if err != nil {
		t.Fatal(err)
	}
	ar, err := NewArrowBatchReader(bytes.NewReader(arrowBytes), columns)
	if err != nil {
		t.Fatal(err)
	}
	viaArrow := drainBatches(t, ar)

	var nd bytes.Buffer
	for _, r := range rows {
		nd.WriteString(`{"id":"` + r["id"].(string) + `","name":"` + r["name"].(string) + `"}` + "\n")
	}
	viaND := drainBatches(t, NewNDJSONBatchReader(bytes.NewReader(nd.Bytes()), columns, 0))

	if len(viaArrow) != len(viaND) {
		t.Fatalf("row counts differ: arrow=%d ndjson=%d", len(viaArrow), len(viaND))
	}
	for i := range viaArrow {
		for _, c := range columns {
			if viaArrow[i][c] != viaND[i][c] {
				t.Errorf("row %d column %q differs: arrow=%#v ndjson=%#v", i, c, viaArrow[i][c], viaND[i][c])
			}
		}
	}
}

// An empty stream reports EOF immediately, like its NDJSON counterpart's
// "[]" sentinel does -- a consumer loop must terminate either way.
func TestArrowBatchReaderEmptyStreamIsEOF(t *testing.T) {
	empty, err := arrowIPCFromRows([]string{"a"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewArrowBatchReader(bytes.NewReader(empty), []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Next(); err != io.EOF {
		t.Fatalf("Next on an empty stream = %v, want io.EOF", err)
	}
}

// The ref's declared column order wins over the file's, matching
// NewNDJSONBatchReader -- a ref is the authority on how its own rows are
// keyed.
func TestArrowBatchReaderUsesSchemaOrderWhenCallerGivesNone(t *testing.T) {
	arrowBytes, err := arrowIPCFromRows([]string{"z", "a"}, []common.DataRow{{"z": "1", "a": "2"}})
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewArrowBatchReader(bytes.NewReader(arrowBytes), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(r.Columns(), ","); got != "z,a" {
		t.Errorf("columns = %q, want the schema's own order z,a", got)
	}
}

func TestArrowBatchReaderRejectsGarbage(t *testing.T) {
	if _, err := NewArrowBatchReader(bytes.NewReader([]byte("not arrow at all")), nil); err == nil {
		t.Fatal("a non-arrow stream was accepted")
	}
}
