package engine

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// A truncated or corrupted blob used to decode as a shorter dataset with
// a nil error, so a resumed node restored half its own output and the run
// was marked successful. The streaming reader was made loud for exactly
// this reason (TestNDJSONBatchReader_MalformedInputErrsLoudly); these are
// the materialising readers, which were not.

func TestDecodeNDJSONFailsOnATruncatedBlob(t *testing.T) {
	// Two good rows and a third cut off mid-write, which is what a
	// killed writer or a partial upload actually leaves behind.
	input := "{\"a\":1}\n{\"a\":2}\n{\"a\":"

	ds, err := DecodeNDJSON(strings.NewReader(input), []string{"a"})
	if err == nil {
		t.Fatalf("a truncated blob decoded as %d rows and reported success", len(ds.Rows))
	}
	// The ordinal is the point: it says which row was cut off.
	if !strings.Contains(err.Error(), "row 3") {
		t.Errorf("error %q does not name the row it stopped at", err.Error())
	}
}

func TestDecodeNDJSONFailsOnBinaryMasqueradingAsNDJSON(t *testing.T) {
	// An Arrow IPC stream read by the NDJSON decoder. This returned zero
	// rows and no error, which is how an arrow blob stored in a text
	// column read back as an empty dataset.
	var buf bytes.Buffer
	if err := EncodeArrowIPC(&buf, smallTypedDataset()); err != nil {
		t.Fatalf("encode arrow: %v", err)
	}
	if _, err := DecodeNDJSON(bytes.NewReader(buf.Bytes()), nil); err == nil {
		t.Fatal("arrow bytes decoded as ndjson and reported success")
	}
}

func TestDecodeNDJSONStillAcceptsWhatItShould(t *testing.T) {
	// Both directions: the guard must not have made valid input fail.
	for _, tc := range []struct {
		name  string
		input string
		rows  int
	}{
		{"the empty sentinel", "[]", 0},
		{"nothing at all", "", 0},
		{"one row", "{\"a\":1}\n", 1},
		{"no trailing newline", "{\"a\":1}\n{\"a\":2}", 2},
		{"blank lines between rows", "{\"a\":1}\n\n\n{\"a\":2}\n", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ds, err := DecodeNDJSON(strings.NewReader(tc.input), []string{"a"})
			if err != nil {
				t.Fatalf("valid input rejected: %v", err)
			}
			if len(ds.Rows) != tc.rows {
				t.Errorf("got %d rows, want %d", len(ds.Rows), tc.rows)
			}
		})
	}
}

func TestReadNDJSONFailsOnATruncatedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.ndjson")
	if err := os.WriteFile(path, []byte("{\"a\":1}\n{\"a\":"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadNDJSON(path); err == nil {
		t.Fatal("a truncated file was read as a complete dataset")
	}
}

func TestReadColumnarBinaryFailsOnACorruptRowBody(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.brok")
	if err := WriteColumnarBinary(path, smallTypedDataset()); err != nil {
		t.Fatalf("write: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Truncate the row body, leaving the magic and schema header intact,
	// so only the row loop can catch it.
	if err := os.WriteFile(path, raw[:len(raw)-8], 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadColumnarBinary(path); err == nil {
		t.Fatal("a file with a truncated row body was read as complete")
	}
}

func smallTypedDataset() *common.DataSet {
	return &common.DataSet{
		Columns: []string{"id", "name"},
		Rows: []common.DataRow{
			{"id": int64(1), "name": "alice"},
			{"id": int64(2), "name": "bob"},
		},
	}
}
