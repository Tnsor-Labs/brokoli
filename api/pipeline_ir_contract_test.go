package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/go-chi/chi/v5"
)

func TestPipelineCreate_RejectsUnsupportedIRBeforePersistence(t *testing.T) {
	s := newOrgCheckStore(t)
	h := NewPipelineHandler(s, nil)
	body := `{
		"ir_version":"2.1",
		"name":"conditional",
		"nodes":[
			{"id":"check","type":"condition","name":"Check","config":{"expression":"always_true"}},
			{"id":"yes","type":"notify","name":"Yes","config":{}}
		],
		"edges":[{"from":"check","to":"yes","condition":true}]
	}`

	req := httptest.NewRequest(http.MethodPost, "/api/pipelines", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.Create(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "unsupported pipeline IR version") {
		t.Fatalf("response should explain unsupported IR: %s", rec.Body.String())
	}
	pipelines, err := s.ListPipelines()
	if err != nil {
		t.Fatal(err)
	}
	if len(pipelines) != 0 {
		t.Fatalf("unsupported pipeline was persisted: %#v", pipelines)
	}
}

func TestPipelineCreate_RejectsConditionalEdgeInIR20(t *testing.T) {
	s := newOrgCheckStore(t)
	h := NewPipelineHandler(s, nil)
	body := `{
		"ir_version":"2.0",
		"name":"conditional",
		"nodes":[
			{"id":"check","type":"condition","name":"Check","config":{"expression":"always_true"}},
			{"id":"yes","type":"notify","name":"Yes","config":{}}
		],
		"edges":[{"from":"check","to":"yes","condition":true}]
	}`

	req := httptest.NewRequest(http.MethodPost, "/api/pipelines", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.Create(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "requires pipeline IR 2.1") {
		t.Fatalf("response should explain conditional-edge version: %s", rec.Body.String())
	}
}

func TestPipelineUpdate_RejectsUnsupportedIRWithoutChangingStoredVersion(t *testing.T) {
	s := newOrgCheckStore(t)
	now := time.Now().UTC()
	p := &models.Pipeline{
		ID:        "pipeline-1",
		IRVersion: "2.0",
		Name:      "Existing",
		Nodes:     []models.Node{},
		Edges:     []models.Edge{},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}

	h := NewPipelineHandler(s, nil)
	body := `{"ir_version":"2.1","name":"Changed","nodes":[],"edges":[]}`
	req := httptest.NewRequest(http.MethodPut, "/api/pipelines/pipeline-1", strings.NewReader(body))
	rec := httptest.NewRecorder()
	router := chi.NewRouter()
	router.Put("/api/pipelines/{id}", h.Update)
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	stored, err := s.GetPipeline(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.IRVersion != "2.0" || stored.Name != "Existing" {
		t.Fatalf("rejected update changed stored pipeline: %#v", stored)
	}
}

func TestPipelineImport_RejectsEmptyBody(t *testing.T) {
	s := newOrgCheckStore(t)
	h := NewPipelineHandler(s, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/pipelines/import", bytes.NewReader(nil))
	rec := httptest.NewRecorder()

	h.Import(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "empty import body") {
		t.Fatalf("response should explain empty body: %s", rec.Body.String())
	}
}

func TestPipelineImport_RejectsUnsupportedIRBeforePersistence(t *testing.T) {
	s := newOrgCheckStore(t)
	h := NewPipelineHandler(s, nil)
	body := `{"ir_version":"2.1","name":"future","nodes":[],"edges":[]}`
	req := httptest.NewRequest(http.MethodPost, "/api/pipelines/import", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.Import(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "unsupported pipeline IR version") {
		t.Fatalf("response should explain unsupported IR: %s", rec.Body.String())
	}
}
