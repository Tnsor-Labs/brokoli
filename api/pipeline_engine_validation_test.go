package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/plugins"
	"github.com/Tnsor-Labs/brokoli/pkg/sodp"
	"github.com/go-chi/chi/v5"
)

type apiValidationExecutor struct {
	nodeType string
}

func (e *apiValidationExecutor) Name() string { return "api-validation" }
func (e *apiValidationExecutor) CanHandle(nodeType string) bool {
	return nodeType == e.nodeType
}
func (e *apiValidationExecutor) Execute(extensions.ExecutionContext) (*extensions.ExecutionResult, error) {
	return &extensions.ExecutionResult{}, nil
}

func engineInvalidPipeline(name string) *models.Pipeline {
	return &models.Pipeline{
		Name: name,
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceFile, Name: "Source", Config: map[string]interface{}{"path": "/tmp/input.csv"}},
			{ID: "unknown", Type: "unknown", Name: "Unknown", Config: map[string]interface{}{}},
		},
		Edges: []models.Edge{{From: "source", To: "unknown"}},
	}
}

func servePipelineHandler(t *testing.T, method, pattern, path string, body []byte, handler http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.MethodFunc(method, pattern, handler)
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestPipelineCreateRejectsEngineInvalidWithoutPersistence(t *testing.T) {
	s := newOrgCheckStore(t)
	h := NewPipelineHandler(s, nil)
	body, _ := json.Marshal(engineInvalidPipeline("invalid create"))
	rec := servePipelineHandler(t, http.MethodPost, "/pipelines", "/pipelines", body, h.Create)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "unsupported type") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	pipelines, err := s.ListPipelines()
	if err != nil {
		t.Fatal(err)
	}
	if len(pipelines) != 0 {
		t.Fatalf("invalid pipeline persisted: %#v", pipelines)
	}
}

