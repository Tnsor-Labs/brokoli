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
	"sort"
	"strings"
	"time"

	_ "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/Tnsor-Labs/brokoli/crypto"
	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/identity"
	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
	"github.com/Tnsor-Labs/brokoli/pkg/secrets"
	"github.com/Tnsor-Labs/brokoli/pkg/secretstore"
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
	// creds resolves a connection's credential references for the test,
	// with the same chain a run resolves them with (#752).
	creds *engine.ConnectionResolver
	// tokens is the deployment's OIDC token source, for connections that
	// authenticate by workload identity federation. Nil when there is none.
	tokens identity.TokenSource
	// secretStores resolves and checks secret:// references (ADR-041).
	// Nil when the server has no secret stores.
	secretStores *engine.SecretStoreResolver
}

// useSecretStores makes the connection test resolve secret:// references
// through res, with the chain a run uses, and lets saving check them.
func (h *ConnectionHandler) useSecretStores(res *engine.SecretStoreResolver) {
	chain := secrets.NewDefaultChain(h.crypto)
	chain.Register(res)
	h.creds = engine.NewConnectionResolver(h.store, chain)
	h.secretStores = res
}

// secretRefErrors checks a connection's secret:// references against its
// workspace when it is saved: the store exists there, and a #field suits
// the provider's shape. Nothing is fetched (ADR-041 section 8). It covers
// password_ref, extra_ref, and any string inside the submitted extra
// document.
func (h *ConnectionHandler) secretRefErrors(c *models.Connection, workspaceID string) string {
	var refs []struct{ field, ref string }
	add := func(field, ref string) {
		if secretstore.IsRef(ref) {
			refs = append(refs, struct{ field, ref string }{field, ref})
		}
	}
	add("password_ref", c.PasswordRef)
	add("extra_ref", c.ExtraRef)
	if strings.Contains(c.Extra, secretstore.Scheme+"://") {
		var doc map[string]interface{}
		if json.Unmarshal([]byte(c.Extra), &doc) == nil {
			var walk func(m map[string]interface{}, prefix string)
			walk = func(m map[string]interface{}, prefix string) {
				for k, v := range m {
					switch t := v.(type) {
					case string:
						add("extra."+prefix+k, t)
					case map[string]interface{}:
						walk(t, prefix+k+".")
					}
				}
			}
			walk(doc, "")
		}
	}
	if len(refs) == 0 {
		return ""
	}
	ss, ok := h.store.(store.SecretStoreStore)
	if !ok || h.secretStores == nil {
		return refs[0].field + ": secret:// references need secret stores, which this server does not have"
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].field < refs[j].field })
	for _, f := range refs {
		ref, err := secretstore.ParseRef(f.ref)
		if err != nil {
			return f.field + ": " + err.Error()
		}
		st, err := ss.GetSecretStoreByName(workspaceID, ref.Store)
		if err != nil {
			return fmt.Sprintf("%s: %s: no secret store named %q in this workspace", f.field, f.ref, ref.Store)
		}
		p, ok := h.secretStores.Providers().Get(st.Provider)
		if !ok {
			return fmt.Sprintf("%s: secret store %q uses provider %q, which this server does not have", f.field, st.Name, st.Provider)
		}
		if err := secretstore.CheckShape(ref, p.Shape()); err != nil {
			return f.field + ": " + err.Error()
		}
	}
	return ""
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
	// The chain is built the way serve builds the one runs use
	// (secrets.NewDefaultChain over the same key), so a reference the test
	// resolves is one a run on this server resolves, allowlists included.
	return &ConnectionHandler{store: s, crypto: c, creds: engine.NewConnectionResolver(s, secrets.NewDefaultChain(c))}
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

	if msg := refErrors(&c, nil); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	if msg := h.secretRefErrors(&c, c.WorkspaceID); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
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
	if msg := refErrors(&c, existing); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	if msg := h.secretRefErrors(&c, existing.WorkspaceID); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}

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

	// Resolve the credentials the way a run does (#752). This used to
	// decrypt Password and Extra directly, so a connection whose
	// credentials are references (env://, vault://, k8s://) was tested with
	// none, and failed the test while runs using it worked -- or passed it
	// against a server that accepts no password. A reference that cannot be
	// resolved fails the test with the message the run would fail with.
	//
	// The plaintext extra goes back onto the connection as well as into
	// the parsed map: the HTTP paths below take the map, but BuildURI reads
	// c.Extra for driver options. An encrypted blob there parses as
	// nothing, which once silently dropped sslmode, so a connection set to
	// "sslmode": "require" tested green against a server with TLS off.
	if err := h.creds.ResolveCredentials(c); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	var extra map[string]interface{}
	if c.Extra != "" {
		json.Unmarshal([]byte(c.Extra), &extra)
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	writeJSON(w, http.StatusOK, withResolvedHereNote(h.testResolved(ctx, c, extra), c))
}

