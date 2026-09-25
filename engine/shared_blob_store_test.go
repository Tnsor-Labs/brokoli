package engine

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// ADR-038 separates two stores that were one. Blobs() is per-pod scratch
// and SharedBlobs() is what another process can read. Conflating them
// meant a staged task input was written to one pod's disk and fetched
// from another, which answered 404 after a capability had already been
// minted and a work order dispatched (#572).

func sqlArtifactStoreForTest(t *testing.T) *SQLArtifactStore {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s, err := NewSQLArtifactStore(db, "sqlite", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// The default distributed configuration has no shared store, and must
// say so rather than hand back the per-pod one.
func TestSQLArtifactStoreHasNoSharedBlobsByDefault(t *testing.T) {
	s := sqlArtifactStoreForTest(t)
	if s.Blobs() == nil {
		t.Error("Blobs() is nil; spill would be disabled")
	}
	if s.SharedBlobs() != nil {
		t.Error("SharedBlobs() returned a store with none configured; " +
			"that is the per-pod store leaking into the cross-pod path")
	}
}

// Single node: one process writes and reads, so its local disk is a
// legitimate shared store. This is why the distinction is per-store
// rather than a global flag.
func TestLocalDiskArtifactStoreSharesItsBlobs(t *testing.T) {
	l := NewLocalDiskArtifactStore(t.TempDir())
	if l.SharedBlobs() == nil {
		t.Error("a single-node store should offer its blobs as shared")
	}
	if l.SharedBlobs() != l.Blobs() {
		t.Error("single-node should be the same store, not a second one")
	}
}

// The refusal is the point. Without a shared store the run fails at
// staging with a message naming what is missing, instead of succeeding
// and 404ing the worker later.
func TestStagingRefusesWithoutASharedStore(t *testing.T) {
	r := &Runner{
		artifactStore: sqlArtifactStoreForTest(t),
		run:           &models.Run{ID: "run-1"},
	}
	input := &common.DataSet{Columns: []string{"id"}}
	for i := 0; i < maxInlineTaskInputRows+1; i++ {
		input.Rows = append(input.Rows, common.DataRow{"id": int64(i)})
	}

	_, _, _, err := r.stageTaskInputByReference(
		models.Node{ID: "t_py"}, input, 0, 1)
	if err == nil {
		t.Fatal("staged an input with no shared store; the worker would get a 404")
	}
	// It has to say what to do, because the failure it replaces was a
	// 404 from a component that had done nothing wrong.
	for _, want := range []string{"t_py", "shared blob store", "BROKOLI_BLOB_S3_BUCKET"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
}
