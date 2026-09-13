package engine

import (
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/artifact"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// An Arrow-format ref written through WriteArtifactRef and read back.
// spill() has produced Arrow refs since #521, so this is the ordinary
// path for any uniformly typed output, not an exotic one.
func TestSQLArtifactStore_ArrowRefRoundTrips(t *testing.T) {
	s := newSQLArtifactTestStore(t)

	ds := &common.DataSet{
		Columns: []string{"id", "name"},
		Rows: []common.DataRow{
			{"id": int64(1), "name": "alice"},
			{"id": int64(2), "name": "bob"},
		},
	}
	outputs := newNodeOutputs(s.Blobs(), "run-arrow", 1)
	ref, err := outputs.spill(ds)
	if err != nil {
		t.Fatalf("spill: %v", err)
	}
	if ref.Format != artifact.FormatArrowIPC {
		t.Fatalf("precondition: spill chose %q, wanted arrow", ref.Format)
	}

	if err := s.WriteArtifactRef("run-arrow", "n1", "", ref); err != nil {
		t.Fatalf("WriteArtifactRef: %v", err)
	}
	got, err := s.ReadArtifact("run-arrow", "n1", "")
	if err != nil {
		t.Fatalf("ReadArtifact: %v", err)
	}
	if len(got.Rows) != 2 {
		t.Fatalf("read back %d rows, want 2: %+v", len(got.Rows), got.Rows)
	}
	if got.Rows[0]["id"] != int64(1) || got.Rows[1]["name"] != "bob" {
		t.Errorf("rows came back wrong: %+v", got.Rows)
	}
}

// The local-disk store keeps a zero-copy manifest over the blob, so the
// format survives the write and only the read had to learn about it.
// This refused anything but NDJSON, which meant a run whose output was
// uniformly typed could not be replayed from its own artifact.
func TestLocalDiskArtifactStore_ArrowRefRoundTrips(t *testing.T) {
	dir := t.TempDir()
	s := NewLocalDiskArtifactStore(dir)

	ds := &common.DataSet{
		Columns: []string{"id", "name"},
		Rows: []common.DataRow{
			{"id": int64(10), "name": "alice"},
			{"id": int64(20), "name": "bob"},
		},
	}
	outputs := newNodeOutputs(s.Blobs(), "run-arrow", 1)
	ref, err := outputs.spill(ds)
	if err != nil {
		t.Fatalf("spill: %v", err)
	}
	if ref.Format != artifact.FormatArrowIPC {
		t.Fatalf("precondition: spill chose %q, wanted arrow", ref.Format)
	}

	if err := s.WriteArtifactRef("run-arrow", "n1", "", ref); err != nil {
		t.Fatalf("WriteArtifactRef: %v", err)
	}
	got, err := s.ReadArtifact("run-arrow", "n1", "")
	if err != nil {
		t.Fatalf("ReadArtifact: %v", err)
	}
	if len(got.Rows) != 2 {
		t.Fatalf("read back %d rows, want 2: %+v", len(got.Rows), got.Rows)
	}
	if got.Rows[0]["id"] != int64(10) || got.Rows[1]["name"] != "bob" {
		t.Errorf("rows came back wrong: %+v", got.Rows)
	}
}

// NDJSON refs must keep taking the byte-for-byte path: converting one
// would be a pointless decode/re-encode of every ordinary artifact.
func TestSQLArtifactStore_NDJSONRefIsStoredVerbatim(t *testing.T) {
	s := newSQLArtifactTestStore(t)

	// A column with mixed types, which is what disqualifies a dataset
	// from Arrow, so spill picks NDJSON.
	ds := &common.DataSet{
		Columns: []string{"v"},
		Rows:    []common.DataRow{{"v": "a"}, {"v": int64(2)}},
	}
	outputs := newNodeOutputs(s.Blobs(), "run-nd", 1)
	ref, err := outputs.spill(ds)
	if err != nil {
		t.Fatalf("spill: %v", err)
	}
	if ref.Format != artifact.FormatNDJSON {
		t.Fatalf("precondition: spill chose %q, wanted ndjson", ref.Format)
	}
	if err := s.WriteArtifactRef("run-nd", "n1", "", ref); err != nil {
		t.Fatalf("WriteArtifactRef: %v", err)
	}
	got, err := s.ReadArtifact("run-nd", "n1", "")
	if err != nil {
		t.Fatalf("ReadArtifact: %v", err)
	}
	if len(got.Rows) != 2 || got.Rows[0]["v"] != "a" || got.Rows[1]["v"] != int64(2) {
		t.Errorf("rows came back wrong: %+v", got.Rows)
	}
}
