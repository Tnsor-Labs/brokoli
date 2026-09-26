package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

/*
 * A transform rule that cannot run must be refused when the pipeline is
 * SAVED, through the API, not only by a validator somewhere.
 *
 * v0.13.0 promised exactly that and did not deliver it: the check was
 * wired into the per-node detail validator, while Create and Update call
 * ValidatePipeline. Unit tests that called the detail validator passed; a
 * live test that created the pipeline through this handler got 201. These
 * tests go through the handler so they cannot pass that way again.
 */

func saveViaAPI(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	h := NewPipelineHandler(newOrgCheckStore(t), nil)
	req := httptest.NewRequest(http.MethodPost, "/api/pipelines", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.Create(rec, req)
	return rec
}

func transformPipeline(rule string) string {
	return `{
		"name":"save-validation",
		"nodes":[
			{"id":"src","type":"source_api","name":"Users","config":{"url":"https://example.com/users"}},
			{"id":"t","type":"transform","name":"T","config":{"rules":[` + rule + `]}}
		],
		"edges":[{"from":"src","to":"t"}]
	}`
}

func TestPipelineCreate_RefusesASortWithoutColumns(t *testing.T) {
	rec := saveViaAPI(t, transformPipeline(`{"type":"sort"}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: an unrunnable rule must be refused on save; body=%s",
			rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "sort requires columns list") {
		t.Errorf("body %q does not name the missing requirement", rec.Body.String())
	}
}

func TestPipelineCreate_RefusesAMisspelledRuleType(t *testing.T) {
	rec := saveViaAPI(t, transformPipeline(`{"type":"sorrt","columns":["id"]}`))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "unsupported transform type") {
		t.Fatalf("status = %d body=%s; want 400 naming the unsupported type", rec.Code, rec.Body.String())
	}
}

// The control: the refusal must be about the rule, not about transforms.
func TestPipelineCreate_SavesAWellFormedSort(t *testing.T) {
	rec := saveViaAPI(t, transformPipeline(`{"type":"sort","columns":["id"]}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 for a well-formed rule; body=%s", rec.Code, rec.Body.String())
	}
}
