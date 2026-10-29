package fetchers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// statusServer answers every request with status and counts them.
func statusServer(t *testing.T, status int, header http.Header) (*httptest.Server, *int64) {
	t.Helper()
	var requests int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&requests, 1)
		for k, v := range header {
			w.Header()[k] = v
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

func fetchPaged(url string, retries int) error {
	_, err := (&RESTFetcher{}).Fetch(url, map[string]interface{}{
		"pagination": map[string]interface{}{"strategy": "offset", "page_size": 2},
		"execution":  map[string]interface{}{"page_max_retries": retries},
	})
	return err
}

// A page that answers 401, 403, 404 or 400 answers the same on every try.
// Retrying only delayed the failure and sent the server more requests.
func TestAPageThatCannotSucceedIsNotRetried(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound} {
		server, requests := statusServer(t, status, nil)
		err := fetchPaged(server.URL, 3)
		if !errors.Is(err, ErrHTTPRequestFailed) {
			t.Errorf("%d: err = %v, want ErrHTTPRequestFailed", status, err)
		}
		if got := atomic.LoadInt64(requests); got != 1 {
			t.Errorf("%d: %d requests, want 1", status, got)
		}
	}
}

// Rate limits, timeouts and server errors can pass, so they are retried.
func TestATransientPageFailureIsRetried(t *testing.T) {
	for _, status := range []int{http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable} {
		server, requests := statusServer(t, status, nil)
		if err := fetchPaged(server.URL, 2); err == nil {
			t.Fatalf("%d: expected an error once retries ran out", status)
		}
		if got := atomic.LoadInt64(requests); got != 3 {
			t.Errorf("%d: %d requests, want 3 (1 + 2 retries)", status, got)
		}
	}
}

// A server that says when to come back is waited for, not hammered on the
// default 100 ms backoff.
func TestRetryAfterIsHonoured(t *testing.T) {
	server, requests := statusServer(t, http.StatusTooManyRequests, http.Header{"Retry-After": {"1"}})
	start := time.Now()
	if err := fetchPaged(server.URL, 1); err == nil {
		t.Fatal("expected an error once retries ran out")
	}
	if got := atomic.LoadInt64(requests); got != 2 {
		t.Fatalf("%d requests, want 2", got)
	}
	if waited := time.Since(start); waited < time.Second {
		t.Errorf("retried after %v, before the server's Retry-After of 1s", waited)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 29, 4, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		in   string
		want time.Duration
	}{
		{"", 0},
		{"120", 2 * time.Minute},
		{"-5", 0},
		{now.Add(30 * time.Second).Format(http.TimeFormat), 30 * time.Second},
		{now.Add(-time.Minute).Format(http.TimeFormat), 0},
		{"soon", 0},
	} {
		if got := parseRetryAfter(tc.in, now); got != tc.want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestPageRetryWaitCapsTheServersAsk(t *testing.T) {
	hours := &statusError{code: http.StatusServiceUnavailable, retryAfter: 3 * time.Hour}
	if got := pageRetryWait("exponential", 0, hours); got != maxRetryAfter {
		t.Errorf("wait = %v, want the %v cap", got, maxRetryAfter)
	}
	short := &statusError{code: http.StatusServiceUnavailable, retryAfter: time.Millisecond}
	if got, backoff := pageRetryWait("exponential", 3, short), pageRetryDelay("exponential", 3); got != backoff {
		t.Errorf("wait = %v, want the configured backoff %v when it is longer", got, backoff)
	}
	if msg := (&statusError{code: 404}).Error(); msg != "HTTP request failed: status code "+strconv.Itoa(404) {
		t.Errorf("message changed: %q", msg)
	}
}
