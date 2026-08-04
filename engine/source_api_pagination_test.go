package engine

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/errors"
)

// TestSourceAPI_Pagination_EndToEnd is the full-pipeline acceptance test for
// issue #30's worked example (a GBIF-style offset_pages source): a
// source_api node with pagination config fetches all pages up to
// max_records/end_flag and hands the combined dataset to a downstream sink
// node, with no manual loop in a @task and no error/warning about ignored
// config.
func TestSourceAPI_Pagination_EndToEnd(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offset := r.URL.Query().Get("offset")
		w.Header().Set("Content-Type", "application/json")
		switch offset {
		case "0", "":
			errors.CheckErrorMultiple(w.Write([]byte(`{"results":[{"key":1,"scientificName":"Aves"},{"key":2,"scientificName":"Mammalia"}],"endOfRecords":false}`)))
		case "2":
			errors.CheckErrorMultiple(w.Write([]byte(`{"results":[{"key":3,"scientificName":"Reptilia"}],"endOfRecords":true}`)))
		default:
			t.Fatalf("unexpected offset requested: %q", offset)
		}
	}))
	defer server.Close()

	eng, s := newResumeTestEngine(t)
	dir := t.TempDir()
	outPath := filepath.Join(dir, "out.csv")

	pipeline := &models.Pipeline{
		ID: "p-source-api-pagination", Name: "Source API Pagination", Enabled: true,
		Nodes: []models.Node{
			{
				ID: "source", Type: models.NodeTypeSourceAPI, Name: "GBIF Occurrences",
				Config: map[string]interface{}{
					"url":     server.URL,
					"records": "results",
					"pagination": map[string]interface{}{
						"strategy":  "offset",
						"page_size": float64(2),
						"end_flag":  "endOfRecords",
					},
				},
			},
			{
				ID: "sink", Type: models.NodeTypeSinkFile, Name: "Write CSV",
				Config: map[string]interface{}{"path": outPath, "format": "csv"},
			},
		},
		Edges: []models.Edge{{From: "source", To: "sink"}},
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	run, err := eng.RunPipeline(pipeline.ID)
	if err != nil {
		t.Fatalf("run pipeline: %v", err)
	}
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %s, want success (error: %s)", run.Status, run.Error)
	}

	out, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read sink output: %v", err)
	}
	content := string(out)
	for _, want := range []string{"Aves", "Mammalia", "Reptilia"} {
		if !strings.Contains(content, want) {
			t.Errorf("expected output CSV to contain %q, got:\n%s", want, content)
		}
	}
}
