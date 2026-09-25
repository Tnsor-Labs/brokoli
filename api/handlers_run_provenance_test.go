package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// GET /api/runs/{id}/provenance.
//
// Provenance names the datasets a run touched, with their digests and row
// counts, so the tenant check is the part that matters most here. Tested
// with both organizations set explicitly: ValidateOrgAccess admits any
// request that carries no organization at all (the community edition),
// so a test that only sets one side proves nothing about isolation.

func provenanceHandlerStore(t *testing.T) store.Store {
	t.Helper()
	s, err := store.NewSQLiteStore(t.TempDir() + "/prov.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.CreatePipeline(&models.Pipeline{
		ID: "p1", Name: "p1", Enabled: true, OrgID: "org-a",
		WorkspaceID: models.DefaultWorkspaceID,
		Nodes:       []models.Node{{ID: "src", Type: models.NodeTypeSourceFile, Name: "src"}},
		CreatedAt:   time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
	started := time.Now().UTC()
	for _, id := range []string{"run-with", "run-without"} {
		if err := s.CreateRun(&models.Run{
			ID: id, PipelineID: "p1", Status: models.RunStatusSuccess, StartedAt: &started,
		}); err != nil {
			t.Fatalf("create run %s: %v", id, err)
		}
	}
	if err := s.SaveNodeProvenance(&models.NodeProvenance{
		RunID: "run-with", NodeID: "src",
		Output: &models.DatasetFact{Digest: "sha256:abc", RowCount: 3, Columns: []string{"id"}},
	}); err != nil {
		t.Fatalf("save provenance: %v", err)
	}
	return s
}

func provenanceRequest(runID, orgID string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/runs/"+runID+"/provenance", nil)
	if orgID != "" {
		r = r.WithContext(context.WithValue(r.Context(), OrgIDContextKey{}, orgID))
	}
	return withURLParam(r, "id", runID)
}

func TestProvenanceIsReturnedToItsOwnOrganization(t *testing.T) {
	h := &RunHandler{store: provenanceHandlerStore(t)}

	rec := httptest.NewRecorder()
	h.GetProvenance(rec, provenanceRequest("run-with", "org-a"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var got RunProvenanceResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if !got.Recorded {
		t.Error("recorded = false for a run that has provenance")
	}
	if len(got.Nodes) != 1 || got.Nodes[0].NodeID != "src" {
		t.Fatalf("nodes = %+v, want the one record", got.Nodes)
	}
	if out := got.Nodes[0].Output; out == nil || out.Digest != "sha256:abc" || out.RowCount != 3 {
		t.Errorf("output = %+v, want the stored digest and row count", out)
	}
}

// Another organization gets 404, not 403 and not the data. A 403 would
// confirm that the run id exists somewhere, which is information the
// caller should not have either.
func TestProvenanceIsHiddenFromAnotherOrganization(t *testing.T) {
	h := &RunHandler{store: provenanceHandlerStore(t)}

	rec := httptest.NewRecorder()
	h.GetProvenance(rec, provenanceRequest("run-with", "org-b"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); strings.Contains(body, "sha256:abc") {
		t.Errorf("another organization's digest leaked into the error body: %s", body)
	}
}

func TestProvenanceForAnUnknownRunIsNotFound(t *testing.T) {
	h := &RunHandler{store: provenanceHandlerStore(t)}

	rec := httptest.NewRecorder()
	h.GetProvenance(rec, provenanceRequest("no-such-run", "org-a"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// A run with no provenance answers recorded:false with an empty list,
// never null. A client must be able to tell "a run from before this
// existed" from a malformed response.
func TestARunWithoutProvenanceSaysSo(t *testing.T) {
	h := &RunHandler{store: provenanceHandlerStore(t)}

	rec := httptest.NewRecorder()
	h.GetProvenance(rec, provenanceRequest("run-without", "org-a"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if string(raw["nodes"]) != "[]" {
		t.Errorf("nodes = %s, want [] rather than null", raw["nodes"])
	}
	if string(raw["recorded"]) != "false" {
		t.Errorf("recorded = %s, want false", raw["recorded"])
	}
}
