package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

func TestPipeline_UnknownNodeType_FailsClosed(t *testing.T) {
	dir := t.TempDir()
	real, err := store.NewSQLiteStore(filepath.Join(dir, "unknown-type.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer real.Close()

	csvPath := filepath.Join(dir, "in.csv")
	if err := os.WriteFile(csvPath, []byte("id,name\n1,brokoli\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pipeline := &models.Pipeline{
		ID:   "unknown-type-pipeline",
		Name: "Unknown type passthrough",
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceFile, Name: "Src", Config: map[string]interface{}{"path": csvPath, "format": "csv"}},
			{ID: "mystery", Type: models.NodeType("totally_unrecognized_future_type"), Name: "Mystery", Config: map[string]interface{}{}},
			{ID: "sink", Type: models.NodeTypeSinkFile, Name: "Sink", Config: map[string]interface{}{"path": filepath.Join(dir, "out.csv"), "format": "csv"}},
		},
		Edges: []models.Edge{
			{From: "src", To: "mystery"},
			{From: "mystery", To: "sink"},
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := real.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}

	eng := NewEngine(real)
	if _, err := eng.RunPipeline(pipeline.ID); err == nil || !strings.Contains(err.Error(), "unsupported type") {
		t.Fatalf("RunPipeline error = %v, want unsupported type", err)
	}
}

// TestPipeline_DatasetMapEndToEnd exercises dataset_map through the real
// engine dispatch (not just the pure ExecutePartitionTransform helper),
// using config.function.script the same way a hand-authored or
// future-SDK-emitted pipeline would.
func TestPipeline_DatasetMapEndToEnd(t *testing.T) {
	dir := t.TempDir()
	real, err := store.NewSQLiteStore(filepath.Join(dir, "dataset-map-e2e.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer real.Close()

	csvPath := filepath.Join(dir, "in.csv")
	if err := os.WriteFile(csvPath, []byte("id,amount\n1,10\n2,20\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, "out.csv")

	pipeline := &models.Pipeline{
		ID:   "dataset-map-e2e-pipeline",
		Name: "dataset_map e2e",
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceFile, Name: "Src", Config: map[string]interface{}{"path": csvPath, "format": "csv"}},
			{
				ID: "map1", Type: models.NodeTypeDatasetMap, Name: "Double",
				Config: map[string]interface{}{
					"function": map[string]interface{}{
						"name": "double_amount",
						"script": `
for r in rows:
    r["amount"] = str(float(r["amount"]) * 2)
output_data = {"columns": columns, "rows": rows}
`,
					},
				},
			},
			{ID: "sink", Type: models.NodeTypeSinkFile, Name: "Sink", Config: map[string]interface{}{"path": outPath, "format": "csv"}},
		},
		Edges: []models.Edge{
			{From: "src", To: "map1"},
			{From: "map1", To: "sink"},
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := real.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}

	eng := NewEngine(real)
	run, err := eng.RunPipeline(pipeline.ID)
	if err != nil {
		t.Fatalf("RunPipeline failed: %v", err)
	}
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %s, want success", run.Status)
	}

	out, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read sink output: %v", err)
	}
	outStr := string(out)
	if !strings.Contains(outStr, "20") || !strings.Contains(outStr, "40") {
		t.Errorf("expected doubled amounts (20, 40) in sink output, got:\n%s", outStr)
	}
}