// resolvedHereNote is added to a test of a connection whose credentials
// come from the server's environment, Vault or Kubernetes: the test ran on
// this server, and a run resolves references on whichever machine runs the
// node, with that machine's environment and allowlists.
const resolvedHereNote = "Credential references were resolved on this server. " +
	"A run resolves them on the machine that runs the node, which needs the same variables, allowlists and access."

func withResolvedHereNote(result map[string]interface{}, c *models.Connection) map[string]interface{} {
	for _, ref := range []string{c.PasswordRef, c.ExtraRef} {
		if ref != "" && !strings.HasPrefix(ref, "encrypted://") {
			result["note"] = resolvedHereNote
			break
		}
	}
	return result
}

// testResolved tests a connection whose credentials are already resolved.
func (h *ConnectionHandler) testResolved(ctx context.Context, c *models.Connection, extra map[string]interface{}) map[string]interface{} {
	switch c.Type {
	case models.ConnTypePostgres:
		return testDBConnection(ctx, c.BuildURI())
	case models.ConnTypeRedshift:
		return testDBConnection(ctx, c.BuildURI())
	case models.ConnTypeMySQL:
		return testDBConnection(ctx, c.BuildURI())
	case models.ConnTypeSQLite:
		return testDBConnection(ctx, c.Host)
	case models.ConnTypeClickHouse:
		return testDBConnection(ctx, c.BuildURI())
	case models.ConnTypeMSSQL:
		return testDBConnection(ctx, c.BuildURI())
	case models.ConnTypeSnowflake:
		return testDBConnection(ctx, c.BuildURI())
	case models.ConnTypeOracle:
		return testDBConnection(ctx, c.BuildURI())
	case models.ConnTypeDatabricks:
		return testDBConnection(ctx, c.BuildURI())
	case models.ConnTypeHTTP:
		return testHTTPAuth(ctx, c, extra)
	case models.ConnTypeSFTP:
		return testSSH(ctx, c)
	case models.ConnTypeS3:
		return testS3(ctx, extra)
	case models.ConnTypeAzureBlob:
		return testAzureBlob(ctx, extra)
	case models.ConnTypeGCS:
		return h.testGCS(ctx, c)
	case models.ConnTypeBigQuery:
		return h.testBigQuery(ctx, c)
	default:
		// Generic: try HTTP GET if it looks like a URL, otherwise TCP
		return testGeneric(ctx, c, extra)
	}
}

func (h *ConnectionHandler) testBigQuery(ctx context.Context, c *models.Connection) map[string]interface{} {
	// c.Extra was resolved by the test handler through the same path a run
	// uses; it is passed to the backend directly, never through a config.
	// An oidc connection is tested with the deployment's token source, for
	// the connection's own workspace and ID, as a run would be.
	req := identity.TokenRequest{WorkspaceID: c.WorkspaceID, SubjectKind: "connection", SubjectID: c.ID}
	if err := engine.CheckBigQueryConnection(ctx, c.BuildURI(), nil, c.Extra, h.tokens, req); err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}
	return map[string]interface{}{"success": true}
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

// testDBReal actually opens a DB connection and pings it.
func testDBReal(ctx context.Context, driver, dsn string) map[string]interface{} {
	db, err := sql.Open(driver, dsn)
	if err != nil {
		log.Printf("Connection test failed (open %s): %v", driver, engine.RedactDSNError(err, dsn))
		return map[string]interface{}{
			"success": false,
			"error":   "connection test failed — check server logs for details",
			"driver":  driver,
		}
	}
	defer db.Close()

	if err := db.PingContext(ctx); err != nil {
		log.Printf("Connection test failed (ping %s): %v", driver, engine.RedactDSNError(err, dsn))
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

// testS3 validates S3 credentials against the configured bucket.
func testS3(ctx context.Context, extra map[string]interface{}) map[string]interface{} {
	if extra == nil {
		return map[string]interface{}{"success": false, "error": "No extra config — set bucket and region"}
	}
	err := engine.TestS3Connection(ctx, extra)
	if err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}
	return map[string]interface{}{"success": true, "message": "Authenticated successfully against the S3 bucket"}
}

// testAzureBlob authenticates against the configured container and lists
// it, through the same client file nodes use at runtime, so a wrong key
// or a missing container fails here rather than in a pipeline (#680).
func testAzureBlob(ctx context.Context, extra map[string]interface{}) map[string]interface{} {
	if extra == nil {
		return map[string]interface{}{"success": false, "error": "No extra config — set account, container, and key or sas_token"}
	}
	if err := engine.TestAzureBlobConnection(ctx, extra); err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}
	return map[string]interface{}{"success": true, "message": "Authenticated and listed the Azure Blob container"}
}

