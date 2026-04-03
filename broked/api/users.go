package api

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// Role defines user permission levels.
type Role string

const (
	RoleSuperAdmin Role = "superadmin"
	RoleAdmin      Role = "admin"
	RoleEditor     Role = "editor"
	RoleViewer     Role = "viewer"
)

// User represents a user account.
type User struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	Role      Role      `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

// UserStore handles user persistence. Uses the same DB connection.
type UserStore struct {
	db *sql.DB
}

// NewUserStore creates a user store and ensures the table exists.
func NewUserStore(db *sql.DB) (*UserStore, error) {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY,
			username TEXT UNIQUE NOT NULL,
			password_hash TEXT NOT NULL,
			role TEXT NOT NULL DEFAULT 'viewer',
			created_at TEXT NOT NULL
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("create users table: %w", err)
	}

	// Login attempts table for account lockout
	db.Exec(`CREATE TABLE IF NOT EXISTS login_attempts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		username TEXT NOT NULL, ip TEXT NOT NULL DEFAULT '',
		success INTEGER NOT NULL DEFAULT 0, attempted_at TEXT NOT NULL)`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_login_attempts ON login_attempts(username, attempted_at DESC)`)

	return &UserStore{db: db}, nil
}

// RecordLoginAttempt records a login attempt for lockout tracking.
func (us *UserStore) RecordLoginAttempt(username, ip string, success bool) {
	successInt := 0
	if success {
		successInt = 1
	}
	us.db.Exec(`INSERT INTO login_attempts (username, ip, success, attempted_at) VALUES (?, ?, ?, ?)`,
		username, ip, successInt, time.Now().Format(time.RFC3339))
}

// IsLocked returns true if the account has 5+ failed attempts in the last 15 minutes.
func (us *UserStore) IsLocked(username string) bool {
	var count int
	since := time.Now().Add(-15 * time.Minute).Format(time.RFC3339)
	us.db.QueryRow(`SELECT COUNT(*) FROM login_attempts WHERE username = ? AND success = 0 AND attempted_at > ?`,
		username, since).Scan(&count)
	return count >= 5
}

// ClearAttempts removes all login attempts for a user (called on successful login).
func (us *UserStore) ClearAttempts(username string) {
	us.db.Exec(`DELETE FROM login_attempts WHERE username = ?`, username)
}

// IsSuperAdmin checks whether the given user has the superadmin role.
func (us *UserStore) IsSuperAdmin(userID string) bool {
	var role string
	us.db.QueryRow(`SELECT role FROM users WHERE id = ?`, userID).Scan(&role)
	if role == "" {
		us.db.QueryRow(`SELECT role FROM users WHERE id = $1`, userID).Scan(&role)
	}
	return role == "superadmin"
}

// GetUserByID returns a user by ID.
func (us *UserStore) GetUserByID(id string) (*User, error) {
	var u User
	var createdAt string
	err := us.db.QueryRow(`SELECT id, username, role, created_at FROM users WHERE id = ?`, id).
		Scan(&u.ID, &u.Username, &u.Role, &createdAt)
	if err != nil {
		err = us.db.QueryRow(`SELECT id, username, role, created_at FROM users WHERE id = $1`, id).
			Scan(&u.ID, &u.Username, &u.Role, &createdAt)
	}
	if err != nil {
		return nil, fmt.Errorf("user not found")
	}
	u.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	return &u, nil
}

