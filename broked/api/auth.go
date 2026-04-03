package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
)

// AuthConfig holds the authentication configuration.
type AuthConfig struct {
	Enabled bool
	Keys    map[string]string // key -> description
	mu      sync.RWMutex
}

// NewAuthConfig creates an auth config. If no keys provided, auth is disabled.
func NewAuthConfig() *AuthConfig {
	return &AuthConfig{
		Keys: make(map[string]string),
	}
}

// AddKey registers an API key.
func (a *AuthConfig) AddKey(key, description string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Keys[key] = description
	a.Enabled = true
}

// RemoveKey removes an API key.
func (a *AuthConfig) RemoveKey(key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.Keys, key)
	if len(a.Keys) == 0 {
		a.Enabled = false
	}
}

// ValidateKey checks if a key is valid using constant-time comparison.
func (a *AuthConfig) ValidateKey(key string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	for k := range a.Keys {
		if subtle.ConstantTimeCompare([]byte(k), []byte(key)) == 1 {
			return true
		}
	}
	return false
}

// GenerateKey creates a cryptographically secure API key.
func GenerateKey() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "brk_" + hex.EncodeToString(b), nil
}

// APIKeyAuth is middleware that enforces API key authentication.
// Skips auth for UI routes (non-/api paths) and WebSocket upgrades.
func APIKeyAuth(auth *AuthConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip if auth is disabled
			if !auth.Enabled {
				next.ServeHTTP(w, r)
				return
			}

			// Skip non-API routes (UI serving)
			if !strings.HasPrefix(r.URL.Path, "/api/") {
				next.ServeHTTP(w, r)
				return
			}

			// WebSocket — let JWT middleware handle auth
			if strings.ToLower(r.Header.Get("Upgrade")) == "websocket" {
				next.ServeHTTP(w, r)
				return
			}

			// Skip webhook triggers (own token auth)
			if strings.Contains(r.URL.Path, "/webhook") && r.Method == "POST" {
				next.ServeHTTP(w, r)
				return
			}

			// Check Authorization header: "Bearer brk_..."
			authHeader := r.Header.Get("Authorization")
			key := ""
			if strings.HasPrefix(authHeader, "Bearer ") {
				key = strings.TrimPrefix(authHeader, "Bearer ")
			}

			// Also accept X-API-Key header
			if key == "" {
				key = r.Header.Get("X-API-Key")
			}

			if key == "" {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "API key required"})
				return
			}

			if !auth.ValidateKey(key) {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "invalid API key"})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
