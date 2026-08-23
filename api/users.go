package api

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/Tnsor-Labs/brokoli/store"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// Sentinel errors returned by CreateUser so the handler can map them to the
// correct HTTP status without string-matching the underlying driver error.
var (
	// ErrUserExists is returned when the username is already taken.
	ErrUserExists = errors.New("user already exists")
	// ErrInvalidPassword wraps the underlying password validation error.
	// The wrapped error carries the human-readable reason (length, mixed case,
	// digit requirement, etc.) which the handler surfaces to the caller.
	ErrInvalidPassword = errors.New("invalid password")
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
	ID       string `json:"id"`
	Username string `json:"username"`
	// DisplayName is the human name to show in the UI; Email is the
	// address the account is reachable at. Both are optional and both
	// are empty for password accounts created with just a username.
	// They exist because SSO providers hand us a real name and address,
	// and without somewhere to put them the provider-prefixed username
	// (e.g. "google_someone@example.com") ends up greeting the user.
	// Identity still keys on Username — these are presentation only.
	DisplayName string    `json:"display_name,omitempty"`
	Email       string    `json:"email,omitempty"`
	Role        Role      `json:"role"`
	CreatedAt   time.Time `json:"created_at"`
}

// DisplayLabel returns the best human label for a user: the display
// name when the account has one, otherwise the username.
func (u *User) DisplayLabel() string {
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return u.Username
}

// UserStore handles user persistence. Uses the same DB connection.
type UserStore struct {
	db *sql.DB
	// attempts is where failed-login state lives. It is the store layer's
	// implementation rather than SQL written here: this type only has a
	// raw *sql.DB, and writing lockout queries against it meant guessing
	// the column types the store had already chosen. It guessed wrong on
	// Postgres — an integer into a boolean column, an RFC3339 string into
	// a timestamptz — and since every error on that path was discarded,
	// the table sat at zero rows through thousands of logins while
	// IsLocked cheerfully answered "not locked".
	attempts store.LoginAttemptStore

	// dialect is "postgres" or "sqlite", detected once at construction.
	// This store writes its own SQL against whatever *sql.DB the server
	// opened, so it has to know which one it got: SQLite DDL sent to a
	// Postgres server does not create a table, it logs a syntax error
	// nobody reads. That is how account lockout came to be silently
	// disabled on every Postgres deployment.
	dialect string
}

// Account lockout policy: 5 failed attempts within 15 minutes.
const (
	lockoutThreshold = 5
	lockoutWindow    = 15 * time.Minute
)

// UseLoginAttemptStore wires the lockout backend. Without it the store
// cannot enforce lockout, and says so rather than pretending.
func (us *UserStore) UseLoginAttemptStore(a store.LoginAttemptStore) {
	us.attempts = a
}

// detectUserStoreDialect asks the server rather than the connection
// string, which the store never sees.
func detectUserStoreDialect(db *sql.DB) string {
	var one int
	if err := db.QueryRow("SELECT 1::int").Scan(&one); err == nil {
		return "postgres"
	}
	return "sqlite"
}

// q rewrites ? placeholders to $1, $2, ... on Postgres, and leaves them
// alone on SQLite.
func (us *UserStore) q(query string) string {
	if us.dialect != "postgres" {
		return query
	}
	var b strings.Builder
	n := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			b.WriteString("$")
			b.WriteString(strconv.Itoa(n))
			n++
		} else {
			b.WriteByte(query[i])
		}
	}
	return b.String()
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

	// Additive columns for installs whose users table predates them.
	// Both dialects error on a duplicate column and both are fine to
	// ignore: this runs on every boot, so "already exists" is the
	// normal case, not a failure.
	_, _ = db.Exec(`ALTER TABLE users ADD COLUMN display_name TEXT NOT NULL DEFAULT ''`)
	_, _ = db.Exec(`ALTER TABLE users ADD COLUMN email TEXT NOT NULL DEFAULT ''`)

	us := &UserStore{db: db, dialect: detectUserStoreDialect(db)}

	// login_attempts is deliberately not created here. The store layer
	// already owns that table and creates it correctly for both dialects
	// (store/postgres.go, store/sqlite.go); a second creator racing it is
	// how the schemas diverged in the first place — whichever ran first
	// won, and on Postgres a CREATE with SQLite's AUTOINCREMENT is a
	// syntax error that was discarded unchecked.

	return us, nil
}

