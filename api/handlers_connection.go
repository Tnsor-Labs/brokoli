package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	_ "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/Tnsor-Labs/brokoli/crypto"
	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
	"github.com/Tnsor-Labs/brokoli/pkg/sftpclient"
	"github.com/Tnsor-Labs/brokoli/store"
	"github.com/go-chi/chi/v5"
	_ "github.com/go-sql-driver/mysql"
	"github.com/golang-jwt/jwt/v5"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type ConnectionHandler struct {
	store  store.Store
	crypto *crypto.Config
}

// validateConnectionAccess checks if a connection exists in the user's org-scoped connection set.
func (h *ConnectionHandler) validateConnectionAccess(r *http.Request, connID string) bool {
	orgID := GetOrgIDFromRequest(r)
	if orgID == "" {
		return true // community edition
	}
	// Get user's actual workspaces (not the spoofable header)
	var userWSIDs []string
	if UserWorkspaceResolverFunc != nil {
		if claims, ok := r.Context().Value("claims").(*jwt.MapClaims); ok {
			if sub, ok := (*claims)["sub"].(string); ok {
				userWSIDs = UserWorkspaceResolverFunc(sub)
			}
		}
	}
	if len(userWSIDs) == 0 {
		return false // org user with no workspaces = no connections
	}
	// Check each of the user's workspaces for the connection
	for _, wsID := range userWSIDs {
		conns, _ := h.store.ListConnectionsByWorkspace(wsID)
		for _, c := range conns {
			if c.ConnID == connID {
				return true
			}
		}
	}
	return false
}

func NewConnectionHandler(s store.Store, c *crypto.Config) *ConnectionHandler {
	return &ConnectionHandler{store: s, crypto: c}
}

func (h *ConnectionHandler) List(w http.ResponseWriter, r *http.Request) {
	// Org-scoped users should only see connections from their workspace,
	// not the shared "default" workspace from other orgs. effectiveWorkspace
	// holds that rule for every list; it was written here first and the
	// pipeline lists now share it rather than keeping a second copy.
	wsID, ok := effectiveWorkspace(r)
	if !ok {
		// No workspace = no connections
		writeJSON(w, http.StatusOK, []models.Connection{})
		return
	}
	// Paginated — use SQL LIMIT/OFFSET
	if r.URL.Query().Get("page") != "" {
		pp := ParsePageParams(r)
		conns, total, err := h.store.ListConnectionsByWorkspacePaged(wsID, pp.Limit(), pp.Offset())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		maskConnections(conns)
		writeJSON(w, http.StatusOK, store.NewPageResult(conns, total, pp))
		return
	}

	conns, err := h.store.ListConnectionsByWorkspace(wsID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if conns == nil {
		conns = []models.Connection{}
	}
	maskConnections(conns)
	writeJSON(w, http.StatusOK, conns)
}

func (h *ConnectionHandler) Get(w http.ResponseWriter, r *http.Request) {
	connID := chi.URLParam(r, "connId")
	c, err := h.store.GetConnection(connID)
	if err != nil {
		writeError(w, http.StatusNotFound, "connection not found")
		return
	}
	// Verify connection belongs to user's workspace scope
	if !h.validateConnectionAccess(r, connID) {
		writeError(w, http.StatusNotFound, "connection not found")
		return
	}
	c.Password = ""
	c.Extra = ""
	c.PasswordRef = maskRef(c.PasswordRef)
	c.ExtraRef = maskRef(c.ExtraRef)
	writeJSON(w, http.StatusOK, c)
}

func (h *ConnectionHandler) Create(w http.ResponseWriter, r *http.Request) {
	var c models.Connection
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
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

	c.ID = common.NewID()
	now := time.Now()
	c.CreatedAt = now
	c.UpdatedAt = now

	// Resolve workspace: if not set, use the user's actual workspace
	if c.WorkspaceID == "" || c.WorkspaceID == "default" {
		orgID := GetOrgIDFromRequest(r)
		if orgID != "" && UserWorkspaceResolverFunc != nil {
			if claims, ok := r.Context().Value("claims").(*jwt.MapClaims); ok {
				if sub, ok := (*claims)["sub"].(string); ok {
					if userWS := UserWorkspaceResolverFunc(sub); len(userWS) > 0 {
						c.WorkspaceID = userWS[0]
					}
				}
			}
		}
		if c.WorkspaceID == "" {
			c.WorkspaceID = GetWorkspaceID(r)
		}
	}

	// Credential handling: if a password_ref is provided (env://, vault://, k8s://),
	// store it directly — no encryption needed since we're storing a reference, not the value.
	// If a bare password is provided (no ref), encrypt it and store as encrypted:// ref.
	if c.PasswordRef != "" {
		c.Password = "" // ref takes precedence, don't store bare ciphertext
	} else if c.Password != "" {
		enc, err := h.crypto.Encrypt(c.Password)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "encryption failed")
			return
		}
		c.Password = enc
		c.PasswordRef = "encrypted://" + enc
	}
	if c.ExtraRef != "" {
		c.Extra = ""
	} else if c.Extra != "" {
		enc, err := h.crypto.Encrypt(c.Extra)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "encryption failed")
			return
		}
		c.Extra = enc
		c.ExtraRef = "encrypted://" + enc
	}

	if err := h.store.CreateConnection(&c); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") || strings.Contains(err.Error(), "unique") {
			writeError(w, http.StatusConflict, "conn_id already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Return sanitized — never expose secrets or refs in responses
	c.Password = ""
	c.Extra = ""
	c.PasswordRef = maskRef(c.PasswordRef)
	c.ExtraRef = maskRef(c.ExtraRef)
	AuditLog(r, "create", "connection", c.ConnID, nil, map[string]interface{}{"type": string(c.Type), "host": c.Host})
	writeJSON(w, http.StatusCreated, c)
}

