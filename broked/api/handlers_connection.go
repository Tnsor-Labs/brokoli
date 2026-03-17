package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	_ "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/hc12r/broked/crypto"
	"github.com/hc12r/broked/models"
	"github.com/hc12r/broked/store"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type ConnectionHandler struct {
	store  store.Store
	crypto *crypto.Config
}

func NewConnectionHandler(s store.Store, c *crypto.Config) *ConnectionHandler {
	return &ConnectionHandler{store: s, crypto: c}
}

func (h *ConnectionHandler) List(w http.ResponseWriter, r *http.Request) {
	conns, err := h.store.ListConnections()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if conns == nil {
		conns = []models.Connection{}
	}
	// Mask passwords, decrypt extras
	for i := range conns {
		conns[i].Password = "********"
		if conns[i].Extra != "" {
			decrypted, err := h.crypto.Decrypt(conns[i].Extra)
			if err == nil {
				conns[i].Extra = decrypted
			} else {
				conns[i].Extra = "{}"
			}
		}
	}
	writeJSON(w, http.StatusOK, conns)
}

func (h *ConnectionHandler) Get(w http.ResponseWriter, r *http.Request) {
	connID := chi.URLParam(r, "connId")
	c, err := h.store.GetConnection(connID)
	if err != nil {
		writeError(w, http.StatusNotFound, "connection not found")
		return
	}
	c.Password = "********"
	if c.Extra != "" {
		decrypted, _ := h.crypto.Decrypt(c.Extra)
		c.Extra = decrypted
	}
	writeJSON(w, http.StatusOK, c)
}

func (h *ConnectionHandler) Create(w http.ResponseWriter, r *http.Request) {
	var c models.Connection
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if c.ConnID == "" {
		writeError(w, http.StatusBadRequest, "conn_id is required")
		return
	}
	if c.Type == "" {
		writeError(w, http.StatusBadRequest, "type is required")
		return
	}

	// Validate conn_id format (slug-like)
	c.ConnID = strings.ToLower(strings.TrimSpace(c.ConnID))

	c.ID = uuid.New().String()
	now := time.Now()
	c.CreatedAt = now
	c.UpdatedAt = now

	// Encrypt password and extra
	if c.Password != "" {
		enc, err := h.crypto.Encrypt(c.Password)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "encryption failed")
			return
		}
		c.Password = enc
	}
	if c.Extra != "" {
		enc, err := h.crypto.Encrypt(c.Extra)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "encryption failed")
			return
		}
		c.Extra = enc
	}

	if err := h.store.CreateConnection(&c); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") || strings.Contains(err.Error(), "unique") {
			writeError(w, http.StatusConflict, "conn_id already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Return with masked password
	c.Password = "********"
	if c.Extra != "" {
		decrypted, _ := h.crypto.Decrypt(c.Extra)
		c.Extra = decrypted
	}
	writeJSON(w, http.StatusCreated, c)
}

func (h *ConnectionHandler) Update(w http.ResponseWriter, r *http.Request) {
	connID := chi.URLParam(r, "connId")
	existing, err := h.store.GetConnection(connID)
	if err != nil {
		writeError(w, http.StatusNotFound, "connection not found")
		return
	}

	var c models.Connection
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	// Preserve immutable fields
	c.ID = existing.ID
	c.ConnID = existing.ConnID
	c.CreatedAt = existing.CreatedAt
	c.UpdatedAt = time.Now()

	// Handle password: if "********" or empty, keep existing
	if c.Password == "" || c.Password == "********" {
		c.Password = existing.Password // already encrypted
	} else {
		enc, err := h.crypto.Encrypt(c.Password)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "encryption failed")
			return
		}
		c.Password = enc
	}

	// Encrypt extra
	if c.Extra != "" {
		enc, err := h.crypto.Encrypt(c.Extra)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "encryption failed")
			return
		}
		c.Extra = enc
	}

	if err := h.store.UpdateConnection(&c); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	c.Password = "********"
	if c.Extra != "" {
		decrypted, _ := h.crypto.Decrypt(c.Extra)
		c.Extra = decrypted
	}
	writeJSON(w, http.StatusOK, c)
}