// RecordLoginAttempt records a login attempt for lockout tracking.
func (us *UserStore) RecordLoginAttempt(username, ip string, success bool) {
	if us.attempts == nil {
		return // already warned at construction
	}
	if err := us.attempts.RecordLoginAttempt(username, ip, success); err != nil {
		// Logged rather than returned: the caller is the login path, and a
		// failure to record an attempt must not fail a valid login. It has
		// to be visible though — silence here is what let lockout stay
		// broken unnoticed.
		log.Printf("WARNING: could not record login attempt for %q (account lockout will not count it): %v", username, err)
	}
}

// IsLocked reports whether the account has 5+ failed attempts in the last
// 15 minutes.
//
// A failed count reports locked, not unlocked. This is a rate limiter, so
// the instinct is to fail open and let people in — but the count only
// fails when the database is unreachable, and authentication needs the
// same database, so nobody was logging in either way. Failing open costs
// nothing during an outage and removes brute-force protection whenever
// one can be induced.
func (us *UserStore) IsLocked(username string) bool {
	if us.attempts == nil {
		return false // already warned at construction; no lockout available
	}
	count, err := us.attempts.GetRecentFailedAttempts(username, time.Now().Add(-lockoutWindow))
	if err != nil {
		log.Printf("WARNING: could not check lockout state for %q, treating as locked: %v", username, err)
		return true
	}
	return count >= lockoutThreshold
}

// ClearAttempts removes all login attempts for a user (called on successful login).
func (us *UserStore) ClearAttempts(username string) {
	if us.attempts == nil {
		return
	}
	if err := us.attempts.ClearLoginAttempts(username); err != nil {
		log.Printf("WARNING: could not clear login attempts for %q: %v", username, err)
	}
}

// IsSuperAdmin checks whether the given user has the superadmin role.
func (us *UserStore) IsSuperAdmin(userID string) bool {
	var role string
	us.db.QueryRow(us.q(`SELECT role FROM users WHERE id = ?`), userID).Scan(&role)
	if role == "" {
		us.db.QueryRow(`SELECT role FROM users WHERE id = $1`, userID).Scan(&role)
	}
	return role == "superadmin"
}

// GetUserByID returns a user by ID.
func (us *UserStore) GetUserByID(id string) (*User, error) {
	var u User
	var createdAt string
	err := us.db.QueryRow(us.q(`SELECT id, username, display_name, email, role, created_at FROM users WHERE id = ?`), id).
		Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.Role, &createdAt)
	if err != nil {
		err = us.db.QueryRow(`SELECT id, username, display_name, email, role, created_at FROM users WHERE id = $1`, id).
			Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.Role, &createdAt)
	}
	if err != nil {
		return nil, fmt.Errorf("user not found")
	}
	u.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	return &u, nil
}

func (us *UserStore) CreateUser(username, password string, role Role) (*User, error) {
	if err := validatePassword(password); err != nil {
		// Wrap so the handler can errors.Is(err, ErrInvalidPassword) AND still
		// recover the human-readable reason via err.Error().
		return nil, fmt.Errorf("%w: %s", ErrInvalidPassword, err.Error())
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}
	id := generateID()
	now := time.Now()
	_, err = us.db.Exec(
		us.q(`INSERT INTO users (id, username, password_hash, role, created_at) VALUES (?, ?, ?, ?, ?)`),
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
		// Both SQLite and Postgres surface UNIQUE constraint violations as
		// errors whose Error() contains "UNIQUE" or "unique". Map those to
		// ErrUserExists; everything else stays as-is and becomes a 500.
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "unique") || strings.Contains(msg, "duplicate") {
			return nil, ErrUserExists
		}
		return nil, fmt.Errorf("insert user: %w", err)
	}
	return &User{ID: id, Username: username, Role: role, CreatedAt: now}, nil
}

