package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// #106 made persistence fail-closed, which left no way to start a
// pipeline from scratch: a create with no nodes, or with an unconfigured
// one, is rejected. A draft skips executable validation on save and in
// exchange cannot run (#107).

func draftHandler(t *testing.T) (*PipelineHandler, store.Store) {
	t.Helper()
	s := newOrgCheckStore(t)
	return NewPipelineHandler(s, nil), s
}

func createPipeline(t *testing.T, h *PipelineHandler, body string) (int, string) {
	t.Helper()
	rec := servePipelineHandler(t, http.MethodPost, "/pipelines", "/pipelines", []byte(body), h.Create)
	return rec.Code, rec.Body.String()
}

// The reported case: start a pipeline with nothing in it.
func TestCreateDraftWithNoNodesSucceeds(t *testing.T) {
	h, s := draftHandler(t)

	code, body := createPipeline(t, h, `{"name":"from scratch","draft":true,"nodes":[],"edges":[]}`)
	if code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body: %s", code, body)
	}

	var created models.Pipeline
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !created.Draft {
		t.Error("the created pipeline is not marked as a draft")
	}

	stored, err := s.GetPipeline(created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !stored.Draft {
		t.Error("Draft did not survive persistence")
	}
	if len(stored.Nodes) != 0 {
		t.Errorf("stored %d nodes, want 0", len(stored.Nodes))
	}
}

// A draft may be incomplete in any way, not merely empty: half-built is
// the state this exists for.
func TestCreateDraftWithUnconfiguredNodeSucceeds(t *testing.T) {
	h, _ := draftHandler(t)
	code, body := createPipeline(t, h,
		`{"name":"half built","draft":true,"nodes":[{"id":"n1","type":"source_file","name":"Source","config":{}}],"edges":[]}`)
	if code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body: %s", code, body)
	}
}

// The exchange: a non-draft is still held to full validation, so drafts
// buy leniency for themselves and not for everyone.
func TestCreateNonDraftStillValidates(t *testing.T) {
	h, _ := draftHandler(t)

	code, body := createPipeline(t, h, `{"name":"empty","nodes":[],"edges":[]}`)
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a non-draft with no nodes", code)
	}
	if !strings.Contains(body, "at least one node") {
		t.Errorf("error = %s, which does not say why", body)
	}

	code, body = createPipeline(t, h,
		`{"name":"unconfigured","nodes":[{"id":"n1","type":"source_file","name":"S","config":{}}],"edges":[]}`)
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a non-draft with an unconfigured node, body: %s", code, body)
	}
}

func TestPublishingValidates(t *testing.T) {
	h, s := draftHandler(t)

	code, body := createPipeline(t, h, `{"name":"wip","draft":true,"nodes":[],"edges":[]}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	var created models.Pipeline
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Publishing an empty pipeline must fail with the same message
	// Create would have given, so the two cannot drift.
	publish := `{"id":"` + created.ID + `","name":"wip","draft":false,"nodes":[],"edges":[]}`
	rec := servePipelineHandler(t, http.MethodPut, "/pipelines/{id}",
		"/pipelines/"+created.ID, []byte(publish), h.Update)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("publishing an empty draft returned %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "at least one node") {
		t.Errorf("error = %s", rec.Body.String())
	}

	// It is still a draft afterwards: a rejected publish must not half
	// apply.
	stored, err := s.GetPipeline(created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !stored.Draft {
		t.Error("a rejected publish cleared the draft flag anyway")
	}

	// With a real node it goes through.
	good := `{"id":"` + created.ID + `","name":"wip","draft":false,"nodes":[` +
		`{"id":"n1","type":"source_file","name":"S","config":{"path":"/tmp/x.csv"}}],"edges":[]}`
	rec = servePipelineHandler(t, http.MethodPut, "/pipelines/{id}",
		"/pipelines/"+created.ID, []byte(good), h.Update)
	if rec.Code != http.StatusOK {
		t.Fatalf("publishing a valid draft returned %d: %s", rec.Code, rec.Body.String())
	}
	stored, err = s.GetPipeline(created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if stored.Draft {
		t.Error("still a draft after a successful publish")
	}
}

// Saving a draft again, still incomplete, must keep working. This is
// "come back to it tomorrow".
func TestUpdatingADraftSkipsValidation(t *testing.T) {
	h, _ := draftHandler(t)
	code, body := createPipeline(t, h, `{"name":"wip","draft":true,"nodes":[],"edges":[]}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	var created models.Pipeline
	_ = json.Unmarshal([]byte(body), &created)

	// Add one unconfigured node and save. Still a draft, still invalid,
	// still saved.
	update := `{"id":"` + created.ID + `","name":"wip","draft":true,"nodes":[` +
		`{"id":"n1","type":"source_file","name":"S","config":{}}],"edges":[]}`
	rec := servePipelineHandler(t, http.MethodPut, "/pipelines/{id}",
		"/pipelines/"+created.ID, []byte(update), h.Update)
	if rec.Code != http.StatusOK {
		t.Fatalf("saving an incomplete draft returned %d: %s", rec.Code, rec.Body.String())
	}
}

// Going back to draft would mean a scheduled pipeline silently stops
// running because someone ticked a box. Enabled already means "stop
// running this", and says so on the row.
func TestPublishedPipelineCannotReturnToDraft(t *testing.T) {
	h, _ := draftHandler(t)
	code, body := createPipeline(t, h,
		`{"name":"live","nodes":[{"id":"n1","type":"source_file","name":"S","config":{"path":"/tmp/x.csv"}}],"edges":[]}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	var created models.Pipeline
	_ = json.Unmarshal([]byte(body), &created)

	revert := `{"id":"` + created.ID + `","name":"live","draft":true,"nodes":[` +
		`{"id":"n1","type":"source_file","name":"S","config":{"path":"/tmp/x.csv"}}],"edges":[]}`
	rec := servePipelineHandler(t, http.MethodPut, "/pipelines/{id}",
		"/pipelines/"+created.ID, []byte(revert), h.Update)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("returning a published pipeline to draft returned %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "disable it instead") {
		t.Errorf("error = %s, which does not point at the alternative", rec.Body.String())
	}
}

// Refusing a draft is a precondition failure, not a server fault.
// Returning 500 would make "not finished yet" look like a bug to
// anything watching error rates.
func TestTriggeringADraftReturns409(t *testing.T) {
	s := newOrgCheckStore(t)
	eng := engine.NewEngine(s)
	t.Cleanup(func() { _ = eng.Close(context.Background()) })
	h := NewRunHandler(s, eng)

	p := &models.Pipeline{
		ID: "d1", Name: "d1", Enabled: true, Draft: true,
		WorkspaceID: models.DefaultWorkspaceID,
		Nodes:       []models.Node{{ID: "n", Type: models.NodeTypeSourceFile, Name: "S"}},
		CreatedAt:   time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := s.CreatePipeline(p); err != nil {
		t.Fatalf("create: %v", err)
	}

	rec := servePipelineHandler(t, http.MethodPost, "/pipelines/{id}/run",
		"/pipelines/d1/run", []byte(`{}`), h.TriggerRun)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "draft") {
		t.Errorf("body = %s, which does not say why", rec.Body.String())
	}
}
