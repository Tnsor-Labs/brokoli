package api

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Logger logs each request with method, path, status, and duration.
func Logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		wrapped := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(wrapped, r)
		path := strings.ReplaceAll(strings.ReplaceAll(r.URL.Path, "\n", ""), "\r", "")
		log.Printf("%s %s %d %s", r.Method, path, wrapped.status, time.Since(start))
	})
}

// SecurityHeaders adds standard security headers to every response.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		// HSTS: enforce HTTPS for 1 year, including subdomains
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

// CORS adds CORS headers. Set BROKOLI_CORS_ORIGINS for production (comma-separated).
// When not set, allows same-origin only (no Access-Control-Allow-Origin header).
func CORS(next http.Handler) http.Handler {
	allowedOrigins := os.Getenv("BROKOLI_CORS_ORIGINS")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		if origin != "" && allowedOrigins != "" {
			for _, allowed := range strings.Split(allowedOrigins, ",") {
				if strings.TrimSpace(allowed) == origin {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Set("Vary", "Origin")
					w.Header().Set("Access-Control-Allow-Credentials", "true")
					break
				}
			}
		}
		// If BROKOLI_CORS_ORIGINS is not set, no CORS header is sent —
		// browsers will only allow same-origin requests (secure default).

		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Workspace-ID, X-Webhook-Token")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RateLimiter is a simple token-bucket rate limiter middleware.
// rateLimitKey is the bucket key for RateLimiter: the caller's address
// with its ephemeral source port removed.
//
// r.RemoteAddr is "IP:port", and the port is different on every TCP
// connection. Keying on it whole gave each connection its own bucket,
// which is not a rate limit: a caller that opened a new connection per
// request was never slowed down, and the visitors map grew per
// connection rather than per client.
//
// An address with no port is used as given rather than refused. An
// address shape we do not recognise is a formatting surprise, not an
// attack, and failing closed here would take the whole API down over
// one.
func rateLimitKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func RateLimiter(requestsPerSecond int) func(http.Handler) http.Handler {
	return newRateLimiter(requestsPerSecond, time.Now).middleware
}

// rateLimiter is a token bucket per client address.
//
// Visitors idle for over two minutes are forgotten, checked at most once a
// minute while a request is handled. A goroutine on time.Tick used to do
// that, one per limiter and never stopped: NewServer builds four limiters,
// so every server, and every test that built one, leaked four.
type rateLimiter struct {
	mu        sync.Mutex
	visitors  map[string]*rateVisitor
	rate      float64
	now       func() time.Time
	lastSweep time.Time
}

type rateVisitor struct {
	tokens    float64
	lastCheck time.Time
}

func newRateLimiter(requestsPerSecond int, now func() time.Time) *rateLimiter {
	return &rateLimiter{
		visitors:  make(map[string]*rateVisitor),
		rate:      float64(requestsPerSecond),
		now:       now,
		lastSweep: now(),
	}
}

func (rl *rateLimiter) visitorCount() int {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	return len(rl.visitors)
}

func (rl *rateLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip rate limiting for static assets and WebSocket
		if r.URL.Path == "/health" || r.URL.Path == "/" || strings.ToLower(r.Header.Get("Upgrade")) == "websocket" {
			next.ServeHTTP(w, r)
			return
		}

		ip := rateLimitKey(r)
		rl.mu.Lock()
		now := rl.now()
		if now.Sub(rl.lastSweep) >= time.Minute {
			for addr, v := range rl.visitors {
				if now.Sub(v.lastCheck) > 2*time.Minute {
					delete(rl.visitors, addr)
				}
			}
			rl.lastSweep = now
		}
		v, exists := rl.visitors[ip]
		if !exists {
			v = &rateVisitor{tokens: rl.rate, lastCheck: now}
			rl.visitors[ip] = v
		}
		// Refill tokens
		elapsed := now.Sub(v.lastCheck).Seconds()
		v.tokens += elapsed * rl.rate
		if v.tokens > rl.rate*2 { // burst cap = 2x rate
			v.tokens = rl.rate * 2
		}
		v.lastCheck = now

		if v.tokens < 1 {
			rl.mu.Unlock()
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		v.tokens--
		rl.mu.Unlock()

		next.ServeHTTP(w, r)
	})
}

// RequestTimeout adds a deadline to non-WebSocket, non-streaming requests.
func RequestTimeout(seconds int) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip WebSocket and streaming endpoints
			if strings.ToLower(r.Header.Get("Upgrade")) == "websocket" {
				next.ServeHTTP(w, r)
				return
			}
			// Skip long-running endpoints (dry-run, backfill, run trigger)
			if strings.Contains(r.URL.Path, "/dry-run") ||
				strings.Contains(r.URL.Path, "/backfill") ||
				// Testing a change request is two previews, bounded by
				// engine.PreviewTimeout.
				(strings.Contains(r.URL.Path, "/changes/") && strings.HasSuffix(r.URL.Path, "/test") && r.Method == "POST") ||
				(strings.Contains(r.URL.Path, "/run") && r.Method == "POST") {
				next.ServeHTTP(w, r)
				return
			}

			ctx, cancel := context.WithTimeout(r.Context(), time.Duration(seconds)*time.Second)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Hijack implements http.Hijacker for WebSocket support.
func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := w.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, fmt.Errorf("underlying ResponseWriter does not support hijacking")
}
