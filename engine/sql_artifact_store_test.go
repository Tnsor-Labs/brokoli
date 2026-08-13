package engine

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/store"
)

// TestSQLArtifactStore_VisibleAcrossSeparateConnections is the direct
// regression test for core#161: LocalDiskArtifactStore ties an artifact to
// whichever single filesystem WriteArtifact happened to run on, which is
// exactly what makes it invisible to a different pod's dispatcher in a
// real distributed deployment. This proves the opposite for
// SQLArtifactStore — write through one *sql.DB connection, read back
// through a genuinely separate one against the same database, simulating
// two different pods sharing one Postgres/SQLite instance rather than two
// different local disks.
func TestSQLArtifactStore_VisibleAcrossSeparateConnections(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "artifacts.db")

	writerStore, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore (writer): %v", err)
	}
	t.Cleanup(func() { _ = writerStore.Close() })
	writerDB, ok := writerStore.RawDB().(*sql.DB)
	if !ok {
		t.Fatal("RawDB() did not return *sql.DB")
	}
	writer, err := NewSQLArtifactStore(writerDB, "sqlite", t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLArtifactStore (writer): %v", err)
	}

	ds := &common.DataSet{
		Columns: []string{"hostname", "value"},
		Rows: []common.DataRow{
			{"hostname": "worker-a", "value": float64(1)},
			{"hostname": "worker-a", "value": float64(2)},
		},
	}
	if err := writer.WriteArtifact("run-1", "expand", "idx:0", ds); err != nil {
		t.Fatalf("WriteArtifact: %v", err)
	}

	// A genuinely separate connection — a different *sql.DB, opened fresh
	// against the same file — not a second handle sharing the writer's own
	// in-process connection pool. This is the property that matters: the
	// bug this store fixes is specifically about visibility ACROSS
	// separate connections/processes, not within one.
	readerStore, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore (reader): %v", err)
	}
	t.Cleanup(func() { _ = readerStore.Close() })
	readerDB, ok := readerStore.RawDB().(*sql.DB)
	if !ok {
		t.Fatal("RawDB() did not return *sql.DB")
	}
	reader, err := NewSQLArtifactStore(readerDB, "sqlite", t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLArtifactStore (reader): %v", err)
	}

	got, err := reader.ReadArtifact("run-1", "expand", "idx:0")
	if err != nil {
		t.Fatalf("ReadArtifact from a separate connection: %v", err)
	}
	if len(got.Columns) != 2 || got.Columns[0] != "hostname" || got.Columns[1] != "value" {
		t.Fatalf("columns = %v, want [hostname value]", got.Columns)
	}
	if len(got.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(got.Rows))
	}
	if got.Rows[0]["hostname"] != "worker-a" || got.Rows[1]["value"] != float64(2) {
		t.Fatalf("row content mismatch: %+v", got.Rows)
	}
}

// TestSQLArtifactStore_ReadMissingReturnsErrArtifactNotFound matches
// LocalDiskArtifactStore's contract exactly: a lookup for a key nothing
// ever wrote must return the shared sentinel error, not a generic one —
// Runner.restoreSkippedNodeOutput distinguishes on it specifically.
func TestSQLArtifactStore_ReadMissingReturnsErrArtifactNotFound(t *testing.T) {
	s := newSQLArtifactTestStore(t)
	_, err := s.ReadArtifact("no-such-run", "no-such-node", "")
	if err == nil {
		t.Fatal("expected an error for a never-written key")
	}
	if !errors.Is(err, ErrArtifactNotFound) {
		t.Fatalf("error = %v, want ErrArtifactNotFound", err)
	}
}

// TestSQLArtifactStore_WriteArtifactOverwritesSameKey proves a second
// WriteArtifact for the same (run, node, instance) key replaces the first
// rather than erroring or leaving stale data behind — the same
// idempotent-overwrite behavior a node retry needs from any ArtifactStore
// implementation.
func TestSQLArtifactStore_WriteArtifactOverwritesSameKey(t *testing.T) {
	s := newSQLArtifactTestStore(t)
	first := &common.DataSet{Columns: []string{"n"}, Rows: []common.DataRow{{"n": float64(1)}}}
	if err := s.WriteArtifact("run-1", "node-1", "", first); err != nil {
		t.Fatalf("first WriteArtifact: %v", err)
	}
	second := &common.DataSet{Columns: []string{"n"}, Rows: []common.DataRow{{"n": float64(2)}, {"n": float64(3)}}}
	if err := s.WriteArtifact("run-1", "node-1", "", second); err != nil {
		t.Fatalf("second WriteArtifact: %v", err)
	}
	got, err := s.ReadArtifact("run-1", "node-1", "")
	if err != nil {
		t.Fatalf("ReadArtifact: %v", err)
	}
	if len(got.Rows) != 2 {
		t.Fatalf("rows = %d, want 2 (the second write's content, not the first's)", len(got.Rows))
	}
}

// TestSQLArtifactStore_DeleteRunArtifactsRemovesAllInstances proves
// DeleteRunArtifacts clears every node/instance key belonging to a run,
// not just one, matching LocalDiskArtifactStore's whole-directory-removal
// contract.
func TestSQLArtifactStore_DeleteRunArtifactsRemovesAllInstances(t *testing.T) {
	s := newSQLArtifactTestStore(t)
	ds := &common.DataSet{Columns: []string{"n"}, Rows: []common.DataRow{{"n": float64(1)}}}
	if err := s.WriteArtifact("run-1", "node-a", "idx:0", ds); err != nil {
		t.Fatalf("WriteArtifact node-a: %v", err)
	}
	if err := s.WriteArtifact("run-1", "node-b", "idx:1", ds); err != nil {
		t.Fatalf("WriteArtifact node-b: %v", err)
	}
	if err := s.WriteArtifact("run-2", "node-a", "idx:0", ds); err != nil {
		t.Fatalf("WriteArtifact (other run): %v", err)
	}

	if err := s.DeleteRunArtifacts("run-1"); err != nil {
		t.Fatalf("DeleteRunArtifacts: %v", err)
	}

	if _, err := s.ReadArtifact("run-1", "node-a", "idx:0"); !errors.Is(err, ErrArtifactNotFound) {
		t.Fatalf("run-1/node-a still readable after delete: err=%v", err)
	}
	if _, err := s.ReadArtifact("run-1", "node-b", "idx:1"); !errors.Is(err, ErrArtifactNotFound) {
		t.Fatalf("run-1/node-b still readable after delete: err=%v", err)
	}
	if _, err := s.ReadArtifact("run-2", "node-a", "idx:0"); err != nil {
		t.Fatalf("run-2's artifact was wrongly deleted along with run-1's: %v", err)
	}
}

func newSQLArtifactTestStore(t *testing.T) *SQLArtifactStore {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "artifacts.db")
	st, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	db, ok := st.RawDB().(*sql.DB)
	if !ok {
		t.Fatal("RawDB() did not return *sql.DB")
	}
	s, err := NewSQLArtifactStore(db, "sqlite", t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLArtifactStore: %v", err)
	}
	return s
}
