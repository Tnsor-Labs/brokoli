package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/models"
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
	nSmall := len(small)
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
	nLarge := len(large)
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