func TestPipelineUpdateMetadataOnlyExemptsLegacyInvalidGraph(t *testing.T) {
	s := newOrgCheckStore(t)
	legacy := engineInvalidPipeline("legacy invalid")
	legacy.ID = "legacy-metadata"
	legacy.Enabled = true
	legacy.CreatedAt = time.Now().UTC()
	legacy.UpdatedAt = legacy.CreatedAt
	if err := s.CreatePipeline(legacy); err != nil {
		t.Fatal(err)
	}
	stored, err := s.GetPipeline(legacy.ID)
	if err != nil {
		t.Fatal(err)
	}

	h := NewPipelineHandler(s, nil)

	// Pause + rename with the graph byte-identical: the operational-metadata
	// exemption must let this through even though the stored graph cannot
	// pass executable validation.
	paused := *stored
	paused.Enabled = false
	paused.Name = "legacy invalid (paused)"
	rec := servePipelineHandler(t, http.MethodPut, "/pipelines/{id}", "/pipelines/legacy-metadata", mustMarshalPipeline(&paused), h.Update)
	if rec.Code != http.StatusOK {
		t.Fatalf("metadata-only update status=%d body=%s", rec.Code, rec.Body.String())
	}
	after, err := s.GetPipeline(legacy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Enabled || after.Name != "legacy invalid (paused)" {
		t.Fatalf("metadata-only update not persisted: enabled=%v name=%q", after.Enabled, after.Name)
	}

	// Any graph change on the same pipeline must revalidate in full and
	// fail closed.
	changed := *after
	changed.Nodes = append(append([]models.Node(nil), after.Nodes...), models.Node{
		ID: "another", Type: "still_unknown", Name: "Another", Config: map[string]interface{}{},
	})
	rec = servePipelineHandler(t, http.MethodPut, "/pipelines/{id}", "/pipelines/legacy-metadata", mustMarshalPipeline(&changed), h.Update)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "unsupported type") {
		t.Fatalf("graph-change update status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPipelineUpdateRejectsEngineInvalidWithoutMutationOrVersion(t *testing.T) {
	s := newOrgCheckStore(t)
	now := time.Now().UTC()
	original := &models.Pipeline{
		ID: "pipeline-1", Name: "Original",
		Nodes: []models.Node{{ID: "source", Type: models.NodeTypeSourceFile, Name: "Source", Config: map[string]interface{}{"path": "/tmp/input.csv"}}},
		Edges: []models.Edge{}, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.CreatePipeline(original); err != nil {
		t.Fatal(err)
	}
	beforeVersions, err := s.ListPipelineVersions(original.ID)
	if err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(engineInvalidPipeline("Changed"))
	h := NewPipelineHandler(s, nil)
	rec := servePipelineHandler(t, http.MethodPut, "/pipelines/{id}", "/pipelines/pipeline-1", body, h.Update)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	stored, err := s.GetPipeline(original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Name != original.Name || len(stored.Nodes) != 1 {
		t.Fatalf("original pipeline mutated: %#v", stored)
	}
	afterVersions, err := s.ListPipelineVersions(original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterVersions) != len(beforeVersions) {
		t.Fatalf("versions changed from %d to %d", len(beforeVersions), len(afterVersions))
	}
}

func TestPipelineRollbackRejectsEngineInvalidSnapshotWithoutMutationOrVersion(t *testing.T) {
	s := newOrgCheckStore(t)
	now := time.Now().UTC()
	current := &models.Pipeline{
		ID: "pipeline-rollback", Name: "Current",
		Nodes: []models.Node{{ID: "source", Type: models.NodeTypeSourceFile, Name: "Source", Config: map[string]interface{}{"path": "/tmp/input.csv"}}},
		Edges: []models.Edge{}, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.CreatePipeline(current); err != nil {
		t.Fatal(err)
	}
	invalidSnapshot := engineInvalidPipeline("Invalid historical snapshot")
	invalidSnapshot.ID = current.ID
	if _, err := s.SavePipelineVersion(current.ID, string(mustMarshalPipeline(invalidSnapshot)), "invalid historical snapshot"); err != nil {
		t.Fatal(err)
	}
	beforeVersions, err := s.ListPipelineVersions(current.ID)
	if err != nil {
		t.Fatal(err)
	}

	h := NewPipelineHandler(s, nil)
	rec := servePipelineHandler(t, http.MethodPost, "/pipelines/{id}/rollback", "/pipelines/pipeline-rollback/rollback", []byte(`{"version":1}`), h.Rollback)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "unsupported type") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	stored, err := s.GetPipeline(current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Name != current.Name || len(stored.Nodes) != 1 || stored.Nodes[0].Type != models.NodeTypeSourceFile {
		t.Fatalf("current pipeline changed after rejected rollback: %#v", stored)
	}
	afterVersions, err := s.ListPipelineVersions(current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterVersions) != len(beforeVersions) {
		t.Fatalf("versions changed from %d to %d", len(beforeVersions), len(afterVersions))
	}
}

func TestPipelineImportRejectsEngineInvalidJSONAndYAML(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        []byte
	}{
		{name: "json", contentType: "application/json", body: mustMarshalPipeline(engineInvalidPipeline("invalid JSON import"))},
		{name: "yaml", contentType: "application/x-yaml", body: []byte("name: invalid YAML import\nnodes:\n  - id: work\n    type: transform\n    name: Work\n    config: {}\nedges: []\n")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newOrgCheckStore(t)
			h := NewPipelineHandler(s, nil)
			req := httptest.NewRequest(http.MethodPost, "/pipelines/import", bytes.NewReader(tt.body))
			req.Header.Set("Content-Type", tt.contentType)
			rec := httptest.NewRecorder()
			h.Import(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			pipelines, err := s.ListPipelines()
			if err != nil {
				t.Fatal(err)
			}
			if len(pipelines) != 0 {
				t.Fatalf("invalid import persisted: %#v", pipelines)
			}
		})
	}
}

func TestPipelineYAMLImportAcceptsOnlyExecutorOwnedCustomType(t *testing.T) {
	yamlBody := []byte("name: custom YAML import\nnodes:\n  - id: source\n    type: source_file\n    name: Source\n    config:\n      path: /tmp/input.csv\n  - id: custom\n    type: custom_yaml\n    name: Custom\n    config: {}\nedges:\n  - from: source\n    to: custom\n")

	t.Run("claimed", func(t *testing.T) {
		s := newOrgCheckStore(t)
		h := NewPipelineHandler(s, nil, &apiValidationExecutor{nodeType: "custom_yaml"})
		req := httptest.NewRequest(http.MethodPost, "/pipelines/import", bytes.NewReader(yamlBody))
		req.Header.Set("Content-Type", "application/x-yaml")
		rec := httptest.NewRecorder()
		h.Import(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("unclaimed", func(t *testing.T) {
		s := newOrgCheckStore(t)
		h := NewPipelineHandler(s, nil)
		req := httptest.NewRequest(http.MethodPost, "/pipelines/import", bytes.NewReader(yamlBody))
		req.Header.Set("Content-Type", "application/x-yaml")
		rec := httptest.NewRecorder()
		h.Import(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "unknown type") {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		pipelines, err := s.ListPipelines()
		if err != nil {
			t.Fatal(err)
		}
		if len(pipelines) != 0 {
			t.Fatalf("unclaimed custom YAML persisted: %#v", pipelines)
		}
	})
}

func mustMarshalPipeline(p *models.Pipeline) []byte {
	b, _ := json.Marshal(p)
	return b
}

func TestPipelineCloneValidatesBeforePersistence(t *testing.T) {
	s := newOrgCheckStore(t)
	original := engineInvalidPipeline("legacy invalid")
	original.ID = "legacy-invalid"
	original.CreatedAt = time.Now().UTC()
	original.UpdatedAt = original.CreatedAt
	if err := s.CreatePipeline(original); err != nil {
		t.Fatal(err)
	}
	h := NewPipelineHandler(s, nil)
	rec := servePipelineHandler(t, http.MethodPost, "/pipelines/{id}/clone", "/pipelines/legacy-invalid/clone", nil, h.Clone)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "unsupported type") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	pipelines, err := s.ListPipelines()
	if err != nil {
		t.Fatal(err)
	}
	if len(pipelines) != 1 {
		t.Fatalf("clone persisted unexpectedly: %#v", pipelines)
	}
}

func TestPipelineExecutorOwnedTypeCreateAndValidate(t *testing.T) {
	s := newOrgCheckStore(t)
	executor := &apiValidationExecutor{nodeType: "custom"}
	h := NewPipelineHandler(s, nil, executor)
	p := &models.Pipeline{
		Name: "custom pipeline",
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceFile, Name: "Source", Config: map[string]interface{}{"path": "/tmp/input.csv"}},
			{ID: "custom", Type: "custom", Name: "Custom", Config: map[string]interface{}{}},
		},
		Edges: []models.Edge{{From: "source", To: "custom"}},
	}
	rec := servePipelineHandler(t, http.MethodPost, "/pipelines", "/pipelines", mustMarshalPipeline(p), h.Create)
	if rec.Code != http.StatusCreated {
		t.Fatalf("custom create status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created models.Pipeline
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	validateRec := servePipelineHandler(t, http.MethodGet, "/pipelines/{id}/validate", "/pipelines/"+created.ID+"/validate", nil, h.Validate)
	if validateRec.Code != http.StatusOK || !strings.Contains(validateRec.Body.String(), `"valid":true`) {
		t.Fatalf("custom validate status=%d body=%s", validateRec.Code, validateRec.Body.String())
	}
}

func TestPipelineCreateRejectsUnsupportedPluginTransformKind(t *testing.T) {
	pluginDir := filepath.Join(t.TempDir(), "transform-plugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"protocol_version":1,"name":"transform","version":"0.1.0","binary":"./bin","node_types":[{"type":"transform_plugin","kind":"transform"}]}`
	if err := os.WriteFile(filepath.Join(pluginDir, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "bin"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	mgr, err := plugins.NewManager(filepath.Dir(pluginDir))
	if err != nil {
		t.Fatal(err)
	}

	s := newOrgCheckStore(t)
	h := NewPipelineHandler(s, nil, mgr)
	p := &models.Pipeline{
		Name: "unsupported plugin transform",
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceFile, Name: "Source", Config: map[string]interface{}{"path": "/tmp/input.csv"}},
			{ID: "transform", Type: "transform_plugin", Name: "Transform", Config: map[string]interface{}{}},
		},
		Edges: []models.Edge{{From: "source", To: "transform"}},
	}
	rec := servePipelineHandler(t, http.MethodPost, "/pipelines", "/pipelines", mustMarshalPipeline(p), h.Create)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "unsupported type") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPipelineValidateEndpointRejectsUnexecutableStoredType(t *testing.T) {
	s := newOrgCheckStore(t)
	p := engineInvalidPipeline("legacy invalid")
	p.ID = "legacy-invalid"
	p.CreatedAt = time.Now().UTC()
	p.UpdatedAt = p.CreatedAt
	if err := s.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	h := NewPipelineHandler(s, nil)
	rec := servePipelineHandler(t, http.MethodGet, "/pipelines/{id}/validate", "/pipelines/legacy-invalid/validate", nil, h.Validate)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"valid":false`) || !strings.Contains(rec.Body.String(), "unsupported type") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRegisterRoutesWiresEngineExecutorsToPipelineHandler(t *testing.T) {
	s := newOrgCheckStore(t)
	e := engine.NewEngine(s)
	t.Cleanup(func() { _ = e.Close(context.Background()) })
	e.Executors = []extensions.NodeExecutor{&apiValidationExecutor{nodeType: "custom"}}
	r := chi.NewRouter()
	RegisterRoutes(r, s, e, sodp.NewServer(), nil, nil, nil)

	p := &models.Pipeline{
		Name: "route custom pipeline",
		Nodes: []models.Node{
			{ID: "source", Type: models.NodeTypeSourceFile, Name: "Source", Config: map[string]interface{}{"path": "/tmp/input.csv"}},
			{ID: "custom", Type: "custom", Name: "Custom", Config: map[string]interface{}{}},
		},
		Edges: []models.Edge{{From: "source", To: "custom"}},
	}
	req := httptest.NewRequest(http.MethodPost, "/api/pipelines", bytes.NewReader(mustMarshalPipeline(p)))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("route-wired custom create status=%d body=%s", rec.Code, rec.Body.String())
	}
}
