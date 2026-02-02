package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/hc12r/broked/crypto"
	"github.com/hc12r/broked/engine"
	"github.com/hc12r/broked/store"
)

// Server is the main HTTP server for Broked.
type Server struct {
	router *chi.Mux
	port   int
	hub    *Hub
}

// NewServer creates a fully configured HTTP server.
func NewServer(port int, s store.Store, e *engine.Engine, uiFS fs.FS, auth *AuthConfig, userStore *UserStore, sched *engine.Scheduler, cryptoCfg ...*crypto.Config) *Server {
	r := chi.NewRouter()
	metrics := NewMetrics()
	r.Use(MetricsMiddleware(metrics))
	r.Use(Logger)
	r.Use(CORS)
	r.Use(RateLimiter(100)) // 100 req/s per IP

	// Auth layers
	if auth != nil {
		r.Use(APIKeyAuth(auth))
	}
	if userStore != nil {
		InitJWTSecret()
		r.Use(JWTAuth(userStore))
	}

	// Observability endpoints (outside /api, no auth required)
	r.Get("/health", HealthHandler(s))
	r.Get("/metrics", PrometheusHandler(metrics, s, e))

	hub := NewHub()
	hub.StartBroadcasting(e.Events())

	RegisterRoutes(r, s, e, hub, sched, cryptoCfg...)

	// Auth routes
	if userStore != nil {
		r.Post("/api/auth/login", LoginHandler(userStore))
		r.Get("/api/auth/me", MeHandler())
		r.Get("/api/auth/users", ListUsersHandler(userStore))
		r.Post("/api/auth/users", CreateUserHandler(userStore))
		r.Get("/api/auth/setup", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"needs_setup": userStore.UserCount() == 0,
			})
		})
	}

	// Serve embedded UI (or fallback)
	if uiFS != nil {
		r.Get("/*", func(w http.ResponseWriter, req *http.Request) {
			// Try serving the exact file path
			path := req.URL.Path
			if path == "/" {
				path = "/index.html"
			}

			// Check if file exists in embedded FS
			f, err := uiFS.Open(path[1:]) // strip leading /
			if err == nil {
				f.Close()
				http.FileServerFS(uiFS).ServeHTTP(w, req)
				return
			}

			// SPA fallback: serve index.html for any unknown path
			indexData, err := fs.ReadFile(uiFS, "index.html")
			if err != nil {
				http.NotFound(w, req)
				return
			}
			w.Header().Set("Content-Type", "text/html")
			w.Write(indexData)
		})
	} else {
		r.Get("/*", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, placeholderHTML)
		})
	}

	return &Server{router: r, port: port, hub: hub}
}

// Start begins listening for HTTP requests with graceful shutdown.
func (s *Server) Start() error {
	addr := fmt.Sprintf(":%d", s.port)
	srv := &http.Server{Addr: addr, Handler: s.router}

	// Graceful shutdown on SIGINT/SIGTERM
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

	errCh := make(chan error, 1)
	go func() {
		log.Printf("Broked server listening on http://localhost%s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case sig := <-quit:
		log.Printf("Received %v, shutting down gracefully (30s timeout)...", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("Server shutdown error: %v", err)
			return err
		}
		log.Println("Server stopped cleanly")
		return nil
	}
}

const placeholderHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Broked</title>
<style>
  * { margin: 0; padding: 0; box-sizing: border-box; }
  body {
    font-family: "Inter", system-ui, sans-serif;
    background: #09090b;
    color: #fafafa;
    display: flex;
    align-items: center;
    justify-content: center;
    min-height: 100vh;
  }
  .container {
    text-align: center;
    max-width: 480px;
  }
  h1 {
    font-size: 2.5rem;
    font-weight: 700;
    letter-spacing: -0.02em;
    margin-bottom: 0.5rem;
  }
  .accent { color: #6366f1; }
  p {
    color: #a1a1aa;
    font-size: 1rem;
    line-height: 1.6;
    margin-bottom: 1.5rem;
  }
  .status {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    background: #18181b;
    border: 1px solid #3f3f46;
    border-radius: 6px;
    padding: 0.5rem 1rem;
    font-family: "JetBrains Mono", "Fira Code", monospace;
    font-size: 0.875rem;
    color: #22c55e;
  }
  .dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: #22c55e;
    animation: pulse 2s ease-in-out infinite;
  }
  @keyframes pulse {
    0%, 100% { opacity: 1; }
    50% { opacity: 0.4; }
  }
</style>
</head>
<body>
  <div class="container">
    <h1><span class="accent">broked</span></h1>
    <p>Data Orchestration Platform</p>
    <div class="status">
      <span class="dot"></span>
      API running — UI coming soon
    </div>
  </div>
</body>
</html>`

// writeJSON writes a JSON response.
func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// writeError writes a JSON error response.
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