func (us *UserStore) Authenticate(username, password string) (*User, error) {
	var u User
	var hash, createdAt string

	err := us.db.QueryRow(
		us.q(`SELECT id, username, display_name, email, password_hash, role, created_at FROM users WHERE username = ?`), username,
	).Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &hash, &u.Role, &createdAt)
	if err != nil {
		// Try Postgres
		err = us.db.QueryRow(
			`SELECT id, username, display_name, email, password_hash, role, created_at FROM users WHERE username = $1`, username,
		).Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &hash, &u.Role, &createdAt)
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
	err := us.db.QueryRow(us.q(`SELECT password_hash FROM users WHERE id = ?`), userID).Scan(&hash)
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
	_, err = us.db.Exec(us.q(`UPDATE users SET password_hash = ? WHERE id = ?`), string(newHash), userID)
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
	result, err := us.db.Exec(us.q(`UPDATE users SET password_hash = ? WHERE id = ?`), string(newHash), userID)
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

// SetProfile updates the presentation fields of an account. Empty
// arguments are ignored rather than written, so a provider that returns
// only some of them (or a later login that returns none) never blanks
// out what an earlier one supplied.
//
// The three cases are spelled out as literal statements rather than
// assembled from fragments: there are only three, and a fixed string
// per case keeps the SQL obviously parameterized. Placeholders are
// rewritten for the dialect by q(); this used to run the ? form and
// retry the $1 form on error, which worked but filled the Postgres log
// with syntax errors on every profile update.
func (us *UserStore) SetProfile(userID, displayName, email string) error {
	var query string
	var args []any
	switch {
	case displayName != "" && email != "":
		query = `UPDATE users SET display_name = ?, email = ? WHERE id = ?`
		args = []any{displayName, email, userID}
	case displayName != "":
		query = `UPDATE users SET display_name = ? WHERE id = ?`
		args = []any{displayName, userID}
	case email != "":
		query = `UPDATE users SET email = ? WHERE id = ?`
		args = []any{email, userID}
	default:
		return nil
	}

	_, err := us.db.Exec(us.q(query), args...)
	return err
}

