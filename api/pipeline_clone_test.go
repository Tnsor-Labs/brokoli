package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

func validClonablePipeline(name string) *models.Pipeline {
	return &models.Pipeline{
		Name: name,
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceFile, Name: "Source", Config: map[string]interface{}{"path": "/tmp/in.csv", "format": "csv"}},
		},
		Edges: []models.Edge{},
	}
}

func clonePipeline(t *testing.T, h *PipelineHandler, id string) (int, models.Pipeline) {
	t.Helper()
	rec := servePipelineHandler(t, http.MethodPost, "/pipelines/{id}/clone", "/pipelines/"+id+"/clone", nil, h.Clone)
	var out models.Pipeline
	if rec.Code == http.StatusCreated {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
	} else {
		t.Logf("clone status=%d body=%s", rec.Code, rec.Body.String())
	}
	return rec.Code, out
}

// A pipeline created in the UI gets a pipeline_id slug from its name, and
// the slug is unique within the workspace. Cloning it copied the slug, so
// every clone failed with a 500 (#590). Each clone now gets its own slug,
// and none of them takes the original's webhook token.
func TestCloningAUICreatedPipelineGivesEachCopyItsOwnSlug(t *testing.T) {
	s := newOrgCheckStore(t)
	h := NewPipelineHandler(s, nil)
	body := validClonablePipeline("Daily Orders")
	body.WebhookToken = "original-secret"
	rec := servePipelineHandler(t, http.MethodPost, "/pipelines", "/pipelines", mustMarshalPipeline(body), h.Create)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
	var orig models.Pipeline
	if err := json.Unmarshal(rec.Body.Bytes(), &orig); err != nil {
		t.Fatal(err)
	}
	if orig.PipelineID != "daily-orders" {
		t.Fatalf("created pipeline_id = %q, want daily-orders", orig.PipelineID)
	}

	want := []string{"daily-orders-copy", "daily-orders-copy-2", "daily-orders-copy-3"}
	for _, slug := range want {
		code, copy := clonePipeline(t, h, orig.ID)
		if code != http.StatusCreated {
			t.Fatalf("clone status = %d, want 201", code)
		}
		if copy.PipelineID != slug {
			t.Errorf("clone pipeline_id = %q, want %q", copy.PipelineID, slug)
		}
		if copy.ID == orig.ID {
			t.Error("the clone kept the original's id")
		}
		stored, err := s.GetPipeline(copy.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.WebhookToken != "" {
			t.Errorf("clone %q carries the original's webhook token", slug)
		}
	}
	if again, _ := s.GetPipeline(orig.ID); again.PipelineID != "daily-orders" || again.WebhookToken != "original-secret" {
		t.Fatalf("the original changed: %+v", again)
	}
}

// A copy exists only in Brokoli, so it is a UI pipeline even when the
// original is managed in Git, where it would refuse every edit.
func TestCloningAGitManagedPipelineGivesAUIPipeline(t *testing.T) {
	s := newOrgCheckStore(t)
	h := NewPipelineHandler(s, nil)
	p := validClonablePipeline("From Git")
	p.ID, p.PipelineID, p.Source = "git-one", "from-git", models.PipelineSourceGit
	p.CreatedAt, p.UpdatedAt = time.Now().UTC(), time.Now().UTC()
	if err := s.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	code, copy := clonePipeline(t, h, "git-one")
	if code != http.StatusCreated || copy.Source != models.PipelineSourceUI || copy.PipelineID != "from-git-copy" {
		t.Fatalf("clone status=%d source=%q pipeline_id=%q, want 201, ui, from-git-copy", code, copy.Source, copy.PipelineID)
	}
}

// A pipeline without a slug clones without one: an empty pipeline_id is
// never unique-checked, so it cannot collide.
func TestCloningAPipelineWithoutASlugKeepsItEmpty(t *testing.T) {
	s := newOrgCheckStore(t)
	h := NewPipelineHandler(s, nil)
	p := validClonablePipeline("No slug")
	p.ID = "no-slug"
	p.CreatedAt, p.UpdatedAt = time.Now().UTC(), time.Now().UTC()
	if err := s.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if code, copy := clonePipeline(t, h, "no-slug"); code != http.StatusCreated || copy.PipelineID != "" {
			t.Fatalf("clone %d: status=%d pipeline_id=%q", i, code, copy.PipelineID)
		}
	}
}

// When every copy slug is taken, the clone is refused with 409, the same
// answer Create gives for a taken slug, not a 500.
func TestCloningWhenEveryCopySlugIsTakenIsAConflict(t *testing.T) {
	s := newOrgCheckStore(t)
	h := NewPipelineHandler(s, nil)
	// Through Create, so every pipeline is in the caller's workspace, where
	// the slug is unique.
	mk := func(name, slug string) string {
		p := validClonablePipeline(name)
		p.PipelineID = slug
		rec := servePipelineHandler(t, http.MethodPost, "/pipelines", "/pipelines", mustMarshalPipeline(p), h.Create)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create %q: status=%d body=%s", slug, rec.Code, rec.Body.String())
		}
		var out models.Pipeline
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.ID
	}
	orig := mk("busy", "busy")
	mk("busy copy", "busy-copy")
	for i := 2; i <= maxCloneSlugAttempts; i++ {
		mk(fmt.Sprintf("busy copy %d", i), fmt.Sprintf("busy-copy-%d", i))
	}
	if code, _ := clonePipeline(t, h, orig); code != http.StatusConflict {
		t.Fatalf("clone status = %d, want 409", code)
	}
}
