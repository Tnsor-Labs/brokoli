package fetchers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// endlessServer streams JSON-looking bytes until the client goes away, with
// no Content-Length, and counts requests.
func endlessServer(t *testing.T) (*httptest.Server, *int64) {
	t.Helper()
	var requests int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&requests, 1)
		w.Header().Set("Content-Type", "application/json")
		chunk := []byte(strings.Repeat(`{"id":1},`, 512))
		_, _ = w.Write([]byte("["))
		for r.Context().Err() == nil {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

// A server that never stops sending used to be read until the process ran
// out of memory. The body is now cut off at the cap.
func TestAnEndlessResponseIsCutOffAtTheCap(t *testing.T) {
	server, _ := endlessServer(t)
	_, err := (&RESTFetcher{}).Fetch(server.URL, map[string]interface{}{"max_response_bytes": 64 << 10})
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("err = %v, want ErrResponseTooLarge", err)
	}
	if !strings.Contains(err.Error(), "max_response_bytes") {
		t.Errorf("the error does not name the setting that raises the cap: %v", err)
	}
}

// A declared length over the cap is refused without reading the body; one
// exactly at the cap is accepted.
func TestTheCapAppliesToTheDeclaredLength(t *testing.T) {
	body := `[{"id":1},{"id":2}]`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	if _, err := (&RESTFetcher{}).Fetch(server.URL, map[string]interface{}{"max_response_bytes": len(body) - 1}); !errors.Is(err, ErrResponseTooLarge) {
		t.Errorf("one byte over: err = %v, want ErrResponseTooLarge", err)
	}
	ds, err := (&RESTFetcher{}).Fetch(server.URL, map[string]interface{}{"max_response_bytes": float64(len(body))})
	if err != nil {
		t.Fatalf("at the cap: %v", err)
	}
	if len(ds.Rows) != 2 {
		t.Errorf("at the cap: %d rows, want 2", len(ds.Rows))
	}
}

// An oversized page is oversized on every try, so it is not retried.
func TestAnOversizedPageIsNotRetried(t *testing.T) {
	server, requests := endlessServer(t)
	_, err := (&RESTFetcher{}).Fetch(server.URL, map[string]interface{}{
		"max_response_bytes": 64 << 10,
		"pagination":         map[string]interface{}{"strategy": "offset", "page_size": 2},
		"execution":          map[string]interface{}{"page_max_retries": 3},
	})
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("err = %v, want ErrResponseTooLarge", err)
	}
	if got := atomic.LoadInt64(requests); got != 1 {
		t.Errorf("%d requests, want 1", got)
	}
}

// A cap the author set but wrote wrongly is an error, not the default.
func TestAnInvalidCapIsRejected(t *testing.T) {
	server, requests := statusServer(t, http.StatusOK, nil)
	for _, v := range []interface{}{0, -1, 1.5, "1MB", true} {
		_, err := (&RESTFetcher{}).Fetch(server.URL, map[string]interface{}{"max_response_bytes": v})
		if err == nil || !strings.Contains(err.Error(), "max_response_bytes") {
			t.Errorf("%v: err = %v, want a max_response_bytes error", v, err)
		}
	}
	if got := atomic.LoadInt64(requests); got != 0 {
		t.Errorf("%d requests sent with an invalid cap, want 0", got)
	}
}
