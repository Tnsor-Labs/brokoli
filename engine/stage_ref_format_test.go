package engine

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/artifact"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// stageRefToNDJSONFile copied a referenced dataset byte-for-byte into a
// file named .ndjson and handed the path to a code-node wrapper. That
// was correct only while every spilled dataset was NDJSON.
//
// nodeOutputs.spill writes Arrow whenever arrowEncodableSchema accepts
// the dataset, and #560 widened that to integer columns, so ordinary
// tables started spilling as Arrow. The wrapper then read Arrow IPC
// bytes as NDJSON, which node_output_store.go's own comment describes:
// "returns ZERO ROWS, which is the silent data loss".
//
// No test covered the function at all before this one.

func stagedRows(t *testing.T, path string) []common.DataRow {
	t.Helper()
	f, err := os.Open(path) // #nosec G304 -- test-controlled temp path.
	if err != nil {
		t.Fatalf("open staged file: %v", err)
	}
	defer func() { _ = f.Close() }()

	var rows []common.DataRow
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		// The empty-dataset sentinel EncodeNDJSON writes.
		if string(line) == "[]" {
			return nil
		}
		var row common.DataRow
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatalf("staged line is not a JSON object: %q: %v", line, err)
		}
		rows = append(rows, row)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan staged file: %v", err)
	}
	return rows
}

// An integer column is what makes spill choose Arrow, and an integer id
// is what essentially every real table has.
func datasetWithIntegerColumn(n int) *common.DataSet {
	ds := &common.DataSet{Columns: []string{"id", "name"}}
	for i := 0; i < n; i++ {
		ds.Rows = append(ds.Rows, common.DataRow{
			"id":   int64(i),
			"name": "row",
		})
	}
	return ds
}

func TestStageRefToNDJSONFileConvertsArrow(t *testing.T) {
	blobs := artifact.NewLocalDiskStore(t.TempDir())
	outputs := newNodeOutputs(blobs, "run-stage", 1) // threshold 1: always spill

	ds := datasetWithIntegerColumn(120)
	ref, err := outputs.spill(ds)
	if err != nil {
		t.Fatalf("spill: %v", err)
	}
	// The precondition this test exists for. If spill stops choosing
	// Arrow here the test is no longer exercising the bug, and should
	// fail loudly rather than pass for the wrong reason.
	if ref.Format != artifact.FormatArrowIPC {
		t.Fatalf("spill chose %q, want %q -- this test needs an Arrow ref to mean anything",
			ref.Format, artifact.FormatArrowIPC)
	}

	path, err := stageRefToNDJSONFile(outputs, ref)
	if err != nil {
		t.Fatalf("stageRefToNDJSONFile: %v", err)
	}
	defer func() { _ = os.Remove(path) }()

	rows := stagedRows(t, path)
	if len(rows) != len(ds.Rows) {
		t.Fatalf("staged %d rows, want %d -- the wrapper would see this many", len(rows), len(ds.Rows))
	}
	// 64-bit fidelity survives the conversion, same as everywhere else.
	got, ok := rows[7]["id"].(float64)
	if !ok {
		t.Fatalf("row 7 id is %T, want a JSON number", rows[7]["id"])
	}
	if int64(got) != 7 {
		t.Errorf("row 7 id = %v, want 7", got)
	}
	if rows[7]["name"] != "row" {
		t.Errorf("row 7 name = %v", rows[7]["name"])
	}
}

// The NDJSON case must stay a plain copy: no decode, no re-encode, and
// byte-identical output, since that is the overwhelmingly common path.
func TestStageRefToNDJSONFileCopiesNDJSONUnchanged(t *testing.T) {
	blobs := artifact.NewLocalDiskStore(t.TempDir())
	outputs := newNodeOutputs(blobs, "run-stage-ndjson", 1)

	// A nested value makes arrowEncodableSchema decline, so this spills
	// as NDJSON.
	ds := &common.DataSet{
		Columns: []string{"id", "meta"},
		Rows: []common.DataRow{
			{"id": int64(1), "meta": map[string]interface{}{"a": "b"}},
			{"id": int64(2), "meta": map[string]interface{}{"a": "c"}},
		},
	}
	ref, err := outputs.spill(ds)
	if err != nil {
		t.Fatalf("spill: %v", err)
	}
	if ref.Format != artifact.FormatNDJSON {
		t.Fatalf("spill chose %q, want ndjson for a nested value", ref.Format)
	}

	path, err := stageRefToNDJSONFile(outputs, ref)
	if err != nil {
		t.Fatalf("stageRefToNDJSONFile: %v", err)
	}
	defer func() { _ = os.Remove(path) }()

	if rows := stagedRows(t, path); len(rows) != 2 {
		t.Fatalf("staged %d rows, want 2", len(rows))
	}
}

// An empty dataset still has to produce something the wrapper can read.
func TestStageRefToNDJSONFileHandlesEmpty(t *testing.T) {
	blobs := artifact.NewLocalDiskStore(t.TempDir())
	outputs := newNodeOutputs(blobs, "run-stage-empty", 1)

	ref, err := outputs.spill(&common.DataSet{Columns: []string{"id"}})
	if err != nil {
		t.Fatalf("spill: %v", err)
	}
	path, err := stageRefToNDJSONFile(outputs, ref)
	if err != nil {
		t.Fatalf("stageRefToNDJSONFile: %v", err)
	}
	defer func() { _ = os.Remove(path) }()
	if rows := stagedRows(t, path); len(rows) != 0 {
		t.Errorf("staged %d rows for an empty dataset", len(rows))
	}
}
