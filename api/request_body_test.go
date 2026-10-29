package api

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeJSONBoundsTheBody(t *testing.T) {
	decode := func(body io.Reader) error {
		var v map[string]any
		return decodeJSON(httptest.NewRecorder(), httptest.NewRequest("POST", "/", body), &v)
	}
	if err := decode(strings.NewReader(`{"name":"ok"}`)); err != nil {
		t.Fatalf("a small body: %v", err)
	}
	if err := decode(strings.NewReader("")); !errors.Is(err, io.EOF) {
		t.Errorf("an empty body = %v, want io.EOF so optional bodies still work", err)
	}
	// A JSON string one byte past the limit: well formed, just too big.
	big := `{"name":"` + strings.Repeat("x", maxJSONBody) + `"}`
	var tooLarge *http.MaxBytesError
	if err := decode(strings.NewReader(big)); !errors.As(err, &tooLarge) {
		t.Errorf("a body past the limit = %v, want *http.MaxBytesError", err)
	}
}

// Creating a pipeline decoded the whole body, however large, before
// looking at it.
func TestCreatePipelineRefusesAnOversizedBody(t *testing.T) {
	h := NewPipelineHandler(newDepAPIStore(t), nil)
	body := `{"name":"big","nodes":[],"edges":[],"description":"` + strings.Repeat("x", maxJSONBody) + `"}`
	rec := httptest.NewRecorder()
	h.Create(rec, httptest.NewRequest("POST", "/pipelines", strings.NewReader(body)))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "request body too large") {
		t.Fatalf("status = %d, body %.200s; want 400 for the size", rec.Code, rec.Body.String())
	}
}
