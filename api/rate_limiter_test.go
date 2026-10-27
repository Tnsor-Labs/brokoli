package api

import (
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"
)

// Each RateLimiter started a cleanup goroutine on time.Tick that never
// stopped: NewServer builds four limiters, and every test server built
// four more. Building limiters now starts no goroutines.
func TestRateLimiterStartsNoGoroutine(t *testing.T) {
	before := runtime.NumGoroutine()
	for i := 0; i < 50; i++ {
		_ = RateLimiter(10)
	}
	time.Sleep(50 * time.Millisecond) // let any started goroutine get scheduled
	if grew := runtime.NumGoroutine() - before; grew > 5 {
		t.Fatalf("building 50 rate limiters left %d more goroutines running", grew)
	}
}

// What the goroutine did is kept: a visitor idle for over two minutes is
// forgotten, checked at most once a minute, now while handling a request.
func TestRateLimiterForgetsIdleVisitors(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	rl := newRateLimiter(10, func() time.Time { return now })
	h := rl.middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	request := func(addr string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/pipelines", nil)
		req.RemoteAddr = addr
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := request("10.0.0.1:1"); code != http.StatusOK {
		t.Fatalf("first request = %d", code)
	}
	now = now.Add(90 * time.Second)
	request("10.0.0.2:1")
	if n := rl.visitorCount(); n != 2 {
		t.Fatalf("after 90s: %d visitors, want 2 (nobody idle for 2 minutes yet)", n)
	}
	now = now.Add(60 * time.Second) // 10.0.0.1 idle for 150s
	request("10.0.0.2:1")
	if n := rl.visitorCount(); n != 1 {
		t.Fatalf("after 150s: %d visitors, want 1 (10.0.0.1 forgotten)", n)
	}
}

// The limit itself is unchanged: a new client has rate tokens, then 429.
func TestRateLimiterStillLimits(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	h := newRateLimiter(2, func() time.Time { return now }).middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	codes := []int{}
	for i := 0; i < 4; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
		req.RemoteAddr = "10.0.0.9:1"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		codes = append(codes, rec.Code)
	}
	if codes[0] != http.StatusOK || codes[1] != http.StatusOK || codes[2] != http.StatusTooManyRequests || codes[3] != http.StatusTooManyRequests {
		t.Fatalf("codes = %v, want 200 200 429 429 (2 tokens, no time to refill)", codes)
	}
}
