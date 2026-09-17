package store

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

func TestNodePreviewTruncationMetadata(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "preview.db")
	s, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Now().Truncate(time.Millisecond)
	p := &models.Pipeline{
		ID:        "pipe-preview",
		Name:      "Preview Pipeline",
		Nodes:     []models.Node{{ID: "n1", Type: models.NodeTypeSourceFile, Name: "src"}},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.CreatePipeline(p); err != nil {
		t.Fatalf("CreatePipeline: %v", err)
	}
	run := &models.Run{ID: "run-preview", PipelineID: p.ID, Status: models.RunStatusSuccess, StartedAt: &now}
	if err := s.CreateRun(run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	cols := []string{"id"}
	small := make([]common.DataRow, 12)
	for i := range small {
		small[i] = common.DataRow{"id": i}
	}
	nSmall := int64(len(small))
	if err := s.SaveNodePreview(run.ID, "n-small", NodePreview{
		Columns: cols, Rows: small, Truncated: false, TotalRows: &nSmall,
	}); err != nil {
		t.Fatalf("SaveNodePreview small: %v", err)
	}
	got, err := s.GetNodePreview(run.ID, "n-small")
	if err != nil {
		t.Fatalf("GetNodePreview small: %v", err)
	}
	if got.Truncated {
		t.Fatalf("small preview Truncated = true, want false")
	}
	if got.TotalRows == nil || *got.TotalRows != 12 {
		t.Fatalf("small TotalRows = %v, want 12", got.TotalRows)
	}
	if len(got.Rows) != 12 {
		t.Fatalf("small rows = %d, want 12", len(got.Rows))
	}

	large := make([]common.DataRow, 80)
	for i := range large {
		large[i] = common.DataRow{"id": i}
	}
	nLarge := int64(len(large))
	if err := s.SaveNodePreview(run.ID, "n-large", NodePreview{
		Columns: cols, Rows: large, Truncated: true, TotalRows: &nLarge,
	}); err != nil {
		t.Fatalf("SaveNodePreview large: %v", err)
	}
	got, err = s.GetNodePreview(run.ID, "n-large")
	if err != nil {
		t.Fatalf("GetNodePreview large: %v", err)
	}
	if !got.Truncated {
		t.Fatalf("large preview Truncated = false, want true")
	}
	if got.TotalRows == nil || *got.TotalRows != 80 {
		t.Fatalf("large TotalRows = %v, want 80", got.TotalRows)
	}
	if len(got.Rows) != 50 {
		t.Fatalf("large rows = %d, want 50", len(got.Rows))
	}

	// Cap hit without a known total (stream preview path).
	capRows := make([]common.DataRow, 50)
	for i := range capRows {
		capRows[i] = common.DataRow{"id": i}
	}
	if err := s.SaveNodePreview(run.ID, "n-cap", NodePreview{
		Columns: cols, Rows: capRows, Truncated: true,
	}); err != nil {
		t.Fatalf("SaveNodePreview cap: %v", err)
	}
	got, err = s.GetNodePreview(run.ID, "n-cap")
	if err != nil {
		t.Fatalf("GetNodePreview cap: %v", err)
	}
	if !got.Truncated {
		t.Fatalf("cap Truncated = false, want true")
	}
	if got.TotalRows != nil {
		t.Fatalf("cap TotalRows = %v, want nil (unknown)", *got.TotalRows)
	}
}

func TestNodePreviewCapBackfill(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "preview-backfill.db")
	s, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Now().Truncate(time.Millisecond)
	p := &models.Pipeline{
		ID:        "pipe-backfill",
		Name:      "Backfill Pipeline",
		Nodes:     []models.Node{{ID: "n1", Type: models.NodeTypeSourceFile, Name: "src"}},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.CreatePipeline(p); err != nil {
		t.Fatalf("CreatePipeline: %v", err)
	}
	run := &models.Run{ID: "run-backfill", PipelineID: p.ID, Status: models.RunStatusSuccess, StartedAt: &now}
	if err := s.CreateRun(run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	// Simulate a pre-migration preview: exactly NodePreviewRowLimit rows,
	// truncated still at the DEFAULT (false), total_rows unknown.
	capRows := make([]common.DataRow, NodePreviewRowLimit)
	for i := range capRows {
		capRows[i] = common.DataRow{"id": i}
	}
	colJSON := `["id"]`
	rowJSON, err := json.Marshal(capRows)
	if err != nil {
		t.Fatalf("marshal rows: %v", err)
	}
	if _, err := s.db.Exec(
		`INSERT INTO node_previews (run_id, node_id, columns, rows, truncated, total_rows) VALUES (?, ?, ?, ?, 0, NULL)`,
		run.ID, "n-old", colJSON, string(rowJSON),
	); err != nil {
		t.Fatalf("insert old preview: %v", err)
	}

	// Same backfill migrate() runs after adding the columns.
	if _, err := s.db.Exec(`UPDATE node_previews SET truncated = 1 WHERE json_array_length(rows) = 50 AND total_rows IS NULL`); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	got, err := s.GetNodePreview(run.ID, "n-old")
	if err != nil {
		t.Fatalf("GetNodePreview: %v", err)
	}
	if !got.Truncated {
		t.Fatal("backfilled Truncated = false, want true")
	}
	if got.TotalRows != nil {
		t.Fatalf("backfilled TotalRows = %v, want nil (unknown)", *got.TotalRows)
	}
	if len(got.Rows) != NodePreviewRowLimit {
		t.Fatalf("rows = %d, want %d", len(got.Rows), NodePreviewRowLimit)
	}
}