// testGCS authenticates and lists one object in the configured bucket,
// through the same client file nodes use. An oidc connection gets the
// deployment's token source, for the connection's own workspace and ID.
func (h *ConnectionHandler) testGCS(ctx context.Context, c *models.Connection) map[string]interface{} {
	req := identity.TokenRequest{WorkspaceID: c.WorkspaceID, SubjectKind: "connection", SubjectID: c.ID}
	if err := engine.TestGCSConnection(ctx, c.Extra, h.tokens, req); err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}
	return map[string]interface{}{"success": true, "message": "Authenticated and listed the GCS bucket"}
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
			"description": "Cloud data warehouse with separated storage and compute. Query only: source_db reads; sink_db and migrate refuse to write to it",
			"fields":      []string{"host", "port", "schema", "login", "password", "extra"},
			"hints":       map[string]string{"host": "account.snowflakecomputing.com", "schema": "database/schema", "extra": `{"warehouse": "COMPUTE_WH", "role": "ANALYST"} — authenticator, if set, must be "snowflake"`}},
		{"type": "redshift", "label": "Amazon Redshift", "category": "database", "icon": "connRedshift",
			"description": "AWS's managed petabyte-scale data warehouse",
			"fields":      []string{"host", "port", "schema", "login", "password"},
			"hints":       map[string]string{"host": "cluster.region.redshift.amazonaws.com", "port": "5439"}},
		{"type": "bigquery", "label": "Google BigQuery", "category": "database", "icon": "connBigquery",
			"description": "Serverless data warehouse on Google Cloud. Reads and atomic load-job writes supported",
			"fields":      []string{"schema", "extra"},
			"hints":       map[string]string{"schema": "project_id.dataset", "extra": `Service account JSON, location and optional billing project`}},
		{"type": "databricks", "label": "Databricks", "category": "database", "icon": "connDatabricks",
			"description": "Lakehouse SQL warehouse. Read only: source_db queries, batch and streamed; writes are refused by name",
			"fields":      []string{"host", "port", "schema", "password", "extra"},
			"hints":       map[string]string{"host": "workspace.cloud.databricks.com (hostname only)", "password": "Personal access token (dapi...)", "schema": "Warehouse HTTP path: /sql/1.0/warehouses/<id>"}},
		{"type": "oracle", "label": "Oracle", "category": "database", "icon": "connOracle",
			"description": "Enterprise relational database for mission-critical workloads. Read-only in this build: database nodes can query Oracle, but not write to it",
			"fields":      []string{"host", "port", "schema", "login", "password", "extra"},
			"hints":       map[string]string{"port": "1521", "schema": "service name", "extra": `{"ssl": true, "ssl verify": true}`}},
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
			"description": "Google Cloud object storage — file nodes read and write objects in a bucket",
			"fields":      []string{"extra"},
			"hints":       map[string]string{"extra": `{"bucket": "my-bucket", "credentials": "<service-account JSON key>"} — or "auth_method": "oidc" with "provider" (and optional "service_account") instead of a key; with neither, the worker's own identity where allowed`}},
		{"type": "azure_blob", "label": "Azure Blob Storage", "category": "storage", "icon": "connAzureBlob",
			"description": "Object storage on Microsoft Azure — file nodes read and write blobs in a container",
			"fields":      []string{"extra"},
			"hints":       map[string]string{"extra": `{"container": "my-container", "account": "storageaccount", "key": "..."} — or "sas_token" instead of "key"; optional "endpoint" for a sovereign cloud, private endpoint or Azurite`}},
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

// refErrors checks the credential references a create or update would
// store, without resolving them (#781). A reference identical to the one
// already stored is not checked again: the form echoes stored references on
// every save, and renaming a connection must not fail because the rules
// changed after its reference was saved.
func refErrors(c, existing *models.Connection) string {
	var storedPassword, storedExtra string
	if existing != nil {
		storedPassword, storedExtra = existing.PasswordRef, existing.ExtraRef
	}
	for _, f := range []struct{ field, ref, stored string }{
		{"password_ref", c.PasswordRef, storedPassword},
		{"extra_ref", c.ExtraRef, storedExtra},
	} {
		if f.ref == "" || (existing != nil && f.ref == f.stored) {
			continue
		}
		if err := secrets.ValidateRef(f.ref); err != nil {
			return f.field + ": " + err.Error()
		}
	}
	return ""
}

// maskRef returns a credential reference as the API may show it (#755).
//
// "encrypted://<ciphertext>" becomes "encrypted://********": the body is
// the credential itself, encrypted under the server's key, and nobody needs
// to see it to edit the connection.
//
// Every other reference -- "env://NAME", "vault://path#key",
// "k8s://ns/secret/key" -- is returned as stored. It is the location of a
// credential, not the credential, and it is what a user types: the form
// shows it so the connection can be edited without retyping it, and a
// reference to a workspace's own secret store (ADR-041) has to be visible
// to be usable. Reading it requires access to the connection; resolving it
// requires the operator's allowlist on the machine that runs the node.
//
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