func (h *ConnectionHandler) Update(w http.ResponseWriter, r *http.Request) {
	connID := chi.URLParam(r, "connId")
	if !h.validateConnectionAccess(r, connID) {
		writeError(w, http.StatusNotFound, "connection not found")
		return
	}
	existing, err := h.store.GetConnection(connID)
	if err != nil {
		writeError(w, http.StatusNotFound, "connection not found")
		return
	}

	var c models.Connection
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	// Preserve immutable fields
	c.ID = existing.ID
	c.ConnID = existing.ConnID
	c.CreatedAt = existing.CreatedAt
	c.UpdatedAt = time.Now()

	// A client that read this connection back got masked credentials. Echoing
	// them into an update means "unchanged", never "set the credential to the
	// mask".
	unmaskCredentials(&c)

	// A stored secret belongs to the server it was entered for. Nobody can
	// read it back through the API, but an editor could otherwise point
	// the connection at a server they control and have the next test or
	// run send it there. So a change of type, host or port does not carry
	// stored secrets over: they have to be entered again. Extra settings
	// are carried over only when they are a database's driver options
	// (sslmode and the like); for every other type they hold credentials:
	// an SFTP private key, HTTP auth headers, cloud keys.
	moved := c.Type != existing.Type || c.Port != existing.Port ||
		!strings.EqualFold(strings.TrimSpace(c.Host), strings.TrimSpace(existing.Host))
	if moved {
		if c.PasswordRef == "" && c.Password == "" && (existing.Password != "" || existing.PasswordRef != "") {
			writeError(w, http.StatusBadRequest, "this change points the connection at a different server (its type, host or port changed), so the stored password is not kept: enter the password again")
			return
		}
		extraIsDriverOptions := c.Type == existing.Type && existing.ExtraIsDriverOptions()
		if !extraIsDriverOptions && c.ExtraRef == "" && c.Extra == "" && (existing.Extra != "" || existing.ExtraRef != "") {
			writeError(w, http.StatusBadRequest, "this change points the connection at a different server (its type, host or port changed), so the stored extra settings are not kept: enter them again")
			return
		}
	}

	// Credential handling for updates:
	// If a new password_ref is provided, use it (replaces any existing ref).
	// If a bare password is provided, encrypt and store as encrypted:// ref.
	// If neither, keep existing refs.
	if c.PasswordRef != "" {
		c.Password = ""
	} else if c.Password != "" {
		enc, err := h.crypto.Encrypt(c.Password)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "encryption failed")
			return
		}
		c.Password = enc
		c.PasswordRef = "encrypted://" + enc
	} else {
		c.Password = existing.Password
		c.PasswordRef = existing.PasswordRef
	}

	if c.ExtraRef != "" {
		c.Extra = ""
	} else if c.Extra != "" {
		enc, err := h.crypto.Encrypt(c.Extra)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "encryption failed")
			return
		}
		c.Extra = enc
		c.ExtraRef = "encrypted://" + enc
	} else {
		c.Extra = existing.Extra
		c.ExtraRef = existing.ExtraRef
	}

	if err := h.store.UpdateConnection(&c); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	c.Password = ""
	c.Extra = ""
	c.PasswordRef = maskRef(c.PasswordRef)
	c.ExtraRef = maskRef(c.ExtraRef)
	AuditLog(r, "update", "connection", c.ConnID, nil, map[string]interface{}{"type": string(c.Type), "host": c.Host})
	writeJSON(w, http.StatusOK, c)
}

