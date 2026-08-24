package engine

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

func sinkFixture() *common.DataSet {
	return &common.DataSet{
		Columns: []string{"id", "name", "amount", "note", "flag"},
		Rows: []common.DataRow{
			{"id": float64(1), "name": "Ada", "amount": float64(12.5), "note": "plain", "flag": true},
			{"id": float64(2), "name": "O'Brien, Bob", "amount": float64(100), "note": "has, comma", "flag": false},
			{"id": float64(3), "name": "line\nbreak", "amount": float64(0.125), "note": `quote"inside`, "flag": nil},
			{"id": float64(4), "name": "", "amount": nil, "note": "unicode ✓ 中文", "flag": true},
			{"id": float64(5), "name": "tab\there", "amount": float64(-3), "note": nil, "flag": false},
		},
	}
}

// batchesOf turns a dataset into a puller that yields n rows at a time, so
// a test can vary where the batch boundaries fall.
func batchesOf(ds *common.DataSet, n int) func() (*common.DataSet, error) {
	i := 0
	return func() (*common.DataSet, error) {
		if i >= len(ds.Rows) {
			return nil, io.EOF
		}
		end := i + n
		if end > len(ds.Rows) {
			end = len(ds.Rows)
		}
		b := &common.DataSet{Columns: ds.Columns, Rows: ds.Rows[i:end]}
		i = end
		return b, nil
	}
}

// The streamed writer must produce exactly what the buffered one produces.
// A sink whose format shifts when the planner picks a different path is
// worse than a slow one.
func TestStreamedSinkFileMatchesBufferedBytes(t *testing.T) {
	ds := sinkFixture()
	r := &Runner{}

	wantCSV, err := r.marshalCSV(ds)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := json.MarshalIndent(ds.Rows, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	// Batch size 1 and 2 do not divide 5 rows evenly, so boundaries land
	// mid-file rather than only at the end.
	for _, size := range []int{1, 2, 3, 5, 100} {
		for _, tc := range []struct {
			format string
			want   []byte
		}{{"csv", wantCSV}, {"json", wantJSON}} {
			path := filepath.Join(t.TempDir(), "out."+tc.format)
			rows, written, err := writeSinkFileStreamed(path, tc.format, ds.Columns, batchesOf(ds, size))
			if err != nil {
				t.Fatalf("%s batch=%d: %v", tc.format, size, err)
			}
			if rows != int64(len(ds.Rows)) {
				t.Errorf("%s batch=%d: wrote %d rows, want %d", tc.format, size, rows, len(ds.Rows))
			}
			got, err := os.ReadFile(path) //nolint:gosec // test temp file
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(tc.want) {
				t.Errorf("%s batch=%d bytes differ:\n got %q\nwant %q", tc.format, size, got, tc.want)
			}
			if written != int64(len(got)) {
				t.Errorf("%s batch=%d: reported %d bytes, file has %d", tc.format, size, written, len(got))
			}
		}
	}
}

// An empty input must produce the same file the buffered path produces,
// not a truncated or absent one.
func TestStreamedSinkFileEmptyInput(t *testing.T) {
	empty := &common.DataSet{Columns: []string{"a", "b"}, Rows: nil}
	r := &Runner{}
	wantCSV, err := r.marshalCSV(empty)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := json.MarshalIndent([]common.DataRow{}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		format string
		want   []byte
	}{{"csv", wantCSV}, {"json", wantJSON}} {
		path := filepath.Join(t.TempDir(), "empty."+tc.format)
		rows, _, err := writeSinkFileStreamed(path, tc.format, empty.Columns, batchesOf(empty, 10))
		if err != nil {
			t.Fatalf("%s: %v", tc.format, err)
		}
		if rows != 0 {
			t.Errorf("%s: wrote %d rows, want 0", tc.format, rows)
		}
		got, err := os.ReadFile(path) //nolint:gosec // test temp file
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(tc.want) {
			t.Errorf("%s: got %q, want %q", tc.format, got, tc.want)
		}
	}
}

func TestSinkFileFormatResolution(t *testing.T) {
	cases := []struct {
		cfg  map[string]interface{}
		want string
	}{
		{map[string]interface{}{"path": "/tmp/a.csv"}, "csv"},
		{map[string]interface{}{"path": "/tmp/a.tsv"}, "csv"},
		{map[string]interface{}{"path": "/tmp/a.sql"}, "sql"},
		{map[string]interface{}{"path": "/tmp/a.json"}, "json"},
		{map[string]interface{}{"path": "/tmp/a.txt"}, "json"},
		// An explicit format wins over the extension.
		{map[string]interface{}{"path": "/tmp/a.csv", "format": "json"}, "json"},
	}
	for _, c := range cases {
		if got := sinkFileFormat(models.Node{Config: c.cfg}); got != c.want {
			t.Errorf("%v: got %q, want %q", c.cfg, got, c.want)
		}
	}
	if sinkFileFormatStreams("sql") {
		t.Error("sql must not claim to stream: it reads one cell from the first row")
	}
	for _, f := range []string{"csv", "json"} {
		if !sinkFileFormatStreams(f) {
			t.Errorf("%s should stream", f)
		}
	}
}
