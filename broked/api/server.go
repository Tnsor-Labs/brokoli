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
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/hc12r/broked/crypto"
	"github.com/hc12r/broked/engine"
	"github.com/hc12r/broked/extensions"
	"github.com/hc12r/broked/models"
	"github.com/hc12r/broked/store"
)

// Server is the main HTTP server for Broked.
type Server struct {
	router *chi.Mux
	port   int
	hub    *Hub
	ext    *extensions.Registry
}

// NewServer creates a fully configured HTTP server.
func NewServer(port int, s store.Store, e *engine.Engine, uiFS fs.FS, auth *AuthConfig, userStore *UserStore, sched *engine.Scheduler, ext *extensions.Registry, cryptoCfg ...*crypto.Config) *Server {
	r := chi.NewRouter()
	metrics := NewMetrics()
	r.Use(MetricsMiddleware(metrics))
	r.Use(Logger)
	r.Use(CORS)
	r.Use(RateLimiter(200)) // 200 req/s per IP
	r.Use(RequestTimeout(60)) // 60s default timeout for API requests
	r.Use(WorkspaceMiddleware)

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
	// Wire EventBus for distributed WebSocket broadcasting
	if ext != nil && ext.EventBus != nil {
		hub.SetEventBus(ext.EventBus)
		hub.StartDistributedBroadcasting(e.Events())
	} else {
		hub.StartBroadcasting(e.Events())
	}

	// Enterprise: SSO middleware
	if ext != nil && ext.Auth != nil && ext.Auth.Enabled() {
		r.Use(ext.Auth.Middleware())
		log.Printf("Enterprise: SSO (%s) enabled", ext.Auth.Name())
	}

	// Enterprise: Git sync webhook
	if ext != nil && ext.GitSync != nil && ext.GitSync.Enabled() {
		r.Post("/api/git/webhook", ext.GitSync.WebhookHandler())
		log.Println("Enterprise: Git sync enabled")
	}

	pc := NewPermissionChecker(s)
	RegisterRoutes(r, s, e, hub, sched, ext, userStore, cryptoCfg...)

	// Auth routes
	if userStore != nil {
		loginLimiter := RateLimiter(10) // 10 req/s for auth (stricter than global 200)
		r.With(loginLimiter).Post("/api/auth/login", withSessionCookie(LoginHandler(userStore), r))
		// Self-service signup is registered by enterprise platform provider
		r.Get("/api/auth/me", MeHandler())
		r.Get("/api/auth/me/permissions", func(w http.ResponseWriter, req *http.Request) {
			claims := req.Context().Value("claims")
			if claims == nil {
				// Open mode — return all permissions
				writeJSON(w, http.StatusOK, models.AllPermissions())
				return
			}
			mapClaims, ok := claims.(*jwt.MapClaims)
			if !ok || mapClaims == nil {
				writeJSON(w, http.StatusOK, []models.Permission{})
				return
			}
			role, _ := (*mapClaims)["role"].(string)
			wsID := GetWorkspaceID(req)
			perms := pc.GetUserPermissions(role, wsID)
			writeJSON(w, http.StatusOK, perms)
		})
		r.Get("/api/auth/users", ListUsersHandler(userStore))
		r.Post("/api/auth/users", CreateUserHandler(userStore))
		r.Get("/api/auth/setup", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"needs_setup": userStore.UserCount() == 0,
			})
		})
		r.Post("/api/auth/admin-reset-password", func(w http.ResponseWriter, r *http.Request) {
			claimsRaw := r.Context().Value("claims")
			claims, _ := claimsRaw.(*jwt.MapClaims)
			if claims == nil {
				writeError(w, http.StatusUnauthorized, "authentication required")
				return
			}
			role, _ := (*claims)["role"].(string)
			if role != "admin" && role != "superadmin" {
				writeError(w, http.StatusForbidden, "admin role required")
				return
			}
			var req struct {
				UserID      string `json:"user_id"`
				NewPassword string `json:"new_password"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeError(w, http.StatusBadRequest, "invalid JSON")
				return
			}
			if err := userStore.AdminResetPassword(req.UserID, req.NewPassword); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "password reset"})
		})
		r.Post("/api/auth/change-password", func(w http.ResponseWriter, r *http.Request) {
			claimsRaw := r.Context().Value("claims")
			claims, _ := claimsRaw.(*jwt.MapClaims)
			if claims == nil {
				writeError(w, http.StatusUnauthorized, "authentication required")
				return
			}
			var req struct {
				CurrentPassword string `json:"current_password"`
				NewPassword     string `json:"new_password"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeError(w, http.StatusBadRequest, "invalid JSON")
				return
			}
			userID, _ := (*claims)["sub"].(string)
			if err := userStore.ChangePassword(userID, req.CurrentPassword, req.NewPassword); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "password changed"})
		})
	}

	// Serve uploaded files (ticket attachments)
	r.Get("/uploads/{filename}", func(w http.ResponseWriter, r *http.Request) {
		filename := chi.URLParam(r, "filename")
		filePath := filepath.Join("./uploads", filepath.Base(filename))
		if _, err := os.Stat(filePath); os.IsNotExist(err) {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, filePath)
	})

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
				// Cache static assets (JS/CSS have content hashes in filenames)
				if strings.HasPrefix(path, "/assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
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

	return &Server{router: r, port: port, hub: hub, ext: ext}
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

// withSessionCookie wraps a login handler to set an httpOnly session cookie on success.
func withSessionCookie(handler http.Handler, router *chi.Mux) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Wrap response writer to intercept the token from the JSON response
		rec := &cookieResponseWriter{ResponseWriter: w, statusCode: 200}
		handler.ServeHTTP(rec, r)

		// If login succeeded (200), try to set cookie from the response body
		if rec.statusCode == 200 && len(rec.body) > 0 {
			var resp map[string]interface{}
			if json.Unmarshal(rec.body, &resp) == nil {
				if token, ok := resp["token"].(string); ok && token != "" {
					secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
					http.SetCookie(w, &http.Cookie{
						Name:     "brokoli_session",
						Value:    token,
						Path:     "/",
						HttpOnly: true,
						Secure:   secure,
						SameSite: http.SameSiteLaxMode,
						MaxAge:   86400,
					})
				}
			}
		}
		// Always write the response back
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rec.statusCode)
		w.Write(rec.body)
	}
}

type cookieResponseWriter struct {
	http.ResponseWriter
	statusCode int
	body       []byte
}

func (w *cookieResponseWriter) WriteHeader(code int) {
	w.statusCode = code
}

func (w *cookieResponseWriter) Write(b []byte) (int, error) {
	w.body = append(w.body, b...)
	return len(b), nil
}
