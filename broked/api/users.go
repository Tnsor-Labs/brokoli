package api

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// Role defines user permission levels.
type Role string

const (
	RoleAdmin  Role = "admin"
	RoleEditor Role = "editor"
	RoleViewer Role = "viewer"
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
	return &UserStore{db: db}, nil
}

func (us *UserStore) CreateUser(username, password string, role Role) (*User, error) {
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
	b := make([]byte, 32)
	rand.Read(b)
	jwtSecret = b
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

		user, err := us.Authenticate(req.Username, req.Password)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}

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
		if role != RoleAdmin && role != RoleEditor && role != RoleViewer {
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
			// Skip auth if no users created yet (open mode)
			if us.UserCount() == 0 {
				next.ServeHTTP(w, r)
				return
			}

			// Skip non-API routes
			if !strings.HasPrefix(r.URL.Path, "/api/") {
				next.ServeHTTP(w, r)
				return
			}

			// Skip auth endpoints
			if r.URL.Path == "/api/auth/login" || r.URL.Path == "/api/auth/setup" {
				next.ServeHTTP(w, r)
				return
			}

			// Skip WebSocket
			if strings.ToLower(r.Header.Get("Upgrade")) == "websocket" {
				next.ServeHTTP(w, r)
				return
			}

			// Check token
			authHeader := r.Header.Get("Authorization")
			if !strings.HasPrefix(authHeader, "Bearer ") {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
				return
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
