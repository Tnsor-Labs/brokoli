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

// newWebhookPipeline builds a store, an engine, and one runnable
// pipeline with a webhook token, and clears any limiter state for it.
func newWebhookPipeline(t *testing.T, pipelineID, token string) (store.Store, *engine.Engine) {
	t.Helper()
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "wh.db"))
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
	if err := s.CreatePipeline(&models.Pipeline{
		ID: pipelineID, Name: pipelineID, Enabled: true,
		WorkspaceID:  models.DefaultWorkspaceID,
		WebhookToken: token,
		Nodes: []models.Node{{
			ID: "s1", Type: models.NodeTypeSourceFile, Name: "src",
			Config: map[string]interface{}{"path": srcPath, "format": "csv"},
		}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	webhookLimiter.Lock()
	webhookLimiter.last = make(map[string]time.Time)
	webhookLimiter.Unlock()

	return s, e
}

func postWebhook(t *testing.T, s store.Store, e *engine.Engine, pipelineID, token string) int {
	t.Helper()
	target := "/pipelines/" + pipelineID + "/webhook"
	if token != "" {
		target += "?token=" + token
	}
	req := withURLParam(httptest.NewRequest(http.MethodPost, target, nil), "id", pipelineID)
	rec := httptest.NewRecorder()
	webhookTriggerHandler(s, e)(rec, req)
	return rec.Code
}

// The rate limiter used to be claimed before the token was checked, on
// the raw {id} from the URL. An unauthenticated caller could therefore
// hold a real pipeline's slot: its own request was rejected as
// unauthorized, but only after it had stamped the limiter, so the
// legitimate sender got 429 and the pipeline silently stopped running
// (#534).
func TestWebhookRateLimitIsNotClaimedByUnauthenticatedCallers(t *testing.T) {
	const id, token = "wh-dos", "whk_dostoken0123456789abcdef0123456789"
	s, e := newWebhookPipeline(t, id, token)

	// An attacker who knows only the pipeline id.
	if code := postWebhook(t, s, e, id, "whk_wrongtoken0123456789abcdef012345678"); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated attempt = %d, want 401", code)
	}
	if code := postWebhook(t, s, e, id, ""); code != http.StatusUnauthorized {
		t.Fatalf("attempt with no token = %d, want 401", code)
	}

	// The real sender, immediately afterwards, must still be served.
	if code := postWebhook(t, s, e, id, token); code != http.StatusOK {
		t.Fatalf("the legitimate webhook got %d after two unauthenticated attempts, want 200; "+
			"an anonymous caller must not be able to consume this pipeline's rate-limit slot", code)
	}
}

// The other half of the contract: the limit must still limit.
func TestWebhookRateLimitStillAppliesToAuthenticatedCallers(t *testing.T) {
	const id, token = "wh-limit", "whk_limittoken0123456789abcdef012345678"
	s, e := newWebhookPipeline(t, id, token)

	if code := postWebhook(t, s, e, id, token); code != http.StatusOK {
		t.Fatalf("first trigger = %d, want 200", code)
	}
	if code := postWebhook(t, s, e, id, token); code != http.StatusTooManyRequests {
		t.Fatalf("second trigger = %d, want 429; the interval must still be enforced", code)
	}
}

// An id that resolves to no pipeline must leave nothing behind. This is
// the memory-growth half of #534: the map used to take an entry for
// whatever {id} arrived, from anyone, and nothing removed it.
func TestWebhookLimiterRecordsNothingForUnknownPipelines(t *testing.T) {
	const id, token = "wh-unknown", "whk_unknowntoken0123456789abcdef0123456"
	s, e := newWebhookPipeline(t, id, token)

	for i := 0; i < 5; i++ {
		if code := postWebhook(t, s, e, "no-such-pipeline", "whk_whatever"); code != http.StatusNotFound {
			t.Fatalf("request for a missing pipeline = %d, want 404", code)
		}
	}

	webhookLimiter.Lock()
	n := len(webhookLimiter.last)
	webhookLimiter.Unlock()
	if n != 0 {
		t.Fatalf("limiter holds %d entries after 5 requests for ids that match no pipeline, want 0", n)
	}
}

// Entries that can no longer refuse anything are dropped, so the map
// stays proportional to recently active webhooks rather than to every
// pipeline ever triggered.
func TestClaimWebhookSlotPrunesExpiredEntries(t *testing.T) {
	webhookLimiter.Lock()
	webhookLimiter.last = make(map[string]time.Time)
	webhookLimiter.Unlock()

	base := time.Now()
	for _, id := range []string{"a", "b", "c"} {
		if !claimWebhookSlot(id, base) {
			t.Fatalf("first claim for %q was refused", id)
		}
	}

	// Still inside the interval: nothing is droppable, and a repeat is
	// refused.
	if claimWebhookSlot("a", base.Add(time.Second)) {
		t.Fatal("a repeat claim inside the interval was allowed")
	}
	webhookLimiter.Lock()
	n := len(webhookLimiter.last)
	webhookLimiter.Unlock()
	if n != 3 {
		t.Fatalf("entries inside the interval = %d, want 3 (none are droppable yet)", n)
	}

	// Past the interval, a claim by one pipeline clears the others too.
	if !claimWebhookSlot("a", base.Add(webhookMinInterval+time.Second)) {
		t.Fatal("a claim after the interval was refused")
	}
	webhookLimiter.Lock()
	defer webhookLimiter.Unlock()
	if len(webhookLimiter.last) != 1 {
		t.Fatalf("entries after the sweep = %d, want 1 (only the pipeline that just claimed)", len(webhookLimiter.last))
	}
	if _, ok := webhookLimiter.last["a"]; !ok {
		t.Fatal("the pipeline that just claimed is missing from the limiter")
	}
}
