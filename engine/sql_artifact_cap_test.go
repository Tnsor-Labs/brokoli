package engine

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
	_ "modernc.org/sqlite"
)

func capTestStore(t *testing.T) *SQLArtifactStore {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s, err := NewSQLArtifactStore(db, "sqlite", filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func wideDataSet(rows int) *common.DataSet {
	pad := strings.Repeat("x", 500)
	ds := &common.DataSet{Columns: []string{"id", "padding"}}
	for i := 0; i < rows; i++ {
		ds.Rows = append(ds.Rows, common.DataRow{"id": float64(i), "padding": pad})
	}
	return ds
}

// An artifact here is a single column value, so a large one is held whole
// in memory and sent as one statement parameter. Past the cap the store
// declines instead of trying — which is what stopped a 100 MB write from
// OOM-killing both the worker and a Postgres backend.
func TestWriteArtifactDeclinesOversized(t *testing.T) {
	s := capTestStore(t)
	t.Setenv("BROKOLI_SQL_ARTIFACT_MAX_BYTES", "100000") // 100 KB

	err := s.WriteArtifact("run-1", "big_node", "", wideDataSet(1000)) // ~0.5 MB
	if err == nil {
		t.Fatal("expected the oversized artifact to be declined")
	}
	for _, want := range []string{"big_node", "resume artifact", "BROKOLI_SQL_ARTIFACT_MAX_BYTES"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q: %v", want, err)
		}
	}

	// Under the cap it still writes, and reads back intact.
	small := wideDataSet(10)
	if err := s.WriteArtifact("run-1", "small_node", "", small); err != nil {
		t.Fatalf("a small artifact should still be written: %v", err)
	}
	got, err := s.ReadArtifact("run-1", "small_node", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rows) != len(small.Rows) {
		t.Fatalf("read back %d rows, want %d", len(got.Rows), len(small.Rows))
	}
}

// The ref path must decline before opening the blob: SizeBytes already
// says how big it is, so an oversized artifact costs nothing to refuse.
func TestWriteArtifactRefDeclinesOversizedWithoutReading(t *testing.T) {
	s := capTestStore(t)
	t.Setenv("BROKOLI_SQL_ARTIFACT_MAX_BYTES", "100000")

	outputs := newNodeOutputs(s.Blobs(), "run-1", 1)
	ref, err := outputs.PutStream(func(emit func(*common.DataSet) error) error {
		return emit(wideDataSet(1000))
	}, func() []string { return []string{"id", "padding"} })
	if err != nil {
		t.Fatal(err)
	}
	if ref.SizeBytes <= 100000 {
		t.Fatalf("fixture too small to exercise the cap: %d bytes", ref.SizeBytes)
	}

	err = s.WriteArtifactRef("run-1", "big_node", "", ref)
	if err == nil {
		t.Fatal("expected the oversized ref to be declined")
	}
	if !strings.Contains(err.Error(), "big_node") {
		t.Errorf("error should name the node: %v", err)
	}
}

// Zero removes the cap, for operators who would rather take the memory
// cost than lose resumability.
func TestArtifactCapCanBeDisabled(t *testing.T) {
	s := capTestStore(t)
	t.Setenv("BROKOLI_SQL_ARTIFACT_MAX_BYTES", "0")
	if err := s.WriteArtifact("run-1", "big_node", "", wideDataSet(1000)); err != nil {
		t.Fatalf("cap disabled, the write should proceed: %v", err)
	}
}
