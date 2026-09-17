package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/go-chi/chi/v5"
)

// TestResumeHandler_FromNodeSelectsTheNodeScopedPath pins the branch the
// resume endpoint gained: an absent or empty from_node must behave exactly
// as the endpoint always has, and a supplied one must reach
// Engine.ResumeRunFromNode instead.
//
// The two paths are told apart by the refusal each produces against the
// same run, which is what makes this a test of the branch rather than of
// the engine: a succeeded run is refused by the plain path because it is
// not failed, and by the node-scoped path only because the node does not
// exist.
func TestResumeHandler_FromNodeSelectsTheNodeScopedPath(t *testing.T) {
	withoutOrgResolver(t)
	s := newDepAPIStore(t)
	e := engine.NewEngine(s)
	t.Cleanup(func() { _ = e.Close(context.Background()) })
	h := NewRunHandler(s, e)

	pipe := createOrgPipe(t, s, "resume-pipe", "Resume Pipeline", "org-a", nil)
	pipe.Nodes = []models.Node{{ID: "src", Type: models.NodeTypeSourceFile, Name: "S",
		Config: map[string]interface{}{"path": "in.csv", "format": "csv"}}}
	if err := s.UpdatePipeline(pipe); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRun(&models.Run{ID: "run-1", PipelineID: pipe.ID, Status: models.RunStatusSuccess}); err != nil {
		t.Fatal(err)
	}

	router := chi.NewRouter()
	router.Post("/runs/{id}/resume", h.ResumeRun)

	post := func(body string) (int, string) {
		t.Helper()
		var payload []byte
		if body != "" {
			payload = []byte(body)
		}
		req := reqWithOrg("POST", "/runs/run-1/resume", payload, "org-a")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	// No body at all: the historical behaviour, failed runs only.
	code, body := post("")
	if code != http.StatusBadRequest {
		t.Fatalf("no body: status %d, body %s", code, body)
	}
	if !strings.Contains(body, "can only resume failed runs") {
		t.Errorf("no body must take the plain path, got: %s", body)
	}

	// An empty from_node is the same as not sending one. This is the half
	// of the branch that a reader is most likely to get wrong.
	code, body = post(`{"from_node":""}`)
	if code != http.StatusBadRequest {
		t.Fatalf("empty from_node: status %d, body %s", code, body)
	}
	if !strings.Contains(body, "can only resume failed runs") {
		t.Errorf("an empty from_node must take the plain path, got: %s", body)
	}

	// A supplied from_node reaches the node-scoped path, which accepts a
	// succeeded run and refuses only because no such node exists.
	code, body = post(`{"from_node":"nope"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("from_node: status %d, body %s", code, body)
	}
	if !strings.Contains(body, "no such node") {
		t.Errorf("a supplied from_node must take the node-scoped path, got: %s", body)
	}
	if strings.Contains(body, "can only resume failed runs") {
		t.Errorf("a supplied from_node must not fall through to the plain path, got: %s", body)
	}
}

// TestResumeHandler_FromNodeStillOrgScoped confirms the new body parameter
// did not open a way around the org check: the refusal must happen before
// the engine is reached at all.
func TestResumeHandler_FromNodeStillOrgScoped(t *testing.T) {
	withoutOrgResolver(t)
	s := newDepAPIStore(t)
	// nil engine: a denied request must never reach it, so a panic here
	// would mean the check was skipped.
	h := NewRunHandler(s, nil)

	pipe := createOrgPipe(t, s, "other-pipe", "Other Pipeline", "org-b", nil)
	if err := s.CreateRun(&models.Run{ID: "run-b", PipelineID: pipe.ID, Status: models.RunStatusFailed}); err != nil {
		t.Fatal(err)
	}

	router := chi.NewRouter()
	router.Post("/runs/{id}/resume", h.ResumeRun)

	req := reqWithOrg("POST", "/runs/run-b/resume", []byte(`{"from_node":"src"}`), "org-a")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code == http.StatusAccepted {
		t.Fatalf("a cross-org resume was accepted: %s", rec.Body.String())
	}
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusForbidden {
		t.Errorf("cross-org resume status = %d, want the org denial", rec.Code)
	}
}
