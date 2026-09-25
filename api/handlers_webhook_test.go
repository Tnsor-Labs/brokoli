package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
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
	// though both correctly reject functionally (now as 404 per #542).
	wrongSameLen := "whk_wrongwrongtoken0123456789abcdef01234"
	if len(wrongSameLen) != len(token) {
		t.Fatalf("test setup: wrongSameLen must match token length (%d vs %d)", len(wrongSameLen), len(token))
	}
	if code, body := trigger("wh-pipe", wrongSameLen); code != http.StatusNotFound {
		t.Errorf("wrong same-length token: status = %d, want 404, body: %s", code, body)
	}
	if code, body := trigger("wh-pipe", ""); code != http.StatusNotFound {
		t.Errorf("empty token against configured webhook: status = %d, want 404, body: %s", code, body)
	}
	// Pipeline with no webhook configured (WebhookToken == "") must never
	// be triggerable, including via an empty provided token -- this is
	// the case where ValidateWebhookToken("", "") == true would be
	// dangerous if reached; the handler must reject it before comparing.
	if code, body := trigger("wh-pipe-nohook", ""); code != http.StatusNotFound {
		t.Errorf("no webhook configured, empty token: status = %d, want 404, body: %s", code, body)
	}
}

// TestWebhookTrigger_HidesExistenceOracle pins #542: missing pipeline,
// configured-but-wrong-token, and no-webhook-configured must return the
// same status and body so an unauthenticated caller cannot tell them apart.
// The real reason stays in the server log only.
func TestWebhookTrigger_HidesExistenceOracle(t *testing.T) {
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "webhook-oracle.db"))
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

	const token = "whk_oracle_token_0123456789abcdef01234567"
	if err := s.CreatePipeline(&models.Pipeline{
		ID: "wh-oracle", Name: "wh-oracle", Enabled: true,
		WorkspaceID:  models.DefaultWorkspaceID,
		WebhookToken: token,
		Nodes:        []models.Node{srcNode},
		CreatedAt:    time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
	if err := s.CreatePipeline(&models.Pipeline{
		ID: "wh-oracle-nohook", Name: "wh-oracle-nohook", Enabled: true,
		WorkspaceID: models.DefaultWorkspaceID,
		Nodes:       []models.Node{srcNode},
		CreatedAt:   time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	var logBuf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(prev) })

	trigger := func(pipelineID, providedToken string) (int, string) {
		webhookLimiter.Lock()
		delete(webhookLimiter.last, pipelineID)
		webhookLimiter.Unlock()

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/pipelines/"+pipelineID+"/webhook?token="+providedToken, nil)
		req = withURLParam(req, "id", pipelineID)
		webhookTriggerHandler(s, e)(rec, req)
		return rec.Code, rec.Body.String()
	}

	missingCode, missingBody := trigger("does-not-exist", "garbage")
	nohookCode, nohookBody := trigger("wh-oracle-nohook", "garbage")
	wrongCode, wrongBody := trigger("wh-oracle", "whk_wrong_token_xxxxxxxxxxxxxxxxxxxxxxx")

	if missingCode != http.StatusNotFound || nohookCode != http.StatusNotFound || wrongCode != http.StatusNotFound {
		t.Fatalf("statuses = missing %d, nohook %d, wrong %d; want all 404", missingCode, nohookCode, wrongCode)
	}
	if missingBody != nohookBody || missingBody != wrongBody {
		t.Fatalf("bodies differ:\n missing=%q\n nohook=%q\n wrong=%q", missingBody, nohookBody, wrongBody)
	}

	if code, body := trigger("wh-oracle", token); code != http.StatusOK {
		t.Errorf("correct token: status = %d, want 200, body: %s", code, body)
	}

	logs := logBuf.String()
	for _, want := range []string{
		`webhook "does-not-exist": pipeline not found`,
		`webhook "wh-oracle-nohook": webhook not configured`,
		`webhook "wh-oracle": invalid webhook token`,
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("server log missing %q; got:\n%s", want, logs)
		}
	}
}

