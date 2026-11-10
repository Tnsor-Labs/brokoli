package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// A preview is stored exactly, so it must read back exactly: ids above
// 2^53 used to come back rounded from GetNodePreview on both dialects,
// because the rows were decoded into float64 (#965).
func TestNodePreviewKeepsIntegersExactOnBothDialects(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "preview.db"))
		if err != nil {
			t.Fatalf("NewSQLiteStore: %v", err)
		}
		t.Cleanup(func() { s.Close() })
		checkPreviewIntegersExact(t, s)
	})
	t.Run("postgres", func(t *testing.T) {
		dsn := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
		if dsn == "" {
			t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
		}
		s, err := NewPostgresStore(dsn)
		if err != nil {
			t.Fatalf("NewPostgresStore: %v", err)
		}
		t.Cleanup(func() { s.Close() })
		checkPreviewIntegersExact(t, s)
	})
}

type previewStore interface {
	CreatePipeline(*models.Pipeline) error
	CreateRun(*models.Run) error
	SaveNodePreview(runID, nodeID string, p NodePreview) error
	GetNodePreview(runID, nodeID string) (NodePreview, error)
}

func checkPreviewIntegersExact(t *testing.T, s previewStore) {
	t.Helper()
	now := time.Now().Truncate(time.Millisecond)
	suffix := now.Format("150405.000000000")
	p := &models.Pipeline{
		ID:        "pipe-int64-" + suffix,
		Name:      "Preview int64 " + suffix,
		Nodes:     []models.Node{{ID: "n1", Type: models.NodeTypeSourceFile, Name: "src"}},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.CreatePipeline(p); err != nil {
		t.Fatalf("CreatePipeline: %v", err)
	}
	run := &models.Run{ID: "run-int64-" + suffix, PipelineID: p.ID, Status: models.RunStatusSuccess, StartedAt: &now}
	if err := s.CreateRun(run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	rows := []common.DataRow{
		{"id": int64(9007199254740993), "max": int64(9223372036854775807), "min": int64(-9223372036854775808)},
		{"id": int64(1), "ratio": 1.5, "nested": map[string]interface{}{"ids": []interface{}{int64(9007199254740995)}}},
	}
	if err := s.SaveNodePreview(run.ID, "n1", NodePreview{Columns: []string{"id", "max", "min"}, Rows: rows}); err != nil {
		t.Fatalf("SaveNodePreview: %v", err)
	}
	got, err := s.GetNodePreview(run.ID, "n1")
	if err != nil {
		t.Fatalf("GetNodePreview: %v", err)
	}
	if got.Rows[0]["id"] != int64(9007199254740993) || got.Rows[0]["max"] != int64(9223372036854775807) || got.Rows[0]["min"] != int64(-9223372036854775808) {
		t.Fatalf("row 0 = %#v, want the stored integers exactly", got.Rows[0])
	}
	if got.Rows[1]["ratio"] != 1.5 {
		t.Fatalf("ratio = %#v, want the float 1.5", got.Rows[1]["ratio"])
	}
	out, err := json.Marshal(got.Rows)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"id":9007199254740993,"max":9223372036854775807,"min":-9223372036854775808},{"id":1,"nested":{"ids":[9007199254740995]},"ratio":1.5}]`
	if string(out) != want {
		t.Fatalf("preview rows as served = %s\nwant %s", out, want)
	}
}

func TestDecodePreviewRowsShapes(t *testing.T) {
	if rows, err := decodePreviewRows([]byte(`null`)); err != nil || rows != nil {
		t.Fatalf("null = %v, %v; want no rows", rows, err)
	}
	if rows, err := decodePreviewRows([]byte(`[null, {"a": 1}]`)); err != nil || len(rows) != 2 || rows[0] != nil || rows[1]["a"] != int64(1) {
		t.Fatalf("[null, {a:1}] = %#v, %v", rows, err)
	}
	for _, bad := range []string{`{}`, `[1]`, `[{"a":1}] x`, `[`} {
		if _, err := decodePreviewRows([]byte(bad)); err == nil {
			t.Errorf("decodePreviewRows(%s) accepted it", bad)
		}
	}
}
