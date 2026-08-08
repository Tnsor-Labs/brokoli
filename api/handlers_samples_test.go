package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func newSamplesTestRouter() *chi.Mux {
	r := chi.NewRouter()
	r.Get("/samples/data/{file}", samplesDataHandler())
	return r
}

func TestSamplesDataHandler_ServesEmployeesCSV(t *testing.T) {
	router := newSamplesTestRouter()
	req := httptest.NewRequest(http.MethodGet, "/samples/data/employees.csv", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Errorf("Content-Type = %q, want text/csv prefix", ct)
	}
	if !strings.Contains(rec.Body.String(), "name,email") {
		t.Errorf("expected employees.csv header row, got: %s", rec.Body.String())
	}
}

func TestSamplesDataHandler_ServesOrdersAndProducts(t *testing.T) {
	router := newSamplesTestRouter()

	for _, tc := range []struct {
		file   string
		header string
	}{
		{"orders.csv", "order_id,product,status"},
		{"products.csv", "name,category,price"},
	} {
		req := httptest.NewRequest(http.MethodGet, "/samples/data/"+tc.file, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", tc.file, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), tc.header) {
			t.Errorf("%s: expected header %q, got: %s", tc.file, tc.header, rec.Body.String())
		}
	}
}

func TestSamplesDataHandler_UnknownFileReturns404(t *testing.T) {
	router := newSamplesTestRouter()
	req := httptest.NewRequest(http.MethodGet, "/samples/data/does-not-exist.csv", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestSamplesDataHandler_PathTraversalAttemptReturns404(t *testing.T) {
	router := newSamplesTestRouter()
	// chi URL-decodes the param; embed.FS.ReadFile rejects paths with ".."
	// outright, so this must 404, not escape samples_data/.
	req := httptest.NewRequest(http.MethodGet, "/samples/data/..%2Fhandlers_samples.go", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for a path traversal attempt", rec.Code)
	}
}
