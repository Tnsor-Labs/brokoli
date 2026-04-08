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
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, wrapped.status, time.Since(start))
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
func RateLimiter(requestsPerSecond int) func(http.Handler) http.Handler {
	type visitor struct {
		tokens    float64
		lastCheck time.Time
	}
	var mu sync.Mutex
	visitors := make(map[string]*visitor)
	rate := float64(requestsPerSecond)

	// Cleanup old visitors every minute
	go func() {
		for range time.Tick(time.Minute) {
			mu.Lock()
			for ip, v := range visitors {
				if time.Since(v.lastCheck) > 2*time.Minute {
					delete(visitors, ip)
				}
			}
			mu.Unlock()
		}
	}()

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip rate limiting for static assets and WebSocket
			if r.URL.Path == "/health" || r.URL.Path == "/" || strings.ToLower(r.Header.Get("Upgrade")) == "websocket" {
				next.ServeHTTP(w, r)
				return
			}

			ip := r.RemoteAddr
			mu.Lock()
			v, exists := visitors[ip]
			now := time.Now()
			if !exists {
				v = &visitor{tokens: rate, lastCheck: now}
				visitors[ip] = v
			}
			// Refill tokens
			elapsed := now.Sub(v.lastCheck).Seconds()
			v.tokens += elapsed * rate
			if v.tokens > rate*2 { // burst cap = 2x rate
				v.tokens = rate * 2
			}
			v.lastCheck = now

			if v.tokens < 1 {
				mu.Unlock()
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			v.tokens--
			mu.Unlock()

			next.ServeHTTP(w, r)
		})
	}
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
