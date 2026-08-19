package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// TestWebhookTrigger_UsesConstantTimeTokenCompare pins the fix for
// ADR-022 finding #7: webhookTriggerHandler compared the caller-supplied
// token against the pipeline's stored WebhookToken with a plain `!=`,
// which short-circuits on the first mismatched byte and so leaks timing
// information an attacker could use to recover the token byte-by-byte.
// The handler must use engine.ValidateWebhookToken (subtle.ConstantTimeCompare)
// instead. This test can't observe timing directly, so it pins the
// externally-visible contract instead: correct token succeeds, wrong
// token of the same length is still rejected, and a pipeline with no
// webhook configured is never triggerable via an empty token.
func TestWebhookTrigger_UsesConstantTimeTokenCompare(t *testing.T) {
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "webhook.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	e := engine.NewEngine(s)
	t.Cleanup(func() { _ = e.Close(context.Background()) })

	srcPath := filepath.Join(t.TempDir(), "src.csv")
	if err := os.WriteFile(srcPath, []byte("id\n1\n"), 0o644); err != nil {
		t.Fatalf("write source csv: %v", err)
	}
	srcNode := models.Node{
		ID: "s1", Type: models.NodeTypeSourceFile, Name: "src",
		Config: map[string]interface{}{"path": srcPath, "format": "csv"},
	}

	const token = "whk_correcttoken0123456789abcdef01234567"
	if err := s.CreatePipeline(&models.Pipeline{
		ID: "wh-pipe", Name: "wh", Enabled: true,
		WorkspaceID:  models.DefaultWorkspaceID,
		WebhookToken: token,
		Nodes:        []models.Node{srcNode},
		CreatedAt:    time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
	if err := s.CreatePipeline(&models.Pipeline{
		ID: "wh-pipe-nohook", Name: "wh-nohook", Enabled: true,
		WorkspaceID: models.DefaultWorkspaceID,
		Nodes:       []models.Node{srcNode},
		CreatedAt:   time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	trigger := func(pipelineID, providedToken string) (int, string) {
		// Each call exercises the token-comparison path in isolation, not
		// the per-pipeline rate limiter (covered elsewhere) -- clear any
		// entry left by a prior call in this test.
		webhookLimiter.Lock()
		delete(webhookLimiter.last, pipelineID)
		webhookLimiter.Unlock()

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/pipelines/"+pipelineID+"/webhook?token="+providedToken, nil)
		req = withURLParam(req, "id", pipelineID)
		webhookTriggerHandler(s, e)(rec, req)
		return rec.Code, rec.Body.String()
	}

	if code, body := trigger("wh-pipe", token); code != http.StatusOK {
		t.Errorf("correct token: status = %d, want 200, body: %s", code, body)
	}
	// Same-length wrong token: this is exactly the case a `!=` vs.
	// constant-time comparison distinguishes at the timing level, even
	// though both correctly return 401 functionally.
	wrongSameLen := "whk_wrongwrongtoken0123456789abcdef01234"
	if len(wrongSameLen) != len(token) {
		t.Fatalf("test setup: wrongSameLen must match token length (%d vs %d)", len(wrongSameLen), len(token))
	}
	if code, body := trigger("wh-pipe", wrongSameLen); code != http.StatusUnauthorized {
		t.Errorf("wrong same-length token: status = %d, want 401, body: %s", code, body)
	}
	if code, body := trigger("wh-pipe", ""); code != http.StatusUnauthorized {
		t.Errorf("empty token against configured webhook: status = %d, want 401, body: %s", code, body)
	}
	// Pipeline with no webhook configured (WebhookToken == "") must never
	// be triggerable, including via an empty provided token -- this is
	// the case where ValidateWebhookToken("", "") == true would be
	// dangerous if reached; the handler must reject it before comparing.
	if code, body := trigger("wh-pipe-nohook", ""); code != http.StatusForbidden {
		t.Errorf("no webhook configured, empty token: status = %d, want 403, body: %s", code, body)
	}
}