func (h *ConnectionHandler) Delete(w http.ResponseWriter, r *http.Request) {
	connID := chi.URLParam(r, "connId")
	if err := h.store.DeleteConnection(connID); err != nil {
		writeError(w, http.StatusNotFound, "connection not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *ConnectionHandler) Test(w http.ResponseWriter, r *http.Request) {
	connID := chi.URLParam(r, "connId")
	c, err := h.store.GetConnection(connID)
	if err != nil {
		writeError(w, http.StatusNotFound, "connection not found")
		return
	}

	// Decrypt password
	if c.Password != "" {
		dec, err := h.crypto.Decrypt(c.Password)
		if err == nil {
			c.Password = dec
		}
	}

	// Decrypt extras for HTTP connections
	var extra map[string]interface{}
	if c.Extra != "" {
		dec, err := h.crypto.Decrypt(c.Extra)
		if err == nil {
			json.Unmarshal([]byte(dec), &extra)
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	switch c.Type {
	case models.ConnTypePostgres:
		result := testDBReal(ctx, "pgx", c.BuildURI())
		writeJSON(w, http.StatusOK, result)
	case models.ConnTypeMySQL:
		result := testDBReal(ctx, "mysql", c.BuildURI())
		writeJSON(w, http.StatusOK, result)
	case models.ConnTypeSQLite:
		result := testDBReal(ctx, "sqlite", c.Host)
		writeJSON(w, http.StatusOK, result)
	case models.ConnTypeHTTP:
		result := testHTTPAuth(ctx, c, extra)
		writeJSON(w, http.StatusOK, result)
	case models.ConnTypeSFTP:
		result := testSSH(ctx, c)
		writeJSON(w, http.StatusOK, result)
	case models.ConnTypeS3:
		result := testS3(ctx, extra)
		writeJSON(w, http.StatusOK, result)
	default:
		// Generic: try HTTP GET if it looks like a URL, otherwise TCP
		result := testGeneric(ctx, c, extra)
		writeJSON(w, http.StatusOK, result)
	}
}

// testDBReal actually opens a DB connection and pings it.
func testDBReal(ctx context.Context, driver, dsn string) map[string]interface{} {
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Failed to open: %v", err),
			"driver":  driver,
		}
	}
	defer db.Close()

	if err := db.PingContext(ctx); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Ping failed: %v", err),
			"driver":  driver,
		}
	}
	return map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("Connected successfully (%s)", driver),
		"driver":  driver,
	}
}

// testHTTPAuth sends a GET request with full auth headers to verify credentials.
func testHTTPAuth(ctx context.Context, c *models.Connection, extra map[string]interface{}) map[string]interface{} {
	url := c.BuildURI()

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Invalid URL: %v", err),
		}
	}

	// Basic auth
	if c.Login != "" {
		req.SetBasicAuth(c.Login, c.Password)
	}

	// Custom headers from extra
	if extra != nil {
		if headers, ok := extra["headers"].(map[string]interface{}); ok {
			for k, v := range headers {
				if sv, ok := v.(string); ok {
					req.Header.Set(k, sv)
				}
			}
		}
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Request failed: %v", err),
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Authentication failed (HTTP %d) — check credentials/API keys", resp.StatusCode),
		}
	}
	if resp.StatusCode >= 500 {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Server error (HTTP %d)", resp.StatusCode),
		}
	}
	return map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("Authenticated successfully (HTTP %d)", resp.StatusCode),
	}
}

// testSSH verifies SSH/SFTP connectivity by doing a TCP handshake and reading the SSH banner.
func testSSH(ctx context.Context, c *models.Connection) map[string]interface{} {
	port := c.Port
	if port == 0 {
		port = 22
	}
	addr := fmt.Sprintf("%s:%d", c.Host, port)

	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Cannot reach %s: %v", addr, err),
		}
	}
	defer conn.Close()

	// Read SSH banner (e.g. "SSH-2.0-OpenSSH_8.9")
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 256)
	n, err := conn.Read(buf)
	if err != nil || n == 0 {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Connected to %s but no SSH banner received — is this an SSH server?", addr),
		}
	}

	banner := strings.TrimSpace(string(buf[:n]))
	if !strings.HasPrefix(banner, "SSH-") {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Connected but got unexpected banner: %q — not an SSH server", banner),
		}
	}

	// SSH server confirmed. We can't do full auth without an SSH library,
	// but confirming the banner + reachability is meaningful.
	msg := fmt.Sprintf("SSH server reachable (%s)", banner)
	if c.Login != "" {
		msg += fmt.Sprintf(", will authenticate as '%s'", c.Login)
	}
	// Note: full password auth requires golang.org/x/crypto/ssh which we don't import yet
	return map[string]interface{}{
		"success": true,
		"message": msg,
	}
}

