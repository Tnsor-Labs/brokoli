package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
)

// A pipeline built from code arrives without a layout; it is laid out when
// saved, and only layout changes, so a save that pauses an old pipeline is
// still not a graph change.
func TestStackedPipelinesAreLaidOutWhenSaved(t *testing.T) {
	b := newAgentBed(t)
	body := `{"name":"from-code","nodes":[
		{"id":"a","type":"source_api","name":"A","config":{"url":"https://example.com/a"}},
		{"id":"b","type":"transform","name":"B","config":{}}],
		"edges":[{"from":"a","to":"b"}]}`
	code, created, raw := b.do(http.MethodPost, "/api/pipelines", b.tokens["ada"], body)
	if code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("create: %d %s", code, raw)
	}
	nodes := created["nodes"].([]any)
	pa, pb := nodes[0].(map[string]any)["position"], nodes[1].(map[string]any)["position"]
	if pa.(map[string]any)["x"].(float64) >= pb.(map[string]any)["x"].(float64) {
		t.Fatalf("not laid out: %v %v", pa, pb)
	}

	// An old pipeline, stored stacked and no longer valid as it stands.
	old := &models.Pipeline{ID: "old", Name: "old", Enabled: true, WorkspaceID: models.DefaultWorkspaceID,
		Nodes: []models.Node{{ID: "x", Type: "source_api", Name: "X", Config: map[string]interface{}{}}, // no url: invalid today
			{ID: "y", Type: "transform", Name: "Y", Config: map[string]interface{}{}}},
		Edges: []models.Edge{{From: "x", To: "y"}}}
	if err := b.s.CreatePipeline(old); err != nil {
		t.Fatal(err)
	}
	stored, _ := b.s.GetPipeline("old")
	stored.Enabled = false
	data, _ := json.Marshal(stored)
	code, paused, raw := b.do(http.MethodPut, "/api/pipelines/old", b.tokens["ada"], string(data))
	if code != http.StatusOK {
		t.Fatalf("pausing an old pipeline: %d %s", code, raw)
	}
	px := paused["nodes"].([]any)
	if px[0].(map[string]any)["position"].(map[string]any)["x"] == px[1].(map[string]any)["position"].(map[string]any)["x"] {
		t.Fatalf("still stacked: %s", raw)
	}
}
