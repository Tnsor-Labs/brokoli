package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// GET /api/lineage attests an edge that a real run proved (ADR-039).
//
// End to end: a real pipeline runs through the real engine with every
// output stored, and the lineage handler -- which had no tests at all
// before this -- reads the profiles and the execution records back and
// draws the graph. The quality check returned its input unchanged, so its
// run stored identical bytes in and out, and the edges through it must say
// so, naming the run.
func TestTheLineageGraphAttestsWhatARunProved(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BROKOLI_ARTIFACT_DIR", filepath.Join(dir, "artifacts"))

	s, err := store.NewSQLiteStore(filepath.Join(dir, "lineage.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	in := filepath.Join(dir, "in.csv")
	if err := os.WriteFile(in, []byte("id,amount\n1,10\n2,20\n3,30\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pipe := &models.Pipeline{
		ID: "p-att", Name: "p-att", Enabled: true,
		// The lineage handler lists the request's workspace; a pipeline
		// with none is not in it, and the graph would be empty.
		WorkspaceID: models.DefaultWorkspaceID,
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceFile, Name: "src",
				Config: map[string]interface{}{"path": in, "format": "csv"}},
			{ID: "qc", Type: models.NodeTypeQualityCheck, Name: "qc",
				Config: map[string]interface{}{"rules": []interface{}{
					map[string]interface{}{"column": "id", "rule": "not_null", "on_failure": "block"},
				}}},
			{ID: "dst", Type: models.NodeTypeSinkFile, Name: "dst",
				Config: map[string]interface{}{"path": filepath.Join(dir, "out.csv"), "format": "csv"}},
		},
		Edges:     []models.Edge{{From: "src", To: "qc"}, {From: "qc", To: "dst"}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := s.CreatePipeline(pipe); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	eng := engine.NewEngine(s)
	eng.SpillThresholdBytes = 1 // every output stored, so every one has a digest
	run, err := eng.RunPipeline(pipe.ID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// Profiles are written asynchronously after a node succeeds, so the
	// graph is read until it carries column edges into the quality check,
	// rather than once.
	qcID := "proc:p-att:qc"
	var edges []engine.LineageColumnEdge
	deadline := time.Now().Add(20 * time.Second)
	for {
		rec := httptest.NewRecorder()
		lineageHandler(s)(rec, httptest.NewRequest(http.MethodGet, "/api/lineage", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("lineage status = %d; body=%s", rec.Code, rec.Body.String())
		}
		var g engine.LineageGraph
		if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil {
			t.Fatalf("decoding the graph: %v", err)
		}
		edges = edges[:0]
		for _, e := range g.ColumnEdges {
			if e.To == qcID {
				edges = append(edges, e)
			}
		}
		if len(edges) >= 2 {
			break
		}
		if time.Now().After(deadline) {
			// Say where the graph falls short, not only that it does.
			status := "unreadable"
			if r, err := s.GetRun(run.ID); err == nil {
				status = string(r.Status)
			}
			profiles, _ := s.GetLatestNodeProfilesForPipelines([]string{pipe.ID})
			var have []string
			for k := range profiles {
				have = append(have, k)
			}
			var nodeIDs []string
			for _, n := range g.Nodes {
				nodeIDs = append(nodeIDs, n.ID)
			}
			t.Fatalf("no column edges into %s after the run: run status=%s, graph nodes=%v, "+
				"column edges in graph=%d, profiles stored=%v", qcID, status, nodeIDs, len(g.ColumnEdges), have)
		}
		time.Sleep(50 * time.Millisecond)
	}

	for _, e := range edges {
		if e.Evidence != engine.EvidenceAttested {
			t.Errorf("%s -> %s: evidence = %q, want attested; the run stored identical bytes in and out",
				e.FromColumn, e.ToColumn, e.Evidence)
		}
		if !strings.Contains(e.MappingReason, run.ID) {
			t.Errorf("%s -> %s: reason %q does not name the run that proved it", e.FromColumn, e.ToColumn, e.MappingReason)
		}
	}
}