func TestWebhookTrigger_PassesTypedParametersFromJSONBody(t *testing.T) {
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "webhook-params.db"))
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
	const token = "whk_paramstoken0123456789abcdef012345678"
	if err := s.CreatePipeline(&models.Pipeline{
		ID: "wh-params", Name: "wh-params", Enabled: true,
		WorkspaceID:  models.DefaultWorkspaceID,
		WebhookToken: token,
		Nodes: []models.Node{{
			ID: "s1", Type: models.NodeTypeSourceFile, Name: "src",
			Config: map[string]interface{}{"path": fixedPath, "format": "csv"},
		}},
		Parameters: map[string]interface{}{
			"region":    map[string]interface{}{"type": map[string]interface{}{"kind": "string"}, "required": true},
			"threshold": map[string]interface{}{"type": map[string]interface{}{"kind": "float64"}, "required": false, "default": 0.5},
		},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	webhookLimiter.Lock()
	delete(webhookLimiter.last, "wh-params")
	webhookLimiter.Unlock()

	body := strings.NewReader(`{"parameters":{"region":"us-east","threshold":0.9}}`)
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
	if run.Parameters["region"] != "us-east" {
		t.Fatalf("run parameters[region] = %v, want us-east", run.Parameters["region"])
	}
	if run.Parameters["threshold"] != 0.9 {
		t.Fatalf("run parameters[threshold] = %v, want 0.9", run.Parameters["threshold"])
	}
	if len(run.Params) != 0 {
		t.Fatalf("legacy Params = %v, want empty (webhooks must not accept untyped params)", run.Params)
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
	const token = "whk_emptybodytoken0123456789abcdef012345"
	if err := s.CreatePipeline(&models.Pipeline{
		ID: "wh-empty", Name: "wh-empty", Enabled: true,
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
	req.Header.Set("Content-Type", "application/json")
	req = withURLParam(req, "id", "wh-badjson")
	webhookTriggerHandler(s, e)(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid JSON status = %d, want 400, body: %s", rec.Code, rec.Body.String())
	}
}

func TestWebhookTrigger_NonJSONContentTypeIgnoresBody(t *testing.T) {
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "webhook-plain.db"))
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
	const token = "whk_plaintexttoken0123456789abcdef01234"
	if err := s.CreatePipeline(&models.Pipeline{
		ID: "wh-plain", Name: "wh-plain", Enabled: true,
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
	delete(webhookLimiter.last, "wh-plain")
	webhookLimiter.Unlock()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/pipelines/wh-plain/webhook?token="+token, strings.NewReader(`not-json-at-all`))
	req.Header.Set("Content-Type", "text/plain")
	req = withURLParam(req, "id", "wh-plain")
	webhookTriggerHandler(s, e)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("non-JSON body status = %d, want 200 (ignored), body: %s", rec.Code, rec.Body.String())
	}
}

func TestWebhookTrigger_OversizedBodyRejected(t *testing.T) {
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "webhook-big.db"))
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
	const token = "whk_oversizedtoken0123456789abcdef012345"
	if err := s.CreatePipeline(&models.Pipeline{
		ID: "wh-big", Name: "wh-big", Enabled: true,
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
	delete(webhookLimiter.last, "wh-big")
	webhookLimiter.Unlock()

	// Just over 64 KiB of JSON.
	big := `{"parameters":{"region":"` + strings.Repeat("x", 64<<10) + `"}}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/pipelines/wh-big/webhook?token="+token, strings.NewReader(big))
	req.Header.Set("Content-Type", "application/json")
	req = withURLParam(req, "id", "wh-big")
	webhookTriggerHandler(s, e)(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge && rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized body status = %d, want 413 or 400, body: %s", rec.Code, rec.Body.String())
	}
}
