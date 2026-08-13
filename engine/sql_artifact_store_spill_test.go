package engine

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/artifact"
	"github.com/Tnsor-Labs/brokoli/store"
)

// TestPipeline_SQLArtifactStore_SpillsWithoutChangingResults is the direct
// regression test for the gap found live implementing
// docs/adr/018-chunked-execution-and-backpressure.md: before
// SQLArtifactStore.Blobs() existed, r.artifactStore.(BlobStoreProvider)
// failed for any deployment using SQLArtifactStore (every instance-dispatch
// deployment, cmd/serve.go), so nodeOutputs.spillEnabled() was always false
// and every node's output stayed fully in memory regardless of size —
// silently, with no error, in exactly the deployment shape most likely to
// run large pipelines across real worker pods. Mirrors
// TestPipeline_LargeOutputSpillsWithoutChangingResults (spill_pipeline_test.go)
// but with SQLArtifactStore as eng.ArtifactStore instead of the default
// LocalDiskArtifactStore, proving spilling now actually engages under it.
func TestPipeline_SQLArtifactStore_SpillsWithoutChangingResults(t *testing.T) {
	const rows = 400
	srv := bigRowServer(t, rows, 200)

	dir := t.TempDir()
	s, err := store.NewSQLiteStore(filepath.Join(dir, "sql-spill.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	db, ok := s.RawDB().(*sql.DB)
	if !ok {
		t.Fatal("RawDB() did not return *sql.DB")
	}
	spillDir := filepath.Join(dir, "spill-blobs")
	artifactStore, err := NewSQLArtifactStore(db, "sqlite", spillDir)
	if err != nil {
		t.Fatalf("NewSQLArtifactStore: %v", err)
	}

	eng := NewEngine(s)
	eng.ArtifactStore = artifactStore
	eng.SpillThresholdBytes = 4096 // well below the source's output
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := eng.Close(ctx); err != nil {
			t.Errorf("engine close: %v", err)
		}
	})

	pipeline := &models.Pipeline{
		ID: "p-sql-spill", Name: "SQL Artifact Store Spill", Enabled: true,
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceAPI, Name: "Big Source",
				Config: map[string]interface{}{"url": srv.URL}},
			{ID: "keep", Type: models.NodeTypeTransform, Name: "Passthrough",
				Config: map[string]interface{}{"rules": []interface{}{
					map[string]interface{}{"type": "add_column", "name": "tag", "expression": "'seen'"},
				}}},
		},
		Edges: []models.Edge{{From: "source", To: "keep"}},
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}

	run, err := eng.RunPipeline(pipeline.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %s, want success (error: %s)", run.Status, run.Error)
	}

	// The point of this test: prove spilling actually engaged under
	// SQLArtifactStore, not merely that results are correct (which would
	// also be true if BlobStoreProvider silently failed and everything
	// stayed in memory the whole time — exactly the bug this fixes). A
	// spilled node writes real blob files under spillDir; before Blobs()
	// existed, spillEnabled() was always false and this directory would
	// never be created at all.
	entries, err := os.ReadDir(spillDir)
	if err != nil {
		t.Fatalf("read spill dir: %v (spilling never engaged — BlobStoreProvider gap regressed)", err)
	}
	if len(entries) == 0 {
		t.Fatal("spill directory exists but is empty — spilling did not actually write anything")
	}

	nodeRuns, err := s.ListNodeRunsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, nr := range nodeRuns {
		if nr.NodeID == "keep" && nr.Status == models.RunStatusSuccess {
			if nr.RowCount != rows {
				t.Errorf("downstream node saw %d rows, want %d", nr.RowCount, rows)
			}
			return
		}
	}
	t.Fatalf("no successful run for the downstream node among %+v", nodeRuns)
}

// TestSQLArtifactStore_BlobStoreProvider proves the type assertion
// node_output_store.go's newOutputs relies on actually succeeds.
func TestSQLArtifactStore_BlobStoreProvider(t *testing.T) {
	s := newSQLArtifactTestStore(t)
	var _ BlobStoreProvider = s // compile-time: must implement the interface
	if s.Blobs() == nil {
		t.Fatal("Blobs() returned nil")
	}
}

// TestSQLArtifactStore_DeleteRunArtifactsClearsSpillBlobsToo proves
// DeleteRunArtifacts cleans up the local-disk spill scratch space under a
// run's namespace, not just the SQL rows — the two are separate stores for
// separate purposes (see Blobs()'s doc comment) but must share one
// lifetime.
func TestSQLArtifactStore_DeleteRunArtifactsClearsSpillBlobsToo(t *testing.T) {
	s := newSQLArtifactTestStore(t)
	ctx := context.Background()

	ref, err := s.Blobs().Put(ctx, "run-1", strings.NewReader("hello spill scratch data"), artifact.PutOptions{MediaType: artifact.MediaTypeNDJSON})
	if err != nil {
		t.Fatalf("Blobs().Put: %v", err)
	}
	if _, err := s.Blobs().Open(ctx, ref); err != nil {
		t.Fatalf("Blobs().Open before delete: %v", err)
	}

	if err := s.DeleteRunArtifacts("run-1"); err != nil {
		t.Fatalf("DeleteRunArtifacts: %v", err)
	}

	if _, err := s.Blobs().Open(ctx, ref); err == nil {
		t.Fatal("expected spilled blob to be gone after DeleteRunArtifacts")
	}
}
