package fetchers

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// alwaysFailServer counts requests and always returns 500 — used to prove
// exactly how many page-retry attempts fetchPaginated makes before giving
// up, without needing a page to ever actually succeed.
func alwaysFailServer(t *testing.T) (*httptest.Server, *int64) {
	t.Helper()
	var requests int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&requests, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	return server, &requests
}

func TestFetchPaginated_PageMaxRetries_OverridesTopLevelMaxRetries(t *testing.T) {
	server, requests := alwaysFailServer(t)
	defer server.Close()

	f := &RESTFetcher{}
	_, err := f.Fetch(server.URL, map[string]interface{}{
		"pagination": map[string]interface{}{
			"strategy":  "offset",
			"page_size": 2,
		},
		// A node-level retry count that would also govern page retry under
		// the old shared-key behavior — execution.page_max_retries must
		// win instead, proving the two are now independent (issue #47).
		"max_retries": 10,
		"execution": map[string]interface{}{
			"page_max_retries": 1,
		},
	})
	if err == nil {
		t.Fatal("expected an error — the server always fails")
	}

	// page_max_retries=1 means 1 retry after the first attempt: 2 total
	// requests for that one page.
	if got := atomic.LoadInt64(requests); got != 2 {
		t.Errorf("got %d requests, want exactly 2 (execution.page_max_retries=1 should override max_retries=10)", got)
	}
}

func TestFetchPaginated_PageMaxRetries_FallsBackToTopLevelMaxRetries(t *testing.T) {
	server, requests := alwaysFailServer(t)
	defer server.Close()

	f := &RESTFetcher{}
	_, err := f.Fetch(server.URL, map[string]interface{}{
		"pagination": map[string]interface{}{
			"strategy":  "offset",
			"page_size": 2,
		},
		"max_retries": 3,
	})
	if err == nil {
		t.Fatal("expected an error — the server always fails")
	}

	// No execution.page_max_retries set — must fall back to the existing
	// top-level max_retries=3 (1 initial attempt + 3 retries = 4 requests),
	// exactly like before issue #47, so no pipeline's behavior changes
	// unless it opts into the new key.
	if got := atomic.LoadInt64(requests); got != 4 {
		t.Errorf("got %d requests, want exactly 4 (fallback to top-level max_retries=3)", got)
	}
}

func TestFetchPaginated_PageMaxRetries_DefaultUnchanged(t *testing.T) {
	server, requests := alwaysFailServer(t)
	defer server.Close()

	f := &RESTFetcher{}
	_, err := f.Fetch(server.URL, map[string]interface{}{
		"pagination": map[string]interface{}{
			"strategy":  "offset",
			"page_size": 2,
		},
	})
	if err == nil {
		t.Fatal("expected an error — the server always fails")
	}

	// Neither max_retries nor execution.page_max_retries set — must use
	// defaultPageRetries (2), exactly as before issue #47: 1 initial
	// attempt + 2 retries = 3 requests.
	if got := atomic.LoadInt64(requests); got != int64(defaultPageRetries)+1 {
		t.Errorf("got %d requests, want %d (defaultPageRetries+1, unchanged)", got, defaultPageRetries+1)
	}
}