func (us *UserStore) CreateUser(username, password string, role Role) (*User, error) {
	if err := validatePassword(password); err != nil {
		return nil, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	id := generateID()
	now := time.Now()
	_, err = us.db.Exec(
		`INSERT INTO users (id, username, password_hash, role, created_at) VALUES (?, ?, ?, ?, ?)`,
		id, username, string(hash), string(role), now.Format(time.RFC3339),
	)
	if err != nil {
		// Try Postgres syntax
		_, err = us.db.Exec(
			`INSERT INTO users (id, username, password_hash, role, created_at) VALUES ($1, $2, $3, $4, $5)`,
			id, username, string(hash), string(role), now.Format(time.RFC3339),
		)
	}
	if err != nil {
		return nil, err
	}
	return &User{ID: id, Username: username, Role: role, CreatedAt: now}, nil
}

func (us *UserStore) Authenticate(username, password string) (*User, error) {
	var u User
	var hash, createdAt string

	err := us.db.QueryRow(
		`SELECT id, username, password_hash, role, created_at FROM users WHERE username = ?`, username,
	).Scan(&u.ID, &u.Username, &hash, &u.Role, &createdAt)
	if err != nil {
		// Try Postgres
		err = us.db.QueryRow(
			`SELECT id, username, password_hash, role, created_at FROM users WHERE username = $1`, username,
		).Scan(&u.ID, &u.Username, &hash, &u.Role, &createdAt)
	}
	if err != nil {
		return nil, fmt.Errorf("invalid credentials")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return nil, fmt.Errorf("invalid credentials")
	}

	u.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	return &u, nil
}

func validatePassword(password string) error {
	if len(password) < 10 {
		return fmt.Errorf("password must be at least 10 characters")
	}
	hasUpper := false
	hasLower := false
	hasDigit := false
	for _, c := range password {
		if c >= 'A' && c <= 'Z' {
			hasUpper = true
		}
		if c >= 'a' && c <= 'z' {
			hasLower = true
		}
		if c >= '0' && c <= '9' {
			hasDigit = true
		}
	}
	if !hasUpper || !hasLower || !hasDigit {
		return fmt.Errorf("password must contain uppercase, lowercase, and digit")
	}
	return nil
}

func (us *UserStore) ChangePassword(userID, currentPassword, newPassword string) error {
	var hash string
	err := us.db.QueryRow(`SELECT password_hash FROM users WHERE id = ?`, userID).Scan(&hash)
	if err != nil {
		err = us.db.QueryRow(`SELECT password_hash FROM users WHERE id = $1`, userID).Scan(&hash)
	}
	if err != nil {
		return fmt.Errorf("user not found")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(currentPassword)); err != nil {
		return fmt.Errorf("current password is incorrect")
	}
	if err := validatePassword(newPassword); err != nil {
		return err
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = us.db.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, string(newHash), userID)
	if err != nil {
		_, err = us.db.Exec(`UPDATE users SET password_hash = $1 WHERE id = $2`, string(newHash), userID)
	}
	return err
}

// AdminResetPassword allows an admin to set a new password for any user.
func (us *UserStore) AdminResetPassword(userID, newPassword string) error {
	if err := validatePassword(newPassword); err != nil {
		return err
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	result, err := us.db.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, string(newHash), userID)
	if err != nil {
		result, err = us.db.Exec(`UPDATE users SET password_hash = $1 WHERE id = $2`, string(newHash), userID)
	}
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("user not found")
	}
	return nil
}

func (us *UserStore) ListUsers() ([]User, error) {
	rows, err := us.db.Query(`SELECT id, username, role, created_at FROM users ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		var createdAt string
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &createdAt); err != nil {
			return nil, err
		}
		u.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		users = append(users, u)
	}
	return users, rows.Err()
}

func (us *UserStore) UserCount() int {
	var count int
	us.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count)
	return count
}

// --- JWT ---

var jwtSecret []byte

func InitJWTSecret() {
	// Try to load persisted secret from env or file
	if s := os.Getenv("BROKOLI_JWT_SECRET"); s != "" {
		jwtSecret = []byte(s)
		return
	}
	// Try to read from file next to the binary
	if data, err := os.ReadFile(".brokoli-jwt-secret"); err == nil && len(data) >= 32 {
		jwtSecret = data[:32]
		return
	}
	// Generate and persist so tokens survive restarts
	b := make([]byte, 32)
	rand.Read(b)
	jwtSecret = b
	if err := os.WriteFile(".brokoli-jwt-secret", b, 0600); err != nil {
		log.Printf("WARNING: failed to persist JWT secret: %v", err)
	}
}

func GenerateToken(user *User) (string, error) {
	if jwtSecret == nil {
		InitJWTSecret()
	}
	claims := jwt.MapClaims{
		"sub":      user.ID,
		"username": user.Username,
		"role":     string(user.Role),
		"exp":      time.Now().Add(24 * time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(jwtSecret)
}

// SignToken signs arbitrary JWT claims with the server's secret.
// Used by enterprise handlers (e.g. Impersonate) that build custom tokens.
func SignToken(claims jwt.MapClaims) (string, error) {
	if jwtSecret == nil {
		InitJWTSecret()
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(jwtSecret)
}

func ParseToken(tokenStr string) (*jwt.MapClaims, error) {
	if jwtSecret == nil {
		return nil, fmt.Errorf("JWT not initialized")
	}
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
		return jwtSecret, nil
	})
	if err != nil || !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, fmt.Errorf("invalid claims")
	}
	// Validate token expiry
	if exp, ok := claims["exp"].(float64); ok {
		if time.Now().Unix() > int64(exp) {
			return nil, fmt.Errorf("token expired")
		}
	}
	return &claims, nil
}

// --- HTTP Handlers ---

func LoginHandler(us *UserStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request")
			return
		}

		// Check account lockout
		if us.IsLocked(req.Username) {
			writeError(w, http.StatusTooManyRequests, "account temporarily locked — try again in 15 minutes")
			return
		}

		user, err := us.Authenticate(req.Username, req.Password)
		if err != nil {
			us.RecordLoginAttempt(req.Username, r.RemoteAddr, false)
			writeError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}

		// Success — clear failed attempts and record success
		us.ClearAttempts(req.Username)
		us.RecordLoginAttempt(req.Username, r.RemoteAddr, true)

		token, err := GenerateToken(user)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "token generation failed")
			return
		}

		writeJSON(w, http.StatusOK, map[string]interface{}{
			"token": token,
			"user":  user,
		})
	}
}

func MeHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims := r.Context().Value("claims")
		if claims == nil {
			writeError(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		writeJSON(w, http.StatusOK, claims)
	}
}

func ListUsersHandler(us *UserStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		users, err := us.ListUsers()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, users)
	}
}

func CreateUserHandler(us *UserStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
			Role     string `json:"role"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request")
			return
		}
		if req.Username == "" || req.Password == "" {
			writeError(w, http.StatusBadRequest, "username and password required")
			return
		}
		role := Role(req.Role)
		if role != RoleSuperAdmin && role != RoleAdmin && role != RoleEditor && role != RoleViewer {
			role = RoleViewer
		}

		user, err := us.CreateUser(req.Username, req.Password, role)
		if err != nil {
			writeError(w, http.StatusConflict, "user already exists")
			return
		}
		writeJSON(w, http.StatusCreated, user)
	}
}

