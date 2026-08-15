package engine

import (
	"database/sql"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

func countBlobFiles(t *testing.T, root string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".blob") {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return n
}

// streamedReclaimPipeline builds a code-node pipeline whose output is
// large enough to engage reference-passing, so real blobs land in the
// run's namespace.
func streamedReclaimPipeline(id string) *models.Pipeline {
	script := "begin_emit([\"id\"])\nfor i in range(500):\n    emit({\"id\": i})\n"
	return &models.Pipeline{
		ID: id, Name: "Blob Reclaim " + id, Enabled: true,
		Nodes: []models.Node{
			{ID: "gen", Type: models.NodeTypeCode, Name: "Gen",
				Capabilities: []string{models.CapabilitySource, models.CapabilityDatasetOutput},
				Config:       map[string]interface{}{"language": "python", "script": script}},
		},
	}
}

func newBlobReclaimEngine(t *testing.T) (*Engine, *store.SQLiteStore, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.NewSQLiteStore(filepath.Join(dir, "reclaim.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	eng := drainEngineOnCleanup(t, NewEngine(s))
	eng.SpillThresholdBytes = 1
	eng.StreamThresholdBytes = 1
	return eng, s, dir
}

// TestTransientBlobsReclaimedOnSQLArtifactStore is the #215 fix: on a
// deployment whose artifacts persist as database rows, a run's local blob
// scratch is deleted the moment the run is terminal — and the persisted
// artifact remains fully readable afterwards. Before this, the soak
// measured ~450MB/hour/worker of scratch that nothing ever reclaimed.
func TestTransientBlobsReclaimedOnSQLArtifactStore(t *testing.T) {
	eng, s, dir := newBlobReclaimEngine(t)
	db, ok := s.RawDB().(*sql.DB)
	if !ok {
		t.Fatal("RawDB() did not return *sql.DB")
	}
	spillDir := filepath.Join(dir, "scratch-blobs")
	artifactStore, err := NewSQLArtifactStore(db, "sqlite", spillDir)
	if err != nil {
		t.Fatal(err)
	}
	eng.ArtifactStore = artifactStore

	pipe := streamedReclaimPipeline("p-reclaim-sql")
	if err := s.CreatePipeline(pipe); err != nil {
		t.Fatal(err)
	}
	run, err := eng.RunPipeline(pipe.ID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %s, want success", run.Status)
	}

	if n := countBlobFiles(t, spillDir); n != 0 {
		t.Fatalf("blob scratch holds %d file(s) after terminal run, want 0", n)
	}
	ds, err := artifactStore.ReadArtifact(run.ID, "gen", "")
	if err != nil {
		t.Fatalf("artifact must survive scratch reclaim: %v", err)
	}
	if len(ds.Rows) != 500 {
		t.Fatalf("artifact rows = %d, want 500", len(ds.Rows))
	}
}

// TestTransientBlobsReclaimedOnFailedRun proves the reclaim is not
// success-only: failed runs park just as much scratch and are just as
// terminal.
func TestTransientBlobsReclaimedOnFailedRun(t *testing.T) {
	eng, s, dir := newBlobReclaimEngine(t)
	db, _ := s.RawDB().(*sql.DB)
	spillDir := filepath.Join(dir, "scratch-blobs")
	artifactStore, err := NewSQLArtifactStore(db, "sqlite", spillDir)
	if err != nil {
		t.Fatal(err)
	}
	eng.ArtifactStore = artifactStore

	script := "begin_emit([\"id\"])\nfor i in range(500):\n    emit({\"id\": i})\n"
	pipe := &models.Pipeline{
		ID: "p-reclaim-fail", Name: "Blob Reclaim Fail", Enabled: true,
		Nodes: []models.Node{
			{ID: "gen", Type: models.NodeTypeCode, Name: "Gen",
				Capabilities: []string{models.CapabilitySource, models.CapabilityDatasetOutput},
				Config:       map[string]interface{}{"language": "python", "script": script}},
			{ID: "boom", Type: models.NodeTypeCode, Name: "Boom",
				Config: map[string]interface{}{"language": "python", "script": "raise RuntimeError(\"boom\")\n", "max_retries": float64(0)}},
		},
		Edges: []models.Edge{{From: "gen", To: "boom"}},
	}
	if err := s.CreatePipeline(pipe); err != nil {
		t.Fatal(err)
	}
	run, _ := eng.RunPipeline(pipe.ID)
	if run == nil || run.Status != models.RunStatusFailed {
		t.Fatalf("run = %+v, want failed", run)
	}
	if n := countBlobFiles(t, spillDir); n != 0 {
		t.Fatalf("blob scratch holds %d file(s) after failed run, want 0", n)
	}
}

// TestBlobsRetainedOnLocalDiskArtifactStore pins the other side of the
// conditional: on LocalDiskArtifactStore the blobs ARE the artifacts
// (WriteArtifactRef is a zero-copy manifest over the same namespace), so
// run-terminal must NOT reclaim them — the ADR-010 resume contract reads
// them later. The store simply does not implement TransientBlobJanitor.
func TestBlobsRetainedOnLocalDiskArtifactStore(t *testing.T) {
	eng, s, dir := newBlobReclaimEngine(t)
	artDir := filepath.Join(dir, "artifacts")
	localStore := NewLocalDiskArtifactStore(artDir)
	eng.ArtifactStore = localStore

	if _, isJanitor := interface{}(localStore).(TransientBlobJanitor); isJanitor {
		t.Fatal("LocalDiskArtifactStore must NOT implement TransientBlobJanitor — its blobs are its artifacts")
	}

	pipe := streamedReclaimPipeline("p-reclaim-local")
	if err := s.CreatePipeline(pipe); err != nil {
		t.Fatal(err)
	}
	run, err := eng.RunPipeline(pipe.ID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %s, want success", run.Status)
	}

	if n := countBlobFiles(t, artDir); n == 0 {
		t.Fatal("local-disk artifact blobs were reclaimed — resume would be broken")
	}
	ds, err := localStore.ReadArtifact(run.ID, "gen", "")
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	if len(ds.Rows) != 500 {
		t.Fatalf("artifact rows = %d, want 500", len(ds.Rows))
	}

	// And the store's own lifetime op still reclaims when retention says so.
	if err := localStore.DeleteRunArtifacts(run.ID); err != nil {
		t.Fatal(err)
	}
	if n := countBlobFiles(t, artDir); n != 0 {
		t.Fatalf("DeleteRunArtifacts left %d blob file(s)", n)
	}
}
