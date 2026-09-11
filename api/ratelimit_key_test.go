package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// RateLimiter keyed on r.RemoteAddr, which carries the client's
// ephemeral source port. Every new TCP connection therefore got its own
// token bucket, so a caller that did not reuse a connection was never
// limited at all (#539).
//
// The test that catches it has to vary the port while holding the
// address fixed. One that reuses a single RemoteAddr passes against the
// broken version.
func TestRateLimiterBucketsByClientNotByConnection(t *testing.T) {
	// 2 req/s, and the burst cap is 2x rate, so the first 4 pass and the
	// rest must not.
	send := newRateLimitedProbe(t, 2)

	rejected := 0
	for i := 0; i < 12; i++ {
		if send(fmt.Sprintf("203.0.113.9:%d", 41000+i)) == http.StatusTooManyRequests {
			rejected++
		}
	}

	if rejected == 0 {
		t.Fatalf("12 requests from one address across 12 source ports were all allowed; "+
			"the limiter is bucketing per connection, not per client (rejected=%d)", rejected)
	}
}

// The other half of the contract. Collapsing the port must not collapse
// genuinely different clients into one bucket, or one busy caller would
// throttle everyone else.
func TestRateLimiterKeepsDistinctClientsIndependent(t *testing.T) {
	send := newRateLimitedProbe(t, 2)

	// Exhaust one client.
	for i := 0; i < 12; i++ {
		send("203.0.113.9:40000")
	}

	// A different address must still be served.
	if code := send("198.51.100.4:40000"); code != http.StatusOK {
		t.Fatalf("a second client got %d after the first exhausted its bucket; "+
			"distinct clients must not share one", code)
	}
}

func TestRateLimitKeyStripsThePort(t *testing.T) {
	cases := map[string]string{
		"203.0.113.9:40000": "203.0.113.9",
		"[2001:db8::1]:443": "2001:db8::1",
		"203.0.113.9":       "203.0.113.9", // no port: used as given
		"":                  "",            // no address at all: used as given
		"@":                 "@",           // abstract unix socket: used as given
	}
	for remote, want := range cases {
		t.Run(fmt.Sprintf("%q", remote), func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/pipelines", nil)
			r.RemoteAddr = remote
			if got := rateLimitKey(r); got != want {
				t.Errorf("rateLimitKey(%q) = %q, want %q", remote, got, want)
			}
		})
	}
}

// newRateLimitedProbe returns a send function that pushes one request
// through a fresh RateLimiter at the given rate and reports the status.
func newRateLimitedProbe(t *testing.T, perSecond int) func(remoteAddr string) int {
	t.Helper()
	h := RateLimiter(perSecond)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	return func(remoteAddr string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/pipelines", nil)
		req.RemoteAddr = remoteAddr
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
}
