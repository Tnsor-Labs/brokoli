package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

func newSamplesTestRouter() *chi.Mux {
	r := chi.NewRouter()
	r.Get("/samples/data/{file}", samplesDataHandler())
	return r
}

func TestSamplesDataHandler_ServesEmployeesJSON(t *testing.T) {
	router := newSamplesTestRouter()
	req := httptest.NewRequest(http.MethodGet, "/samples/data/employees.json", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json prefix", ct)
	}
	if !strings.Contains(rec.Body.String(), `"email"`) {
		t.Errorf("expected employees.json to contain an email field, got: %s", rec.Body.String())
	}
}

func TestSamplesDataHandler_ServesOrdersAndProducts(t *testing.T) {
	router := newSamplesTestRouter()

	for _, tc := range []struct {
		file  string
		field string
	}{
		{"orders.json", `"product"`},
		{"products.json", `"category"`},
	} {
		req := httptest.NewRequest(http.MethodGet, "/samples/data/"+tc.file, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", tc.file, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), tc.field) {
			t.Errorf("%s: expected field %q, got: %s", tc.file, tc.field, rec.Body.String())
		}
	}
}

// TestSamplesDataHandler_ResponsesAreValidSourceAPIInput guards the exact
// bug this endpoint originally shipped with: source_api's fetcher
// (pkg/fetchers/rest_fetcher.go) only ever parses response bodies as JSON
// via common.ParseJSONData — there is no CSV-response support anywhere in
// that path. A CSV sample file 404s on the URL the templates ask for
// (they don't know to add a .csv extension expectation) and, even if it
// did resolve, would fail exactly this parse. Every embedded sample must
// round-trip through the same parser source_api actually uses.
func TestSamplesDataHandler_ResponsesAreValidSourceAPIInput(t *testing.T) {
	router := newSamplesTestRouter()

	for _, file := range []string{"employees.json", "orders.json", "products.json"} {
		req := httptest.NewRequest(http.MethodGet, "/samples/data/"+file, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		records, err := common.ParseJSONData(rec.Body.Bytes())
		if err != nil {
			t.Fatalf("%s: not parseable by source_api's ParseJSONData: %v", file, err)
		}
		if len(records) == 0 {
			t.Errorf("%s: parsed to zero records", file)
		}
	}
}

func TestSamplesDataHandler_UnknownFileReturns404(t *testing.T) {
	router := newSamplesTestRouter()
	req := httptest.NewRequest(http.MethodGet, "/samples/data/does-not-exist.json", nil)
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