func (h *ConnectionHandler) Delete(w http.ResponseWriter, r *http.Request) {
	connID := chi.URLParam(r, "connId")
	if !h.validateConnectionAccess(r, connID) {
		writeError(w, http.StatusNotFound, "connection not found")
		return
	}
	if err := h.store.DeleteConnection(connID); err != nil {
		writeError(w, http.StatusNotFound, "connection not found")
		return
	}
	AuditLog(r, "delete", "connection", connID, nil, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (h *ConnectionHandler) Test(w http.ResponseWriter, r *http.Request) {
	connID := chi.URLParam(r, "connId")
	if !h.validateConnectionAccess(r, connID) {
		writeError(w, http.StatusNotFound, "connection not found")
		return
	}
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

	// Decrypt extras. The plaintext goes back onto the connection as well as
	// into the parsed map: the HTTP paths below take the map, but BuildURI
	// reads c.Extra for driver options, and an encrypted blob there parses as
	// nothing. That silently dropped sslmode, so testing a connection with
	// "sslmode": "require" against a server with TLS switched off reported
	// "Connected successfully" for a session that was in the clear -- the
	// exact false assurance the option exists to prevent.
	var extra map[string]interface{}
	if c.Extra != "" {
		dec, err := h.crypto.Decrypt(c.Extra)
		if err == nil {
			c.Extra = dec
			json.Unmarshal([]byte(dec), &extra)
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	switch c.Type {
	case models.ConnTypePostgres:
		result := testDBConnection(ctx, c.BuildURI())
		writeJSON(w, http.StatusOK, result)
	case models.ConnTypeRedshift:
		result := testDBConnection(ctx, c.BuildURI())
		writeJSON(w, http.StatusOK, result)
	case models.ConnTypeMySQL:
		result := testDBConnection(ctx, c.BuildURI())
		writeJSON(w, http.StatusOK, result)
	case models.ConnTypeSQLite:
		result := testDBConnection(ctx, c.Host)
		writeJSON(w, http.StatusOK, result)
	case models.ConnTypeClickHouse:
		result := testDBConnection(ctx, c.BuildURI())
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
	case models.ConnTypeMSSQL, models.ConnTypeSnowflake, models.ConnTypeOracle,
		models.ConnTypeBigQuery, models.ConnTypeDatabricks:
		result := unsupportedDatabaseTest(c.Type)
		writeJSON(w, http.StatusOK, result)
	default:
		// Generic: try HTTP GET if it looks like a URL, otherwise TCP
		result := testGeneric(ctx, c, extra)
		writeJSON(w, http.StatusOK, result)
	}
}

func testDBConnection(ctx context.Context, uri string) map[string]interface{} {
	driver, dsn, err := engine.DetectDriver(uri)
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		}
	}
	return testDBReal(ctx, driver, dsn)
}

func unsupportedDatabaseTest(kind models.ConnectionType) map[string]interface{} {
	return map[string]interface{}{
		"success": false,
		"error":   fmt.Sprintf("%s has no driver in this build", kind),
	}
}

// testDBReal actually opens a DB connection and pings it.
func testDBReal(ctx context.Context, driver, dsn string) map[string]interface{} {
	db, err := sql.Open(driver, dsn)
	if err != nil {
		log.Printf("Connection test failed (open %s): %v", driver, err)
		return map[string]interface{}{
			"success": false,
			"error":   "connection test failed — check server logs for details",
			"driver":  driver,
		}
	}
	defer db.Close()

	if err := db.PingContext(ctx); err != nil {
		log.Printf("Connection test failed (ping %s): %v", driver, err)
		return map[string]interface{}{
			"success": false,
			"error":   "connection test failed — check server logs for details",
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

	client := netguard.Outbound().Client(5 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, netguard.ErrBlockedTarget) {
			return map[string]interface{}{
				"success": false,
				"error":   "blocked: " + err.Error(),
			}
		}
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

// testSSH checks an sftp connection the way a run uses it (ADR-040): dial
// through the outbound policy, verify the host key, authenticate, open the
// SFTP subsystem, and confirm the base directory exists. It used to read
// the SSH banner and stop, so a wrong password tested green. An unknown
// host key fails with the key the server presented, returned as host_key,
// so configuring it is one copy and paste once verified out of band.
func testSSH(ctx context.Context, c *models.Connection) map[string]interface{} {
	cfg, err := engine.SFTPConfig(c)
	if err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}
	if deadline, ok := ctx.Deadline(); ok {
		cfg.Timeout = time.Until(deadline)
	}
	client, err := sftpclient.Dial(ctx, cfg)
	if err != nil {
		result := map[string]interface{}{"success": false, "error": err.Error()}
		var hk *sftpclient.HostKeyError
		if errors.As(err, &hk) {
			result["host_key"] = hk.Presented
		}
		return result
	}
	defer client.Close() //nolint:errcheck

	dir, err := client.CheckBaseDir()
	if err != nil {
		return map[string]interface{}{"success": false, "error": err.Error(), "host_key": client.HostKey}
	}
	checked := "host key verified"
	if cfg.InsecureSkipHostKeyCheck {
		checked = "host key NOT checked, because insecure_skip_host_key_check is set"
	}
	return map[string]interface{}{
		"success":  true,
		"message":  fmt.Sprintf("Signed in as %s over SFTP (%s); base directory %s exists", cfg.User, checked, dir),
		"host_key": client.HostKey,
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
	client := netguard.Outbound().Client(5 * time.Second)
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

			client := netguard.Outbound().Client(5 * time.Second)
			resp, err := client.Do(req)
			if err != nil {
				if errors.Is(err, netguard.ErrBlockedTarget) {
					return map[string]interface{}{
						"success": false,
						"error":   "blocked: " + err.Error(),
					}
				}
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
//
// description and icon are presentation metadata for the connection
// catalog: description is the one-line card copy, icon names an entry in
// the UI's icon set (ui/src/lib/icons.ts). Additive — clients that only
// know type/label/fields keep working; the UI falls back to a monogram
// tile when an icon name is missing from its set.
func ConnectionTypes(w http.ResponseWriter, r *http.Request) {
	types := []map[string]interface{}{
		// Databases
		{"type": "postgres", "label": "PostgreSQL", "category": "database", "icon": "connPostgres",
			"description": "Open-source relational database with strong standards compliance",
			"fields":      []string{"host", "port", "schema", "login", "password"}},
		{"type": "mysql", "label": "MySQL", "category": "database", "icon": "connMysql",
			"description": "The most widely used open-source relational database",
			"fields":      []string{"host", "port", "schema", "login", "password"}},
		{"type": "clickhouse", "label": "ClickHouse", "category": "database", "icon": "connGeneric",
			"description": "Column-oriented analytical database. Read, append and overwrite supported; upsert has no ClickHouse equivalent",
			"fields":      []string{"host", "port", "schema", "login", "password"},
			"hints":       map[string]string{"port": "9000 (native protocol)", "schema": "database name"}},
		{"type": "snowflake", "label": "Snowflake", "category": "database", "icon": "connSnowflake",
			"description": "Cloud data warehouse with separated storage and compute. No driver in this build: the connection test says so by name",
			"fields":      []string{"host", "port", "schema", "login", "password", "extra"},
			"hints":       map[string]string{"host": "account.snowflakecomputing.com", "schema": "database/schema", "extra": `{"warehouse": "COMPUTE_WH", "role": "SYSADMIN"}`}},
		{"type": "redshift", "label": "Amazon Redshift", "category": "database", "icon": "connRedshift",
			"description": "AWS's managed petabyte-scale data warehouse",
			"fields":      []string{"host", "port", "schema", "login", "password"},
			"hints":       map[string]string{"host": "cluster.region.redshift.amazonaws.com", "port": "5439"}},
		{"type": "bigquery", "label": "Google BigQuery", "category": "database", "icon": "connBigquery",
			"description": "Serverless data warehouse on Google Cloud. No driver in this build: the connection test says so by name",
			"fields":      []string{"schema", "extra"},
			"hints":       map[string]string{"schema": "project_id.dataset", "extra": "Service account JSON key"}},
		{"type": "databricks", "label": "Databricks", "category": "database", "icon": "connDatabricks",
			"description": "Lakehouse platform for analytics and machine learning. No driver in this build: the connection test says so by name",
			"fields":      []string{"host", "port", "schema", "login", "password"},
			"hints":       map[string]string{"host": "workspace.cloud.databricks.com", "login": "token", "password": "dapi..."}},
		{"type": "oracle", "label": "Oracle", "category": "database", "icon": "connOracle",
			"description": "Enterprise relational database for mission-critical workloads. No driver in this build: the connection test says so by name",
			"fields":      []string{"host", "port", "schema", "login", "password"}},
		{"type": "mssql", "label": "SQL Server", "category": "database", "icon": "connMssql",
			"description": "Microsoft's enterprise relational database with authenticated read and write support",
			"fields":      []string{"host", "port", "schema", "login", "password"}},
		{"type": "sqlite", "label": "SQLite", "category": "database", "icon": "connSqlite",
			"description": "Zero-configuration embedded database in a single file",
			"fields":      []string{"host"}},
		// Cloud Storage
		{"type": "s3", "label": "Amazon S3", "category": "storage", "icon": "connS3",
			"description": "AWS object storage — buckets of files at any scale",
			"fields":      []string{"extra"},
			"hints":       map[string]string{"extra": `{"bucket": "my-bucket", "region": "us-east-1", "access_key": "...", "secret_key": "..."}`}},
		{"type": "gcs", "label": "Google Cloud Storage", "category": "storage", "icon": "connGcs",
			"description": "Object storage on Google Cloud",
			"fields":      []string{"extra"},
			"hints":       map[string]string{"extra": `{"bucket": "my-bucket", "credentials": "service-account-json"}`}},
		{"type": "azure_blob", "label": "Azure Blob Storage", "category": "storage", "icon": "connAzureBlob",
			"description": "Object storage on Microsoft Azure",
			"fields":      []string{"extra"},
			"hints":       map[string]string{"extra": `{"container": "my-container", "account": "storageaccount", "key": "..."}`}},
		// APIs & Other
		{"type": "http", "label": "HTTP / REST API", "category": "api", "icon": "connHttp",
			"description": "Any HTTP endpoint — REST APIs, webhooks, exports",
			"fields":      []string{"host", "port", "login", "password", "extra"}},
		{"type": "sftp", "label": "SFTP / SSH", "category": "api", "icon": "connSftp",
			"description": "File delivery and pickup over SSH: source_file and sink_file read and write through it",
			"fields":      []string{"host", "port", "schema", "login", "password", "extra"},
			"hints": map[string]string{
				"schema": "/upload (empty: the login directory)",
				"extra":  `{"host_key": "SHA256:...", "private_key": "-----BEGIN OPENSSH PRIVATE KEY-----...", "passphrase": "..."}`,
			}},
		{"type": "generic", "label": "Generic", "category": "other", "icon": "connGeneric",
			"description": "Any other system — bring your own settings",
			"fields":      []string{"host", "port", "login", "password", "extra"}},
	}
	writeJSON(w, http.StatusOK, types)
}

// maskRef returns the credential ref with the sensitive portion masked.
// "env://MY_VAR" stays as-is (env var name is not a secret).
// "encrypted://..." becomes "encrypted://********".
// "vault://secret/data/prod#password" stays as-is (path is not a secret).
// "k8s://ns/secret/key" stays as-is (reference path is not a secret).
// maskedRef is what Get and List return in place of an encrypted credential
// ref, and maskedSecret is the same for a bare password. Both are sentinels,
// not values: a client doing read-modify-write against the REST API sends
// them straight back, so the update path has to read them as "unchanged".
// Storing one as though it were the credential destroys the real one, and
// nothing surfaces until the next run tries to use the connection.
const (
	maskedRef    = "encrypted://********"
	maskedSecret = "********"
)

func maskRef(ref string) string {
	if ref == "" {
		return ""
	}
	if strings.HasPrefix(ref, "encrypted://") {
		return maskedRef
	}
	return ref
}

// unmaskCredentials normalises the sentinels a client may echo back into the
// empty values that mean "keep what is stored".
func unmaskCredentials(c *models.Connection) {
	if c.PasswordRef == maskedRef {
		c.PasswordRef = ""
	}
	if c.ExtraRef == maskedRef {
		c.ExtraRef = ""
	}
	if c.Password == maskedSecret {
		c.Password = ""
	}
	if c.Extra == maskedSecret {
		c.Extra = ""
	}
}
