package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/fetchers"
	"github.com/Tnsor-Labs/brokoli/store"
)

// TestSystemPurge_DeletesArtifactsAndCheckpointsForPurgedRuns is the
// end-to-end acceptance test for Tnsor-Labs/brokoli#49: POST /api/system/purge
// must clean up local-disk artifacts and pagination checkpoints for the
// same runs it deletes from the database, not just the DB rows.
func TestSystemPurge_DeletesArtifactsAndCheckpointsForPurgedRuns(t *testing.T) {
	dir := t.TempDir()
	s, err := store.NewSQLiteStore(filepath.Join(dir, "purge.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	e := engine.NewEngine(s)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = e.Close(ctx)
	})
	e.ArtifactStore = engine.NewLocalDiskArtifactStore(filepath.Join(dir, "artifacts"))
	e.PaginationCheckpointStore = engine.NewLocalDiskPaginationCheckpointStore(filepath.Join(dir, "checkpoints"))

	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := s.CreatePipeline(&models.Pipeline{ID: "p1", Name: "Purge Test", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	old := now.AddDate(0, 0, -60)
	recent := now.AddDate(0, 0, -1)
	if err := s.CreateRun(&models.Run{ID: "run-old", PipelineID: "p1", Status: models.RunStatusSuccess, StartedAt: &old}); err != nil {
		t.Fatalf("create old run: %v", err)
	}
	if err := s.CreateRun(&models.Run{ID: "run-recent", PipelineID: "p1", Status: models.RunStatusSuccess, StartedAt: &recent}); err != nil {
		t.Fatalf("create recent run: %v", err)
	}

	ds := &common.DataSet{Columns: []string{"v"}, Rows: []common.DataRow{{"v": "x"}}}
	if err := e.ArtifactStore.WriteArtifact("run-old", "node-a", "", ds); err != nil {
		t.Fatalf("write artifact for run-old: %v", err)
	}
	if err := e.ArtifactStore.WriteArtifact("run-recent", "node-a", "", ds); err != nil {
		t.Fatalf("write artifact for run-recent: %v", err)
	}
	cp := fetchers.PaginationCheckpoint{Strategy: "offset", Offset: 10, PagesFetched: 5, RecordCount: 1}
	if err := e.PaginationCheckpointStore.SaveCheckpoint("run-old", "node-a", cp, []map[string]interface{}{{"v": "x"}}); err != nil {
		t.Fatalf("save checkpoint for run-old: %v", err)
	}

	body, _ := json.Marshal(map[string]int{"days": 30})
	req := httptest.NewRequest(http.MethodPost, "/api/system/purge", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	systemPurge(s, e)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if deleted, _ := resp["deleted"].(float64); int(deleted) != 1 {
		t.Errorf("expected 1 run deleted, got %v", resp["deleted"])
	}

	// The old run's DB row, artifact, and checkpoint must all be gone.
	if _, err := s.GetRun("run-old"); err == nil {
		t.Error("run-old should have been purged from the DB")
	}
	if _, err := e.ArtifactStore.ReadArtifact("run-old", "node-a", ""); !errors.Is(err, engine.ErrArtifactNotFound) {
		t.Errorf("run-old artifact: got %v, want ErrArtifactNotFound", err)
	}
	if _, _, err := e.PaginationCheckpointStore.LoadCheckpoint("run-old", "node-a"); !errors.Is(err, engine.ErrCheckpointNotFound) {
		t.Errorf("run-old checkpoint: got %v, want ErrCheckpointNotFound", err)
	}

	// The recent run must be completely untouched.
	if _, err := s.GetRun("run-recent"); err != nil {
		t.Errorf("run-recent should still exist in the DB: %v", err)
	}
	if _, err := e.ArtifactStore.ReadArtifact("run-recent", "node-a", ""); err != nil {
		t.Errorf("run-recent artifact should still exist: %v", err)
	}
}

// TestSystemPurge_NilEngineDoesNotPanic covers a caller (or test) that
// constructs the handler without an engine — purgeRunFiles must be a
// no-op, not a nil-pointer panic.
func TestSystemPurge_NilEngineDoesNotPanic(t *testing.T) {
	dir := t.TempDir()
	s, err := store.NewSQLiteStore(filepath.Join(dir, "purge.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	body, _ := json.Marshal(map[string]int{"days": 30})
	req := httptest.NewRequest(http.MethodPost, "/api/system/purge", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	systemPurge(s, nil)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}
