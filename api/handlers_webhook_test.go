package api

import (
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

func TestWebhookTrigger_PassesParamsFromJSONBody(t *testing.T) {
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "webhook-params.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	e := engine.NewEngine(s)
	t.Cleanup(func() { _ = e.Close(context.Background()) })

	// Params are asserted on the persisted run record; the source file
	// path does not need substitution for the run to succeed.
	fixedPath := filepath.Join(t.TempDir(), "src.csv")
	if err := os.WriteFile(fixedPath, []byte("id\n1\n"), 0o644); err != nil {
		t.Fatalf("write source csv: %v", err)
	}
	srcNode := models.Node{
		ID: "s1", Type: models.NodeTypeSourceFile, Name: "src",
		Config: map[string]interface{}{"path": fixedPath, "format": "csv"},
	}

	const token = "whk_paramstoken0123456789abcdef012345678"
	if err := s.CreatePipeline(&models.Pipeline{
		ID: "wh-params", Name: "wh-params", Enabled: true,
		WorkspaceID:  models.DefaultWorkspaceID,
		WebhookToken: token,
		Nodes:        []models.Node{srcNode},
		CreatedAt:    time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	webhookLimiter.Lock()
	delete(webhookLimiter.last, "wh-params")
	webhookLimiter.Unlock()

	body := strings.NewReader(`{"params":{"source":"github","env":"prod"}}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/pipelines/wh-params/webhook?token="+token, body)
	req.Header.Set("Content-Type", "application/json")
	req = withURLParam(req, "id", "wh-params")
	webhookTriggerHandler(s, e)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rec.Body.String())
	}
	runID, _ := resp["run_id"].(string)
	if runID == "" {
		t.Fatalf("missing run_id in response: %s", rec.Body.String())
	}
	run, err := s.GetRun(runID)
	if err != nil {
		t.Fatalf("GetRun(%s): %v", runID, err)
	}
	got := run.Params
	if got["source"] != "github" || got["env"] != "prod" {
		t.Fatalf("run params = %v, want source=github env=prod", got)
	}
}

func TestWebhookTrigger_EmptyBodyStillWorks(t *testing.T) {
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "webhook-empty.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	e := engine.NewEngine(s)
	t.Cleanup(func() { _ = e.Close(context.Background()) })

	fixedPath := filepath.Join(t.TempDir(), "src.csv")
	if err := os.WriteFile(fixedPath, []byte("id\n1\n"), 0o644); err != nil {
		t.Fatalf("write source csv: %v", err)
	}
	srcNode := models.Node{
		ID: "s1", Type: models.NodeTypeSourceFile, Name: "src",
		Config: map[string]interface{}{"path": fixedPath, "format": "csv"},
	}
	const token = "whk_emptybodytoken0123456789abcdef012345"
	if err := s.CreatePipeline(&models.Pipeline{
		ID: "wh-empty", Name: "wh-empty", Enabled: true,
		WorkspaceID:  models.DefaultWorkspaceID,
		WebhookToken: token,
		Nodes:        []models.Node{srcNode},
		CreatedAt:    time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	webhookLimiter.Lock()
	delete(webhookLimiter.last, "wh-empty")
	webhookLimiter.Unlock()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/pipelines/wh-empty/webhook?token="+token, nil)
	req = withURLParam(req, "id", "wh-empty")
	webhookTriggerHandler(s, e)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("empty body status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}
}

func TestWebhookTrigger_InvalidJSONBodyRejected(t *testing.T) {
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "webhook-badjson.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	e := engine.NewEngine(s)
	t.Cleanup(func() { _ = e.Close(context.Background()) })

	fixedPath := filepath.Join(t.TempDir(), "src.csv")
	if err := os.WriteFile(fixedPath, []byte("id\n1\n"), 0o644); err != nil {
		t.Fatalf("write source csv: %v", err)
	}
	const token = "whk_badjsontoken0123456789abcdef0123456"
	if err := s.CreatePipeline(&models.Pipeline{
		ID: "wh-badjson", Name: "wh-badjson", Enabled: true,
		WorkspaceID:  models.DefaultWorkspaceID,
		WebhookToken: token,
		Nodes: []models.Node{{
			ID: "s1", Type: models.NodeTypeSourceFile, Name: "src",
			Config: map[string]interface{}{"path": fixedPath, "format": "csv"},
		}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	webhookLimiter.Lock()
	delete(webhookLimiter.last, "wh-badjson")
	webhookLimiter.Unlock()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/pipelines/wh-badjson/webhook?token="+token, strings.NewReader(`{not-json`))
	req = withURLParam(req, "id", "wh-badjson")
	webhookTriggerHandler(s, e)(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid JSON status = %d, want 400, body: %s", rec.Code, rec.Body.String())
	}
}
