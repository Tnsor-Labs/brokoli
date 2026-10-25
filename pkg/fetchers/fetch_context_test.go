package fetchers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Page retries waited with time.Sleep, so a fetch whose context had ended
// still sat through every backoff. The wait now ends with the context.
func TestFetchContextStopsRetryingWhenTheContextEnds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	// Exponential backoff waits 100ms, 200ms, 400ms, 800ms, 1.6s: a 1.6s
	// deadline falls inside the 1.6s wait, which ignoring the context
	// would sit out to about 3.1s.
	ctx, cancel := context.WithTimeout(context.Background(), 1600*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := (&RESTFetcher{}).FetchContext(ctx, srv.URL, map[string]interface{}{
		"records":    "data",
		"pagination": map[string]interface{}{"strategy": "cursor", "cursor_path": "next", "cursor_param": "cursor"},
		"execution":  map[string]interface{}{"page_max_retries": float64(20), "page_retry_backoff": "exponential"},
	})
	if err == nil {
		t.Fatal("the fetch succeeded against an endpoint that always fails")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the context's deadline", err)
	}
	if waited := time.Since(start); waited > 2500*time.Millisecond {
		t.Fatalf("the fetch returned after %s, want about 1.6s", waited)
	}
}