// JWTAuth middleware — checks Bearer token. Skips if no users exist (open mode).
func JWTAuth(us *UserStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Open mode: if no users created yet, only allow auth setup and non-API routes
			if us.UserCount() == 0 {
				if strings.HasPrefix(r.URL.Path, "/api/auth/") || !strings.HasPrefix(r.URL.Path, "/api/") {
					next.ServeHTTP(w, r)
					return
				}
				// Block other API access in open mode
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "system requires initial setup — create an admin user first"})
				return
			}

			// Skip non-API routes
			if !strings.HasPrefix(r.URL.Path, "/api/") {
				next.ServeHTTP(w, r)
				return
			}

			// Skip auth endpoints
			if r.URL.Path == "/api/auth/login" || r.URL.Path == "/api/auth/setup" || r.URL.Path == "/api/auth/signup" {
				next.ServeHTTP(w, r)
				return
			}

			// Skip webhook triggers (they have their own token auth)
			if strings.Contains(r.URL.Path, "/webhook") && r.Method == "POST" {
				next.ServeHTTP(w, r)
				return
			}

			// WebSocket requires auth token and origin validation
			if strings.ToLower(r.Header.Get("Upgrade")) == "websocket" {
				// Validate origin
				origin := r.Header.Get("Origin")
				if origin != "" {
					allowedOrigins := os.Getenv("BROKOLI_CORS_ORIGINS")
					if allowedOrigins != "" && allowedOrigins != "*" {
						originAllowed := false
						for _, allowed := range strings.Split(allowedOrigins, ",") {
							if strings.TrimSpace(allowed) == origin {
								originAllowed = true
								break
							}
						}
						if !originAllowed {
							writeJSON(w, http.StatusForbidden, map[string]string{"error": "origin not allowed"})
							return
						}
					}
				}

				token := r.URL.Query().Get("token")
				if token == "" {
					token = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				}
				if token != "" {
					if _, err := ParseToken(token); err == nil {
						next.ServeHTTP(w, r)
						return
					}
				}
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
				return
			}

			// Check token: Authorization header first, then httpOnly cookie
			authHeader := r.Header.Get("Authorization")
			if !strings.HasPrefix(authHeader, "Bearer ") {
				if cookie, err := r.Cookie("brokoli_session"); err == nil && cookie.Value != "" {
					authHeader = "Bearer " + cookie.Value
				} else {
					writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
					return
				}
			}

			claims, err := ParseToken(strings.TrimPrefix(authHeader, "Bearer "))
			if err != nil {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid token"})
				return
			}

			// Add claims to context
			ctx := r.Context()
			ctx = contextWithClaims(ctx, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireRole middleware — requires specific role.
func RequireRole(roles ...Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := r.Context().Value("claims")
			if claims == nil {
				// No auth = open mode, allow
				next.ServeHTTP(w, r)
				return
			}
			mc := claims.(*jwt.MapClaims)
			userRole := Role((*mc)["role"].(string))
			for _, allowed := range roles {
				if userRole == allowed {
					next.ServeHTTP(w, r)
					return
				}
			}
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "insufficient permissions"})
		})
	}
}

// --- Helpers ---

func generateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

type contextKey string

func contextWithClaims(ctx interface{ Value(any) any }, claims *jwt.MapClaims) interface {
	Deadline() (time.Time, bool)
	Done() <-chan struct{}
	Err() error
	Value(any) any
} {
	// Use standard context
	return &claimsContext{parent: ctx.(interface {
		Deadline() (time.Time, bool)
		Done() <-chan struct{}
		Err() error
		Value(any) any
	}), claims: claims}
}

type claimsContext struct {
	parent interface {
		Deadline() (time.Time, bool)
		Done() <-chan struct{}
		Err() error
		Value(any) any
	}
	claims *jwt.MapClaims
}

func (c *claimsContext) Deadline() (time.Time, bool) { return c.parent.Deadline() }
func (c *claimsContext) Done() <-chan struct{}       { return c.parent.Done() }
func (c *claimsContext) Err() error                  { return c.parent.Err() }
func (c *claimsContext) Value(key any) any {
	if k, ok := key.(string); ok && k == "claims" {
		return c.claims
	}
	return c.parent.Value(key)
}
