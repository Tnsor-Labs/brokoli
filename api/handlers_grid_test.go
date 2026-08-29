package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/go-chi/chi/v5"
)

// The grid endpoint (#400): rows in dependency order regardless of
// authored order, columns oldest to newest, cells keyed run -> node with
// the latest attempt's outcome.
func TestGridHandler(t *testing.T) {
	withoutOrgResolver(t)
	s := newDepAPIStore(t)
	h := NewRunHandler(s, nil)

	pipe := createOrgPipe(t, s, "grid-pipe", "Grid Pipeline", "org-a", nil)
	// Authored deliberately out of dependency order: c, a, b for a -> b -> c.
	pipe.Nodes = []models.Node{
		{ID: "c", Type: models.NodeTypeSinkFile, Name: "C", Config: map[string]interface{}{"path": "/o.csv", "format": "csv"}},
		{ID: "a", Type: models.NodeTypeSourceFile, Name: "A", Config: map[string]interface{}{"path": "/i.csv", "format": "csv"}},
		{ID: "b", Type: models.NodeTypeTransform, Name: "B", Config: map[string]interface{}{}},
	}
	pipe.Edges = []models.Edge{{From: "a", To: "b"}, {From: "b", To: "c"}}
	if err := s.UpdatePipeline(pipe); err != nil {
		t.Fatal(err)
	}

	t0 := time.Now().UTC().Add(-2 * time.Hour)
	t1 := time.Now().UTC().Add(-1 * time.Hour)
	iv0, iv1 := t0.Truncate(time.Hour), t1.Truncate(time.Hour)
	run0, run1 := common.NewID(), common.NewID()
	for _, r := range []*models.Run{
		{ID: run0, PipelineID: pipe.ID, Status: models.RunStatusFailed, StartedAt: &t0,
			Trigger: models.RunTriggerScheduled, DataIntervalStart: &iv0, DataIntervalEnd: &iv1},
		{ID: run1, PipelineID: pipe.ID, Status: models.RunStatusSuccess, StartedAt: &t1},
	} {
		if err := s.CreateRun(r); err != nil {
			t.Fatal(err)
		}
	}
	for _, nr := range []*models.NodeRun{
		{ID: common.NewID(), RunID: run0, NodeID: "a", Status: models.RunStatusSuccess, Attempt: 0, StartedAt: &t0},
		{ID: common.NewID(), RunID: run0, NodeID: "b", Status: models.RunStatusFailed, Attempt: 0, StartedAt: &t0, Error: "boom"},
		{ID: common.NewID(), RunID: run0, NodeID: "b", Status: models.RunStatusFailed, Attempt: 1, StartedAt: &t0, Error: "boom again"},
		{ID: common.NewID(), RunID: run1, NodeID: "a", Status: models.RunStatusSuccess, Attempt: 0, StartedAt: &t1},
	} {
		if err := s.CreateNodeRun(nr); err != nil {
			t.Fatal(err)
		}
	}

	router := chi.NewRouter()
	router.Get("/pipelines/{id}/grid", h.Grid)
	req := reqWithOrg("GET", "/pipelines/"+pipe.ID+"/grid?runs=30", nil, "org-a")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	var got struct {
		Nodes []struct{ ID, Name string }
		Runs  []struct {
			ID              string
			Trigger         string
			PipelineVersion int `json:"pipeline_version"`
		}
		Cells map[string]map[string]struct {
			Status  string
			Attempt int
			Error   string
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}

	// Rows in dependency order, not authored order.
	if len(got.Nodes) != 3 || got.Nodes[0].ID != "a" || got.Nodes[1].ID != "b" || got.Nodes[2].ID != "c" {
		t.Errorf("node order = %+v, want a, b, c by dependency", got.Nodes)
	}
	// Columns oldest to newest.
	if len(got.Runs) != 2 || got.Runs[0].ID != run0 || got.Runs[1].ID != run1 {
		t.Errorf("run order = %+v, want oldest first", got.Runs)
	}
	if got.Runs[0].Trigger != models.RunTriggerScheduled {
		t.Errorf("run0 trigger = %q; the grid marks how a column was born", got.Runs[0].Trigger)
	}
	// The cell shows the LATEST attempt.
	if c := got.Cells[run0]["b"]; c.Attempt != 1 || c.Error != "boom again" {
		t.Errorf("run0/b cell = %+v, want the retry", c)
	}
	if _, ok := got.Cells[run1]["b"]; ok {
		t.Error("run1 never ran b; its cell must be absent, not invented")
	}

	// Another org's caller cannot read the grid.
	req = reqWithOrg("GET", "/pipelines/"+pipe.ID+"/grid", nil, "org-b")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatal("org-b read org-a's grid")
	}
}
