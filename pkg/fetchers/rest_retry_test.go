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
	if got, backoff := pageRetryWait("exponential", 3, short), pageRetryDelay("exponential", 3); got < backoff/2 || got > backoff {
		t.Errorf("wait = %v, want the configured backoff %v (jittered) when it is longer", got, backoff)
	}
	if msg := (&statusError{code: 404}).Error(); msg != "HTTP request failed: status code "+strconv.Itoa(404) {
		t.Errorf("message changed: %q", msg)
	}
}

func TestPageRetryDelayFollowsTheConfiguredBackoff(t *testing.T) {
	for _, tc := range []struct {
		backoff string
		attempt int
		want    time.Duration
	}{
		{"exponential", 0, 100 * time.Millisecond},
		{"exponential", 1, 200 * time.Millisecond},
		{"exponential", 3, 800 * time.Millisecond},
		{"", 2, 400 * time.Millisecond},
		{"linear", 0, 100 * time.Millisecond},
		{"linear", 4, 500 * time.Millisecond},
		// The UI offers fixed; page retries used to treat it as exponential.
		{"fixed", 0, 100 * time.Millisecond},
		{"fixed", 9, 100 * time.Millisecond},
	} {
		if got := pageRetryDelay(tc.backoff, tc.attempt); got != tc.want {
			t.Errorf("pageRetryDelay(%q, %d) = %v, want %v", tc.backoff, tc.attempt, got, tc.want)
		}
	}
}

// A long retry budget must not wait for ever, nor overflow into a
// negative wait (no wait at all), which doubling from 100ms did at the
// 37th retry.
func TestPageRetryDelayIsCapped(t *testing.T) {
	for _, backoff := range []string{"exponential", "linear"} {
		prev := time.Duration(0)
		for attempt := 0; attempt < 5000; attempt++ {
			got := pageRetryDelay(backoff, attempt)
			if got < prev || got > maxPageRetryDelay {
				t.Fatalf("pageRetryDelay(%q, %d) = %v after %v; want non-decreasing and at most %v", backoff, attempt, got, prev, maxPageRetryDelay)
			}
			prev = got
		}
		if prev != maxPageRetryDelay {
			t.Errorf("%s backoff settles at %v, want the %v cap", backoff, prev, maxPageRetryDelay)
		}
	}
}

// Pages that fail together must not retry together: every wait falls in
// the upper half of the backoff, and the waits are spread across it.
func TestPageRetryWaitIsJittered(t *testing.T) {
	limited := &statusError{code: http.StatusTooManyRequests}
	for _, attempt := range []int{0, 3, 40} {
		delay := pageRetryDelay("exponential", attempt)
		seen := map[time.Duration]bool{}
		lowest, highest := delay, time.Duration(0)
		for range 500 {
			got := pageRetryWait("exponential", attempt, limited)
			if got < delay/2 || got > delay {
				t.Fatalf("attempt %d: wait %v outside [%v, %v]", attempt, got, delay/2, delay)
			}
			seen[got] = true
			lowest, highest = min(lowest, got), max(highest, got)
		}
		// 500 uniform draws leave less than a 1e-50 chance of any of these.
		if len(seen) < 400 || lowest > delay/2+delay/8 || highest < delay-delay/8 {
			t.Errorf("attempt %d: %d distinct waits in [%v, %v] of [%v, %v]; want them spread across it", attempt, len(seen), lowest, highest, delay/2, delay)
		}
	}
}

// A Retry-After is the server's schedule: kept exactly, not jittered.
func TestPageRetryWaitKeepsRetryAfterExactly(t *testing.T) {
	asked := &statusError{code: http.StatusTooManyRequests, retryAfter: 7 * time.Second}
	for range 50 {
		if got := pageRetryWait("exponential", 0, asked); got != 7*time.Second {
			t.Fatalf("wait = %v, want the server's 7s", got)
		}
	}
}
