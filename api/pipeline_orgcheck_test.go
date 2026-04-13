package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/store"
)

func newOrgCheckStore(t *testing.T) store.Store {
	t.Helper()
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestPipelineCreate_RejectsEmptyOrgInMultiTenant is the regression test
// for the silent-data-loss bug where users without an org_id in their
// JWT could create pipelines with org_id="" that were then invisible to
// every list/dashboard handler (which filter empty-org rows out to
// prevent cross-tenant leaks). The user saw "pipeline created" but then
// "0 pipelines" in the list — classic surprise.
//
// The fix is requirePipelineOrg() in handlers_pipeline.go, which refuses
// the create with 400 when multi-tenant mode is active and the caller
// has no org.
func TestPipelineCreate_RejectsEmptyOrgInMultiTenant(t *testing.T) {
	// Simulate enterprise mode by installing a non-nil OrgResolverFunc.
	// The resolver body doesn't matter — requirePipelineOrg only checks
	// that the function is set, not what it returns. Restore on exit so
	// other tests aren't affected.
	orig := OrgResolverFunc
	OrgResolverFunc = func(userID string) string { return "" }
	t.Cleanup(func() { OrgResolverFunc = orig })

	s := newOrgCheckStore(t)
	h := NewPipelineHandler(s, nil)

	body, _ := json.Marshal(map[string]any{
		"name":        "orphan",
		"description": "should be rejected",
		"enabled":     true,
		"nodes": []map[string]any{
			{"id": "src", "type": "source_api", "config": map[string]any{"url": "/api/samples/data/employees.csv", "format": "csv"}},
		},
		"edges": []any{},
	})
	req := httptest.NewRequest("POST", "/api/pipelines", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// No org_id in context — simulates a user whose JWT had no org.
	rec := httptest.NewRecorder()
	h.Create(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "organization membership") {
		t.Errorf("response should mention 'organization membership'; got: %s", rec.Body.String())
	}

	// Now repeat with an org_id in context — should succeed.
	req2 := httptest.NewRequest("POST", "/api/pipelines", bytes.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req2.Context(), OrgIDContextKey{}, "org-acme")
	req2 = req2.WithContext(ctx)
	rec2 := httptest.NewRecorder()
	h.Create(rec2, req2)
	if rec2.Code != http.StatusCreated {
		t.Errorf("status: got %d, want 201; body=%s", rec2.Code, rec2.Body.String())
	}
}

// TestPipelineCreate_CommunityModeAllowsEmptyOrg verifies that when
// multi-tenant mode is NOT active (OSS / self-hosted), an empty org_id
// is legitimate and the pipeline is created with workspace scoping
// instead. This is the community-edition path and must not regress.
func TestPipelineCreate_CommunityModeAllowsEmptyOrg(t *testing.T) {
	orig := OrgResolverFunc
	OrgResolverFunc = nil // community mode
	t.Cleanup(func() { OrgResolverFunc = orig })

	s := newOrgCheckStore(t)
	h := NewPipelineHandler(s, nil)

	body, _ := json.Marshal(map[string]any{
		"name":    "community-pipeline",
		"enabled": true,
		"nodes": []map[string]any{
			{"id": "src", "type": "source_api", "config": map[string]any{"url": "/api/samples/data/employees.csv", "format": "csv"}},
		},
		"edges": []any{},
	})
	req := httptest.NewRequest("POST", "/api/pipelines", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.Create(rec, req)

	if rec.Code != http.StatusCreated {
		t.Errorf("status: got %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
}
