package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
)

// An import gets a new id, whatever the document says, as Create does. A
// kept id could collide with any pipeline on the server, and the error that
// came back said whether that id existed.
func TestImportAlwaysAssignsANewID(t *testing.T) {
	s := newOrgCheckStore(t)
	existing := &models.Pipeline{ID: "taken-id", PipelineID: "someone-elses", Name: "Someone else's", OrgID: "org-other",
		Nodes: []models.Node{}, Edges: []models.Edge{}}
	if err := s.CreatePipeline(existing); err != nil {
		t.Fatal(err)
	}
	h := NewPipelineHandler(s, nil)

	body := `{"id":"taken-id","name":"Imported","nodes":[{"id":"src","type":"source_api","name":"src","config":{"url":"/api/samples/data/employees.json","method":"GET"}}],"edges":[]}`
	rec := httptest.NewRecorder()
	h.Import(rec, httptest.NewRequest(http.MethodPost, "/api/pipelines/import", strings.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("import: %d %s", rec.Code, rec.Body.String())
	}
	var got models.Pipeline
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID == "" || got.ID == "taken-id" {
		t.Errorf("imported pipeline id = %q, want a new one", got.ID)
	}
	if p, err := s.GetPipeline("taken-id"); err != nil || p.Name != "Someone else's" {
		t.Errorf("the existing pipeline changed: %+v (%v)", p, err)
	}
}