func (us *UserStore) ListUsers() ([]User, error) {
	rows, err := us.db.Query(`SELECT id, username, display_name, email, role, created_at FROM users ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		var createdAt string
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.Role, &createdAt); err != nil {
			return nil, err
		}
		u.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		users = append(users, u)
	}
	return users, rows.Err()
}

// ListUsersByOrg returns users that belong to a specific org via org_members join.
func (us *UserStore) ListUsersByOrg(orgID string) ([]User, error) {
	rows, err := us.db.Query(
		`SELECT u.id, u.username, u.display_name, u.email, u.role, u.created_at
		 FROM users u
		 INNER JOIN org_members om ON u.id = om.user_id
		 WHERE om.org_id = $1
		 ORDER BY u.created_at`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		var u User
		var createdAt string
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.Role, &createdAt); err != nil {
			return nil, err
		}
		u.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		users = append(users, u)
	}
	return users, rows.Err()
}

// UserCountErr reports how many users exist, and whether that could be
// determined at all.
//
// The distinction is a security boundary, not a nicety. Zero users means
// "fresh install", and a fresh install runs in open mode so the first
// admin can be created without credentials. UserCount used to discard the
// scan error and return 0, so any transient database failure — a
// restarting server, an exhausted pool, a network blip — presented a
// fully provisioned system as an unconfigured one, and open mode let an
// unauthenticated caller create an admin account. Observed happening on
// its own under load, not merely in theory.
//
// Callers must treat an error as "cannot tell" and refuse, never as zero.
func (us *UserStore) UserCountErr() (int, error) {
	var count int
	if err := us.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return count, nil
}

// UserCount reports the number of users, or -1 if it cannot be determined.
//
// The sentinel is deliberately not 0: every guard in this package asks
// whether the system is unconfigured, and a failure answering that
// question must never be mistaken for "yes". Prefer UserCountErr in new
// code.
func (us *UserStore) UserCount() int {
	count, err := us.UserCountErr()
	if err != nil {
		return -1
	}
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
	// Presentation fields ride along so /api/auth/me (which returns the
	// claims verbatim) can label the signed-in user without a second
	// lookup. Omitted when empty to keep password-account tokens as they
	// were. Identity remains sub/username — never these.
	if user.DisplayName != "" {
		claims["display_name"] = user.DisplayName
	}
	if user.Email != "" {
		claims["email"] = user.Email
	}
	// Include org_id if enterprise org resolver is configured
	if OrgResolverFunc != nil {
		if orgID := OrgResolverFunc(user.ID); orgID != "" {
			claims["org_id"] = orgID
		}
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
		if PasswordLoginEnabledFunc != nil && !PasswordLoginEnabledFunc() {
			writeError(w, http.StatusForbidden, "password login is disabled; use an enabled OAuth provider")
			return
		}

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
		orgID := GetOrgIDFromRequest(r)

		// Superadmin or community mode: show all users
		role := ""
		if claims, ok := r.Context().Value("claims").(*jwt.MapClaims); ok {
			role, _ = (*claims)["role"].(string)
		}

		var users []User
		var err error
		if orgID != "" && role != "superadmin" {
			users, err = us.ListUsersByOrg(orgID)
		} else {
			users, err = us.ListUsers()
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if users == nil {
			users = []User{}
		}
		writeJSON(w, http.StatusOK, users)
	}
}

// UserPostCreateHook is called after a user is successfully created via
// CreateUserHandler. Enterprise platform sets this to attach the new user
// to an org (default org for setup, caller's org otherwise). Without this
// hook, users created via /api/auth/users in enterprise mode have no org
// membership and are silently unable to see any data — every list filter
// rejects empty-org users in multi-tenant mode.
//
// The hook receives the new user and the request so it can inspect the
// caller's claims. Return an error to fail the user creation (the caller
// sees 500); returning nil commits success.
var UserPostCreateHook func(user *User, r *http.Request) error

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

		// If users already exist, require admin role to create new users.
		// First user creation (setup) is allowed without auth — so a
		// count that cannot be established has to refuse rather than
		// assume a fresh install, or a database blip becomes a way to
		// create an admin without credentials.
		userCount, countErr := us.UserCountErr()
		if countErr != nil {
			log.Printf("CreateUser: refusing, user count unavailable: %v", countErr)
			writeError(w, http.StatusServiceUnavailable, "cannot verify system state; try again")
			return
		}
		if userCount > 0 {
			claims := r.Context().Value("claims")
			if claims == nil {
				writeError(w, http.StatusUnauthorized, "authentication required")
				return
			}
			mc := claims.(*jwt.MapClaims)
			callerRole, _ := (*mc)["role"].(string)
			if callerRole != "admin" && callerRole != "superadmin" {
				writeError(w, http.StatusForbidden, "admin role required to create users")
				return
			}
		}

		role := Role(req.Role)
		if role != RoleSuperAdmin && role != RoleAdmin && role != RoleEditor && role != RoleViewer {
			role = RoleViewer
		}

		// First user must be admin (prevent creating viewer-only accounts during setup)
		if userCount == 0 {
			role = RoleAdmin
		}

		user, err := us.CreateUser(req.Username, req.Password, role)
		if err != nil {
			switch {
			case errors.Is(err, ErrInvalidPassword):
				// Surface the actual validation reason ("password must be at
				// least 10 characters", etc.) so the caller knows what to fix.
				writeError(w, http.StatusBadRequest, err.Error())
			case errors.Is(err, ErrUserExists):
				writeError(w, http.StatusConflict, "user already exists")
			default:
				log.Printf("CreateUser failed: %v", err)
				writeError(w, http.StatusInternalServerError, "failed to create user")
			}
			return
		}

		// Enterprise hook: attach the new user to an organization so they
		// aren't stranded without an org_id. Without this, admin-created
		// users (via /api/auth/users) in multi-tenant mode can log in but
		// see nothing — every list endpoint filters by org_id and rejects
		// empty-org users to prevent cross-tenant data leaks.
		if UserPostCreateHook != nil {
			if err := UserPostCreateHook(user, r); err != nil {
				log.Printf("UserPostCreateHook failed for %s: %v", user.Username, err)
				writeError(w, http.StatusInternalServerError, "failed to attach user to organization")
				return
			}
		}

		writeJSON(w, http.StatusCreated, user)
	}
}

// openModeCtxKey marks a request that JWTAuth deliberately let through
// because the system has no users yet and is waiting to be set up.
//
// It exists because "no claims" is ambiguous: it is what an open-mode
// request looks like, and equally what a request looks like when the
// authentication middleware was never mounted — which NewServer does
// whenever the user store could not be built. Reading the first meaning
// into the second served every permission-gated route to anyone.
type openModeCtxKey struct{}

func withOpenMode(ctx context.Context) context.Context {
	return context.WithValue(ctx, openModeCtxKey{}, true)
}

// IsOpenMode reports whether JWTAuth passed this request through as an
// unconfigured system awaiting its first user.
func IsOpenMode(r *http.Request) bool {
	v, _ := r.Context().Value(openModeCtxKey{}).(bool)
	return v
}

// JWTAuth middleware — checks Bearer token. Skips if no users exist (open mode).
func JWTAuth(us *UserStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isPublicCapabilitiesRequest(r) {
				next.ServeHTTP(w, r)
				return
			}

			// A static API key already authenticated this request (see
			// APIKeyAuth, which runs first and stamps claims on success).
			// That's a complete identity on its own -- don't additionally
			// demand a JWT a static key was never going to produce, and
			// don't block it behind the zero-users open-mode gate below
			// either, since presenting a valid operator key is exactly
			// what that gate exists to require in the first place.
			if r.Context().Value("claims") != nil {
				next.ServeHTTP(w, r)
				return
			}

			// Open mode: if no users created yet, only allow auth setup and
			// non-API routes. A count that cannot be established is not
			// zero — answering "unconfigured" to a database outage would
			// drop authentication on a live system.
			userCount, countErr := us.UserCountErr()
			if countErr != nil {
				log.Printf("auth: refusing request, user count unavailable: %v", countErr)
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "authentication is temporarily unavailable"})
				return
			}
			if userCount == 0 {
				if strings.HasPrefix(r.URL.Path, "/api/auth/") || !strings.HasPrefix(r.URL.Path, "/api/") {
					// Mark the request as deliberately unauthenticated.
					// HasPermission used to infer this from the absence of
					// claims, which is also what a request that never met
					// this middleware looks like — so a server started
					// without JWTAuth answered permission-gated routes to
					// anyone. Absence is not a decision; this is.
					next.ServeHTTP(w, r.WithContext(withOpenMode(r.Context())))
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
			if r.URL.Path == "/api/auth/login" || r.URL.Path == "/api/auth/logout" || r.URL.Path == "/api/auth/setup" || r.URL.Path == "/api/auth/signup" || r.URL.Path == "/api/auth/methods" {
				next.ServeHTTP(w, r)
				return
			}

			// Skip OAuth endpoints (redirect flows — no JWT)
			if strings.HasPrefix(r.URL.Path, "/api/auth/oauth/") {
				next.ServeHTTP(w, r)
				return
			}

			// Skip worker endpoints (they use work pool token auth)
			if strings.HasPrefix(r.URL.Path, "/api/workers/") {
				next.ServeHTTP(w, r)
				return
			}

			// Skip invite endpoints (public access for accepting invites)
			if strings.HasPrefix(r.URL.Path, "/api/invites/") {
				next.ServeHTTP(w, r)
				return
			}

			// Skip sample data endpoints (public)
			if strings.HasPrefix(r.URL.Path, "/api/samples/") {
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

				token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				// Fall back to httpOnly session cookie for WebSocket auth
				if token == "" {
					if cookie, err := r.Cookie("brokoli_session"); err == nil && cookie.Value != "" {
						token = cookie.Value
					}
				}
				if token != "" {
					if claims, err := ParseToken(token); err == nil {
						// Propagate claims and org_id into the request context so
						// downstream WebSocket handlers (sodp.Server.HandleWS) can
						// enforce per-session tenant isolation. Without this the
						// SODP server treats every session as the "default" org
						// and multi-tenant separation collapses on the WS path.
						ctx := contextWithClaims(r.Context(), claims)
						orgID, _ := (*claims)["org_id"].(string)
						if orgID == "" && OrgResolverFunc != nil {
							if sub, ok := (*claims)["sub"].(string); ok {
								orgID = OrgResolverFunc(sub)
							}
						}
						if orgID != "" {
							(*claims)["org_id"] = orgID
							ctx = context.WithValue(ctx, OrgIDContextKey{}, orgID)
						}
						next.ServeHTTP(w, r.WithContext(ctx))
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
			// Extract org_id from JWT claims and set in context for data isolation
			orgID, _ := (*claims)["org_id"].(string)
			// If org_id missing from JWT (old token), resolve it dynamically
			if orgID == "" && OrgResolverFunc != nil {
				if sub, ok := (*claims)["sub"].(string); ok {
					orgID = OrgResolverFunc(sub)
				}
			}
			if orgID != "" {
				(*claims)["org_id"] = orgID
				ctx = context.WithValue(ctx, OrgIDContextKey{}, orgID)
			}
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
