package api

// Tests for #109 M2: fail-closed unknown fields with an "extensions"
// escape namespace, the stateless validate endpoint, and the removal of
// the lossy JSON->YAML import fallback.

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func postJSON(t *testing.T, handler http.HandlerFunc, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

const validDoc = `{
	"name": "strict",
	"nodes": [{"id": "src", "type": "source_file", "name": "Src", "config": {"path": "/tmp/in.csv"}}],
	"edges": []
}`

func TestCreateRejectsUnknownTopLevelFieldNamingIt(t *testing.T) {
	s := newOrgCheckStore(t)
	h := NewPipelineHandler(s, nil)
	body := strings.Replace(validDoc, `"name": "strict",`, `"name": "strict", "surprise": 1,`, 1)
	rec := postJSON(t, h.Create, "/pipelines", body)
	if rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "surprise") ||
		!strings.Contains(rec.Body.String(), "extensions") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	pipelines, _ := s.ListPipelines()
	if len(pipelines) != 0 {
		t.Fatal("rejected payload persisted")
	}
}

func TestCreatePersistsAndEchoesExtensionsVerbatim(t *testing.T) {
	s := newOrgCheckStore(t)
	h := NewPipelineHandler(s, nil)
	body := strings.Replace(validDoc, `"name": "strict",`,
		`"name": "strict", "extensions": {"x_next": {"deep": [1, 2]}},`, 1)
	rec := postJSON(t, h.Create, "/pipelines", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"x_next"`) {
		t.Fatalf("extensions not echoed on create: %s", rec.Body.String())
	}
	pipelines, _ := s.ListPipelines()
	stored, err := s.GetPipeline(pipelines[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	// json.Marshal compacts RawMessage whitespace in the store round-trip:
	// preservation is semantic, not byte-for-byte.
	if string(stored.Extensions["x_next"]) != `{"deep":[1,2]}` {
		t.Fatalf("extensions not persisted: %q", stored.Extensions["x_next"])
	}
}

func TestUpdateRejectsUnknownTopLevelField(t *testing.T) {
	s := newOrgCheckStore(t)
	h := NewPipelineHandler(s, nil)
	created := postJSON(t, h.Create, "/pipelines", validDoc)
	if created.Code != http.StatusCreated {
		t.Fatalf("setup create failed: %s", created.Body.String())
	}
	var id string
	{
		body := created.Body.String()
		start := strings.Index(body, `"id":"`) + len(`"id":"`)
		id = body[start : start+strings.Index(body[start:], `"`)]
	}
	r := chi.NewRouter()
	r.Put("/pipelines/{id}", h.Update)
	req := httptest.NewRequest(http.MethodPut, "/pipelines/"+id,
		strings.NewReader(strings.Replace(validDoc, `"name": "strict",`, `"name": "strict", "typo_field": true,`, 1)))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "typo_field") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestImportJSONTypeErrorIs400NotSilentYAML(t *testing.T) {
	// nodes-as-string is the classic type mismatch that used to fall
	// through to the YAML parser (YAML parses JSON) and return 201 with a
	// silently mutilated pipeline.
	s := newOrgCheckStore(t)
	h := NewPipelineHandler(s, nil)
	rec := postJSON(t, h.Import, "/pipelines/import", `{"name": "broken", "nodes": "not-a-list", "edges": []}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s -- JSON type error must not fall back to YAML", rec.Code, rec.Body.String())
	}
	pipelines, _ := s.ListPipelines()
	if len(pipelines) != 0 {
		t.Fatal("mutilated import persisted")
	}
}

func TestImportYAMLStillWorks(t *testing.T) {
	s := newOrgCheckStore(t)
	h := NewPipelineHandler(s, nil)
	yaml := "name: yaml import\nnodes:\n  - id: src\n    type: source_file\n    name: Src\n    config:\n      path: /tmp/in.csv\nedges: []\n"
	req := httptest.NewRequest(http.MethodPost, "/pipelines/import", strings.NewReader(yaml))
	req.Header.Set("Content-Type", "application/x-yaml")
	rec := httptest.NewRecorder()
	h.Import(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("YAML import broke: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestStatelessValidate(t *testing.T) {
	s := newOrgCheckStore(t)
	h := NewPipelineHandler(s, nil)

	rec := postJSON(t, h.ValidateDocument, "/pipelines/validate", validDoc)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"valid":true`) {
		t.Fatalf("valid doc: status=%d body=%s", rec.Code, rec.Body.String())
	}

	invalid := strings.Replace(validDoc, `"type": "source_file"`, `"type": "mystery"`, 1)
	rec = postJSON(t, h.ValidateDocument, "/pipelines/validate", invalid)
	if rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), `"valid":false`) ||
		!strings.Contains(rec.Body.String(), "unsupported type") {
		t.Fatalf("invalid doc: status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = postJSON(t, h.ValidateDocument, "/pipelines/validate",
		strings.Replace(validDoc, `"name": "strict",`, `"name": "strict", "oops": 1,`, 1))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "oops") {
		t.Fatalf("unknown field: status=%d body=%s", rec.Code, rec.Body.String())
	}

	pipelines, _ := s.ListPipelines()
	if len(pipelines) != 0 {
		t.Fatal("stateless validate persisted something")
	}
}