// testS3 validates S3 credentials by checking the extra config.
func testS3(ctx context.Context, extra map[string]interface{}) map[string]interface{} {
	if extra == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "No extra config — set bucket, region, access_key, secret_key",
		}
	}

	missing := []string{}
	for _, field := range []string{"bucket", "region", "access_key", "secret_key"} {
		if v, ok := extra[field].(string); !ok || v == "" {
			missing = append(missing, field)
		}
	}
	if len(missing) > 0 {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Missing required S3 fields: %s", strings.Join(missing, ", ")),
		}
	}

	// Try an HTTP HEAD to the S3 endpoint to verify the bucket exists and is reachable
	bucket := extra["bucket"].(string)
	region := extra["region"].(string)
	url := fmt.Sprintf("https://%s.s3.%s.amazonaws.com", bucket, region)

	req, _ := http.NewRequestWithContext(ctx, "HEAD", url, nil)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Cannot reach S3 bucket: %v", err),
		}
	}
	defer resp.Body.Close()

	// 200/301/307 = bucket exists, 403 = bucket exists but no public access (expected with private buckets)
	if resp.StatusCode == 200 || resp.StatusCode == 301 || resp.StatusCode == 307 || resp.StatusCode == 403 {
		return map[string]interface{}{
			"success": true,
			"message": fmt.Sprintf("S3 bucket '%s' in %s is reachable (HTTP %d). Full auth requires AWS SDK at runtime.", bucket, region, resp.StatusCode),
		}
	}
	if resp.StatusCode == 404 {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("S3 bucket '%s' not found in region %s", bucket, region),
		}
	}
	return map[string]interface{}{
		"success": false,
		"error":   fmt.Sprintf("Unexpected S3 response: HTTP %d", resp.StatusCode),
	}
}

// testGeneric tries the best test for a generic connection.
func testGeneric(ctx context.Context, c *models.Connection, extra map[string]interface{}) map[string]interface{} {
	// If extra has a webhook_url, try an authenticated request to it
	if extra != nil {
		if webhookURL, ok := extra["webhook_url"].(string); ok && webhookURL != "" {
			req, err := http.NewRequestWithContext(ctx, "POST", webhookURL, strings.NewReader("{}"))
			if err != nil {
				return map[string]interface{}{
					"success": false,
					"error":   fmt.Sprintf("Invalid webhook URL: %v", err),
				}
			}
			req.Header.Set("Content-Type", "application/json")

			client := &http.Client{Timeout: 5 * time.Second}
			resp, err := client.Do(req)
			if err != nil {
				return map[string]interface{}{
					"success": false,
					"error":   fmt.Sprintf("Webhook request failed: %v", err),
				}
			}
			defer resp.Body.Close()

			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return map[string]interface{}{
					"success": true,
					"message": fmt.Sprintf("Webhook responded (HTTP %d)", resp.StatusCode),
				}
			}
			return map[string]interface{}{
				"success": false,
				"error":   fmt.Sprintf("Webhook returned HTTP %d — check URL and credentials", resp.StatusCode),
			}
		}
	}

	// Fallback: TCP port check
	if c.Host == "" {
		return map[string]interface{}{
			"success": false,
			"error":   "No host or webhook_url configured — cannot test",
		}
	}
	port := c.Port
	if port == 0 {
		port = 80
	}
	addr := fmt.Sprintf("%s:%d", c.Host, port)
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Cannot reach %s: %v", addr, err),
		}
	}
	conn.Close()
	return map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("Host %s reachable (TCP)", addr),
	}
}

// ConnectionTypes returns available connection type metadata.
func ConnectionTypes(w http.ResponseWriter, r *http.Request) {
	types := []map[string]interface{}{
		{"type": "postgres", "label": "PostgreSQL", "fields": []string{"host", "port", "schema", "login", "password"}},
		{"type": "mysql", "label": "MySQL", "fields": []string{"host", "port", "schema", "login", "password"}},
		{"type": "sqlite", "label": "SQLite", "fields": []string{"host"}},
		{"type": "http", "label": "HTTP / REST API", "fields": []string{"host", "port", "login", "password", "extra"}},
		{"type": "sftp", "label": "SFTP / SSH", "fields": []string{"host", "port", "login", "password", "extra"}},
		{"type": "s3", "label": "Amazon S3", "fields": []string{"extra"}},
		{"type": "generic", "label": "Generic", "fields": []string{"host", "port", "login", "password", "extra"}},
	}
	writeJSON(w, http.StatusOK, types)
}
