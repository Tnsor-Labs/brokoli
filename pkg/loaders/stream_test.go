package loaders

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

func writeCSV(t *testing.T, name string, rows [][]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := csv.NewWriter(f)
	for _, r := range rows {
		if err := w.Write(r); err != nil {
			t.Fatal(err)
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func collect(t *testing.T, path string, batchSize int) *common.DataSet {
	t.Helper()
	out := &common.DataSet{}
	cols, total, err := StreamBatches(context.Background(), path, batchSize, func(b *common.DataSet) error {
		out.Rows = append(out.Rows, b.Rows...)
		return nil
	})
	if err != nil {
		t.Fatalf("StreamBatches: %v", err)
	}
	if int(total) != len(out.Rows) {
		t.Errorf("reported %d rows, emitted %d", total, len(out.Rows))
	}
	out.Columns = cols
	return out
}

// The only reason to choose between StreamBatches and Load is memory. If they
// disagree on the data, that choice becomes a correctness decision made by
// whoever set a threshold, which is not a decision anyone can make well.
// Batch size is varied because a boundary landing exactly on the row count,
// or on a single row, is where an off-by-one lives.
func TestStreamBatchesMatchesLoad(t *testing.T) {
	rows := [][]string{{"id", " name ", "email", "note"}}
	for i := 0; i < 250; i++ {
		note := "a note"
		if i%7 == 0 {
			note = "" // empty fields must become nil, as Load does
		}
		rows = append(rows, []string{
			fmt.Sprint(i), fmt.Sprintf("user %d", i), fmt.Sprintf("u%d@example.com", i), note,
		})
	}
	path := writeCSV(t, "equiv.csv", rows)

	want, err := (&CSVLoader{}).Load(path)
	if err != nil {
		t.Fatal(err)
	}

	for _, size := range []int{1, 2, 7, 249, 250, 251, 1000, 0} {
		t.Run(fmt.Sprintf("batch=%d", size), func(t *testing.T) {
			got := collect(t, path, size)
			if !reflect.DeepEqual(got.Columns, want.Columns) {
				t.Fatalf("columns differ:\n got %v\nwant %v", got.Columns, want.Columns)
			}
			if len(got.Rows) != len(want.Rows) {
				t.Fatalf("row count: got %d, want %d", len(got.Rows), len(want.Rows))
			}
			for i := range want.Rows {
				if !reflect.DeepEqual(got.Rows[i], want.Rows[i]) {
					t.Fatalf("row %d differs:\n got %#v\nwant %#v", i, got.Rows[i], want.Rows[i])
				}
			}
		})
	}
}

// An empty field is NULL, not "". The streaming loader that this replaces got
// this wrong -- it stored the empty string -- so a pipeline would have
// produced different data depending on whether streaming happened to engage.
func TestStreamBatchesEmptyFieldIsNil(t *testing.T) {
	path := writeCSV(t, "empty.csv", [][]string{
		{"a", "b"},
		{"", "set"},
	})
	ds := collect(t, path, 0)
	v, ok := ds.Rows[0]["a"]
	if !ok {
		t.Fatal("column a missing from the row")
	}
	if v != nil {
		t.Errorf("empty field = %#v, want nil", v)
	}
	if ds.Rows[0]["b"] != "set" {
		t.Errorf("b = %#v, want \"set\"", ds.Rows[0]["b"])
	}
}

// A truncated or malformed file must fail the read. The replaced loader
// signalled errors by sending a nil row and dropping the error, so a
// half-written file was indistinguishable from a complete one: the run wrote
// a partial result and reported success.
func TestStreamBatchesReportsMalformedRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.csv")
	// Row 2 has an unterminated quote, which csv.Reader rejects.
	if err := os.WriteFile(path, []byte("a,b\n1,2\n\"unterminated,3\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	emitted := 0
	_, _, err := StreamBatches(context.Background(), path, 1, func(b *common.DataSet) error {
		emitted += len(b.Rows)
		return nil
	})
	if err == nil {
		t.Fatal("a malformed CSV was read as if it were complete")
	}
	if !strings.Contains(err.Error(), "failed to read CSV data") {
		t.Errorf("error does not say what went wrong: %v", err)
	}
}

// A cancelled run must stop reading promptly rather than at the next batch
// boundary, which for a large file and a large batch is most of the file.
func TestStreamBatchesHonoursCancellation(t *testing.T) {
	rows := [][]string{{"id"}}
	for i := 0; i < 5000; i++ {
		rows = append(rows, []string{fmt.Sprint(i)})
	}
	path := writeCSV(t, "cancel.csv", rows)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := StreamBatches(ctx, path, 100, func(b *common.DataSet) error {
		t.Error("emit was called after the context was already cancelled")
		return nil
	})
	if err == nil {
		t.Fatal("expected the cancelled context to stop the read")
	}
}

// An error from the consumer stops the read; it must not be swallowed and it
// must not keep reading the rest of the file.
func TestStreamBatchesPropagatesEmitError(t *testing.T) {
	rows := [][]string{{"id"}}
	for i := 0; i < 100; i++ {
		rows = append(rows, []string{fmt.Sprint(i)})
	}
	path := writeCSV(t, "emiterr.csv", rows)

	sentinel := fmt.Errorf("consumer said stop")
	calls := 0
	_, _, err := StreamBatches(context.Background(), path, 10, func(b *common.DataSet) error {
		calls++
		return sentinel
	})
	if err == nil || !strings.Contains(err.Error(), "consumer said stop") {
		t.Fatalf("emit error was not propagated: %v", err)
	}
	if calls != 1 {
		t.Errorf("kept reading after the consumer failed: emit called %d times", calls)
	}
}

func TestSupportsStreaming(t *testing.T) {
	for path, want := range map[string]bool{
		"/data/x.csv": true, "/data/x.CSV": true,
		"/data/x.json": false, "/data/x.xlsx": false, "/data/x.xml": false, "/data/x": false,
	} {
		if got := SupportsStreaming(path); got != want {
			t.Errorf("SupportsStreaming(%q) = %v, want %v", path, got, want)
		}
	}
	if _, _, err := StreamBatches(context.Background(), "/data/x.json", 0, nil); err == nil {
		t.Error("streaming a JSON file should be refused, not attempted")
	}
}
