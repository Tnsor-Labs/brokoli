package engine

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"reflect"
	"strings"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/drivers"
	"github.com/Tnsor-Labs/brokoli/pkg/identity"
	"github.com/Tnsor-Labs/brokoli/pkg/secrets"
	"github.com/Tnsor-Labs/brokoli/pkg/secretstore"
	"github.com/Tnsor-Labs/brokoli/store"
)

// ConnectionResolver resolves conn_id in node configs to actual connection URIs and headers.
// Credentials are resolved via the secrets.Chain at execution time — the resolver never
// caches plaintext passwords beyond the scope of a single Resolve call.
type ConnectionResolver struct {
	store   store.Store
	secrets *secrets.Chain
	// pools carries the per-connection concurrency budgets (#398); it
	// lives here because the resolver is the one engine-wide object every
	// Runner already holds, and pool membership is decided by the same
	// conn_id the resolver resolves.
	pools *connectionPools
	// tokens issues OIDC tokens for connections that authenticate by
	// workload identity federation. Nil when the deployment has none.
	tokens identity.TokenSource
	// driverManager verifies pinned native driver identities. It never loads a
	// driver; execution remains restricted to the isolated native worker.
	driverManager *drivers.Manager
}

// ErrNativeWorkerUnavailable reports that a Flight SQL connection passed
// identity validation but this process cannot execute native drivers.
var ErrNativeWorkerUnavailable = errors.New("isolated native worker execution is not enabled")

// ErrFlightSQLConnectionTestUnsupported reports that this server cannot test a
// Flight SQL connection in place. The driver only loads inside the isolated
// native child, so the honest answer is that a run is the test; reporting a
// driver's connection failure from a process that never loaded the driver
// would be fiction.
var ErrFlightSQLConnectionTestUnsupported = errors.New("a Flight SQL connection is verified by running a pipeline: its driver loads only inside the isolated native worker")

// SetTokenSource sets where connections authenticating by OIDC get their
// tokens. A nil pointer inside a non-nil interface is treated as none, so a
// caller can pass a constructor's nil result through unchanged.
func (cr *ConnectionResolver) SetTokenSource(src identity.TokenSource) {
	if src == nil || reflect.ValueOf(src).Kind() == reflect.Ptr && reflect.ValueOf(src).IsNil() {
		cr.tokens = nil
		return
	}
	cr.tokens = src
}

// TokenSource returns the deployment's OIDC token source, or nil. A backend
// gets its token through identity.Token with this source, which refuses a
// machine's identity where ambient identity is denied.
func (cr *ConnectionResolver) TokenSource() identity.TokenSource {
	if cr == nil {
		return nil
	}
	return cr.tokens
}

// NewConnectionResolver creates a new resolver.
func NewConnectionResolver(s store.Store, sec *secrets.Chain) *ConnectionResolver {
	return &ConnectionResolver{store: s, secrets: sec, pools: newConnectionPools()}
}

// SetDriverManager sets the verified native-driver inventory used for native
// ADBC preflight. It is primarily useful to wire an isolated worker inventory.
func (cr *ConnectionResolver) SetDriverManager(manager *drivers.Manager) { cr.driverManager = manager }

// ValidateConnection checks gates that must run before any credential is read.
func (cr *ConnectionResolver) ValidateConnection(conn *models.Connection) error {
	if conn == nil || !nativeADBCConnection(conn) {
		return nil
	}
	manager := cr.driverManager
	if manager == nil {
		var err error
		manager, err = drivers.NewManager(drivers.DefaultDir())
		if err != nil {
			return fmt.Errorf("native ADBC driver inventory: %w", err)
		}
	}
	if conn.DriverIdentity == nil {
		// RequiredCapabilities owns canonical identity validation, including
		// the missing identity case; never broaden this into a name-only match.
		_, err := manager.RequiredCapabilities(drivers.DriverIdentity{})
		return fmt.Errorf("native ADBC driver identity: %w", err)
	}
	if _, err := manager.RequiredCapabilities(*conn.DriverIdentity); err != nil {
		return fmt.Errorf("native ADBC driver identity: %w", err)
	}
	return nil
}

// nativeADBCConnectionType is intentionally a short allowlist. Existing
// database/sql connection types do not become native-driver capable merely by
// carrying a DriverIdentity.
func nativeADBCConnectionType(kind models.ConnectionType) bool {
	return kind == models.ConnTypeFlightSQL || kind == models.ConnTypePostgres || kind == models.ConnTypeSQLite
}

// Flight SQL has no legacy database/sql execution path, so it always requires
// a pin. PostgreSQL and SQLite remain legacy unless an operator explicitly
// pins them to a native ADBC artifact.
func nativeADBCConnection(conn *models.Connection) bool {
	return conn != nil && nativeADBCConnectionType(conn.Type) && (conn.Type == models.ConnTypeFlightSQL || conn.DriverIdentity != nil)
}

// PipelineRequiredCapabilities returns the exact native-driver tags required
// by saved native ADBC sources. This happens before a queued run is published,
// so a scheduler can place it only on a worker with the pinned driver.
func (cr *ConnectionResolver) PipelineRequiredCapabilities(pipe *models.Pipeline) ([]string, error) {
	if cr == nil || pipe == nil {
		return nil, nil
	}
	seen := make(map[string]struct{})
	var capabilities []string
	for _, node := range pipe.Nodes {
		if node.Type != models.NodeTypeSourceDB {
			continue
		}
		connID, _ := node.Config["conn_id"].(string)
		if connID == "" {
			continue
		}
		conn, err := cr.store.GetConnection(connID)
		if err != nil {
			return nil, fmt.Errorf("native ADBC connection %q: %w", connID, err)
		}
		if !nativeADBCConnection(conn) {
			continue
		}
		if err := cr.ValidateConnection(conn); err != nil {
			return nil, err
		}
		for _, capability := range mustDriverCapabilities(cr, *conn.DriverIdentity) {
			if _, ok := seen[capability]; !ok {
				seen[capability] = struct{}{}
				capabilities = append(capabilities, capability)
			}
		}
	}
	return capabilities, nil
}

func mustDriverCapabilities(cr *ConnectionResolver, identity drivers.DriverIdentity) []string {
	capabilities, err := managerFor(cr).RequiredCapabilities(identity)
	if err != nil {
		// ValidateConnection above performs this exact check; this preserves its
		// error handling if manager construction ever changes.
		return nil
	}
	return capabilities
}

// ResolveWithWarnings is Resolve, plus the warnings it would otherwise only
// write to the process log.
//
// A connection that resolves to nothing usable is a pipeline-authoring
// problem, and the author reads the run's node log, not the server's stdout
// -- so the message that explains the failure has to reach them there.
// Without this, a run against an Oracle connection (advertised in the
// catalog, no driver compiled in) failed with an error naming neither
// Oracle nor the connection, while the sentence that would have explained
// it went to a log the author cannot see.
func (cr *ConnectionResolver) ResolveWithWarnings(config map[string]interface{}, nodeType models.NodeType) (map[string]interface{}, []string, error) {
	return cr.ResolveWithWarningsIn(config, nodeType, "")
}

// ResolveWithWarningsIn is ResolveWithWarnings for a pipeline in
// workspaceID: a conn_id naming another workspace's connection is not
// resolved. conn_id is unique across the whole store, so without this a
// pipeline could name any workspace's connection and run with its
// credentials. Runs resolve through the *In methods; the unscoped ones
// remain for callers with no pipeline, which decide access themselves.
//
// The error is a connection whose credentials could not be resolved. The
// node must not run: it would run with an empty or unresolved credential
// and fail, if at all, with the target's authentication error instead of
// the reason (#751).
func (cr *ConnectionResolver) ResolveWithWarningsIn(config map[string]interface{}, nodeType models.NodeType, workspaceID string) (map[string]interface{}, []string, error) {
	return cr.ResolveWithWarningsScoped(config, nodeType, secrets.Scope{WorkspaceID: workspaceID})
}

// ResolveWithWarningsScoped is ResolveWithWarningsIn for one node of one
// run: the scope's workspace bounds which connections it may use, and
// the run and node say whose work a reference is resolved for (ADR-041
// section 5). Credentials resolved for a run are redacted from what the
// run records.
func (cr *ConnectionResolver) ResolveWithWarningsScoped(config map[string]interface{}, nodeType models.NodeType, scope secrets.Scope) (map[string]interface{}, []string, error) {
	var warnings []string
	resolved, err := cr.resolve(config, nodeType, scope, func(format string, args ...interface{}) {
		warnings = append(warnings, fmt.Sprintf(format, args...))
	})
	return resolved, warnings, err
}

// ResolveIn is Resolve for a pipeline in workspaceID; see
// ResolveWithWarningsIn.
func (cr *ConnectionResolver) ResolveIn(config map[string]interface{}, nodeType models.NodeType, workspaceID string) (map[string]interface{}, error) {
	return cr.resolve(config, nodeType, secrets.Scope{WorkspaceID: workspaceID}, nil)
}

// ResolveScoped is ResolveIn for one node of one run; see
// ResolveWithWarningsScoped.
func (cr *ConnectionResolver) ResolveScoped(config map[string]interface{}, nodeType models.NodeType, scope secrets.Scope) (map[string]interface{}, error) {
	return cr.resolve(config, nodeType, scope, nil)
}

// sameWorkspace reports whether conn may serve a pipeline in workspaceID.
// An unknown workspace on either side is not a refusal: a runner with no
// pipeline, or a store that does not carry workspaces, has nothing to
// compare, and refusing would break it rather than protect anything.
func sameWorkspace(conn *models.Connection, workspaceID string) bool {
	return workspaceID == "" || conn.WorkspaceID == "" || conn.WorkspaceID == workspaceID
}

// notInWorkspace is worded exactly like a missing connection on purpose:
// "belongs to another workspace" would tell a pipeline author which slugs
// exist elsewhere.
const notInWorkspace = "conn_id %q not found in this pipeline's workspace"

// Resolve checks if the config has a conn_id and replaces connection fields with resolved values.
// Returns the config unchanged if no conn_id is present (backward compatible).
//
// Callers that can reach the run's log should prefer ResolveWithWarnings.
func (cr *ConnectionResolver) Resolve(config map[string]interface{}, nodeType models.NodeType) (map[string]interface{}, error) {
	return cr.resolve(config, nodeType, secrets.Scope{}, nil)
}

func (cr *ConnectionResolver) resolve(config map[string]interface{}, nodeType models.NodeType, scope secrets.Scope, warn func(string, ...interface{})) (map[string]interface{}, error) {
	workspaceID := scope.WorkspaceID
	connID, ok := config["conn_id"].(string)
	if !ok || connID == "" {
		return config, nil
	}

	conn, err := cr.store.GetConnection(connID)
	if err != nil {
		// "this store cannot look connections up" and "there is no such
		// connection" are different facts and were wearing the same
		// sentence. A worker on an HTTP-backed store refuses
		// GetConnection outright, and the operator was told
		// `conn_id "x" not found` -- a statement about their data
		// describing a property of their deployment, while the node went
		// on to run with its credentials unresolved and failed somewhere
		// less obvious.
		//
		// Returning the config unchanged stays right for a genuinely
		// missing connection, because a node may carry inline fields as a
		// fallback and that is the backward-compatible behaviour. It is
		// wrong for a store that can never resolve one.
		msg, args := "conn_id %q not found: %v", []interface{}{connID, err}
		if errors.Is(err, store.ErrUnsupported) {
			msg = "conn_id %q cannot be resolved here: this worker has no access to stored connections, " +
				"so the node would run without its credentials. Give the node inline connection fields, " +
				"or run it on a worker that can reach the control plane's connection store (%v)"
		}
		log.Printf("[conn-resolver] WARNING: "+msg, args...)
		if warn != nil {
			warn(msg, args...)
		}
		return config, nil
	}
	if !sameWorkspace(conn, workspaceID) {
		log.Printf("[conn-resolver] WARNING: "+notInWorkspace, connID)
		if warn != nil {
			warn(notInWorkspace, connID)
		}
		return config, nil
	}
	if err := cr.ValidateConnection(conn); err != nil {
		return config, err
	}

	if err := cr.resolveCredentials(conn, scope); err != nil {
		return config, err
	}

	// Parse decrypted extra into a map
	var extra map[string]interface{}
	if conn.Extra != "" {
		if err := json.Unmarshal([]byte(conn.Extra), &extra); err != nil {
			return config, fmt.Errorf("parse connection extra: %w", err)
		}
	}

	// Inject connection fields based on node type
	resolved := make(map[string]interface{}, len(config))
	for k, v := range config {
		resolved[k] = v
	}

	switch nodeType {
	case models.NodeTypeSourceDB, models.NodeTypeSinkDB:
		if nodeType == models.NodeTypeSourceDB && nativeADBCConnection(conn) {
			// These markers are only injected for a saved, identity-validated
			// connection. The runner keeps native ADBC out of database/sql.
			manifest := managerFor(cr).GetIdentity(*conn.DriverIdentity)
			resolved["uri"] = conn.BuildURI()
			resolved["native_adbc_library"] = manifest.LibraryPath()
			resolved["native_adbc_entrypoint"] = manifest.Entrypoint
			resolved["native_adbc_options"] = nativeADBCOptions(conn, extra)
			break
		}
		// A connection type with no engine driver has no URI to inject. Leaving
		// the node's own uri untouched makes the failure say so; fabricating one
		// from the bare hostname used to hand the Postgres driver a malformed
		// DSN, losing the port, database, and credentials on the way.
		if !conn.IsDatabase() {
			msg := "conn_id %q is type %q, which has no database driver in this build; the node's own uri is used unchanged, and the run will fail against it if there is none"
			args := []interface{}{connID, conn.Type}
			log.Printf("[conn-resolver] WARNING: "+msg, args...)
			if warn != nil {
				warn(msg, args...)
			}
			break
		}
		// A BigQuery URI names only the project, dataset and location. Its
		// key stays out of the node config: the backend resolves it by the
		// conn_id already there, where the node runs (ADR-042 section 1).
		resolved["uri"] = conn.BuildURI()

	case models.NodeTypeSourceAPI, models.NodeTypeSinkAPI:
		resolveAPIConnectionFields(config, resolved, conn, extra)
	}

	return resolved, nil
}

func managerFor(cr *ConnectionResolver) *drivers.Manager {
	if cr.driverManager != nil {
		return cr.driverManager
	}
	manager, _ := drivers.NewManager(drivers.DefaultDir())
	return manager
}

func flightSQLHeaders(conn *models.Connection, extra map[string]interface{}) map[string]string {
	headers := make(map[string]string)
	if raw, ok := extra["headers"].(map[string]interface{}); ok {
		for name, value := range raw {
			if text, ok := value.(string); ok {
				headers[name] = text
			}
		}
	}
	if conn.Password != "" {
		if conn.Login == "" {
			headers["authorization"] = "Bearer " + conn.Password
		} else {
			headers["authorization"] = "Basic " + base64.StdEncoding.EncodeToString([]byte(conn.Login+":"+conn.Password))
		}
	}
	return headers
}

// nativeADBCOptions carries only string driver options. The worker enforces
// bounds again at its process boundary before passing them to drivermgr.
func nativeADBCOptions(conn *models.Connection, extra map[string]interface{}) map[string]string {
	options := make(map[string]string)
	if raw, ok := extra["adbc_options"].(map[string]interface{}); ok {
		for name, value := range raw {
			if text, ok := value.(string); ok {
				options[name] = text
			}
		}
	}
	if conn.Type == models.ConnTypeFlightSQL {
		for name, value := range flightSQLHeaders(conn, extra) {
			options["adbc.flight.sql.rpc.call_header."+name] = value
		}
	}
	return options
}

// resolveAPIConnectionFields injects a connection's base URL, merged headers,
// and Basic Auth credentials into an HTTP node's resolved config. Shared by
// source_api and sink_api -- both are plain HTTP requests against the same
// kind of connection, so they resolve identically (previously sink_api had
// no case here at all: a conn_id on a sink_api node silently injected
// nothing, unlike every other node type that accepts one).
func resolveAPIConnectionFields(
	config map[string]interface{},
	resolved map[string]interface{},
	conn *models.Connection,
	extra map[string]interface{},
) {
	baseURL := conn.BuildURI()
	if path, ok := config["url"].(string); ok && path != "" && path[0] == '/' {
		resolved["url"] = baseURL + path
	} else if _, ok := config["url"].(string); !ok || config["url"] == "" {
		resolved["url"] = baseURL
	}
	if extra != nil {
		if connHeaders, ok := extra["headers"].(map[string]interface{}); ok {
			merged := make(map[string]interface{})
			for k, v := range connHeaders {
				merged[k] = v
			}
			if nodeHeaders, ok := config["headers"].(map[string]interface{}); ok {
				for k, v := range nodeHeaders {
					merged[k] = v
				}
			}
			resolved["headers"] = merged
		}
	}
	if conn.Login != "" {
		resolved["auth_user"] = conn.Login
		resolved["auth_password"] = conn.Password
	}
}

// resolveCredentials resolves password_ref and extra_ref using the secrets chain,
// populating the plaintext Password and Extra fields on the connection.
//
// A reference that cannot be resolved is an error (#751). It used to be
// logged to the server and skipped, so the node ran with whatever the
// field held before: an empty password for env://, vault:// and k8s://,
// and the ciphertext itself for an encrypted:// value that failed to
// decrypt. The author then saw the target's authentication error, with
// nothing in the run log to say the reference was the cause.
//
// With no secrets chain there is nothing to resolve, and the connection is
// used as it is. That is the contract for a connection that arrives
// already resolved, as it does on a worker that receives credentials from
// its control plane rather than reading them itself.
//
// A value with no reference is the legacy shape: plaintext from before
// references existed, or an encrypted blob. It is offered to the chain's
// fallback, and when that cannot decrypt it, it is kept, because failing
// to decrypt is how a legacy plaintext value is recognised. Connections
// read from the store always carry a reference for an encrypted value
// (the startup backfill in both stores), so this path does not hide a
// failed decryption of a stored credential.
//
// Each value resolved for a run (scope.RunID) joins that run's redaction
// set (run_redaction.go): the password, and the credential-like values of
// the extra document.
func (cr *ConnectionResolver) resolveCredentials(conn *models.Connection, scope secrets.Scope) error {
	if cr.secrets == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if conn.PasswordRef != "" {
		plain, err := cr.secrets.ResolveIn(ctx, scope, conn.PasswordRef)
		if err != nil {
			return credentialError(conn.ConnID, "password", conn.PasswordRef, err)
		}
		conn.Password = plain
		addRunSecret(scope.RunID, plain)
	} else if conn.Password != "" {
		if plain, err := cr.secrets.ResolveIn(ctx, scope, conn.Password); err == nil {
			conn.Password = plain
			addRunSecret(scope.RunID, plain)
		}
	}

	if conn.ExtraRef != "" {
		plain, err := cr.secrets.ResolveIn(ctx, scope, conn.ExtraRef)
		if err != nil {
			return credentialError(conn.ConnID, "extra settings", conn.ExtraRef, err)
		}
		conn.Extra = plain
		addRunExtraSecrets(scope.RunID, plain)
	} else if conn.Extra != "" {
		if plain, err := cr.secrets.ResolveIn(ctx, scope, conn.Extra); err == nil {
			conn.Extra = plain
			addRunExtraSecrets(scope.RunID, plain)
		}
	}
	return cr.resolveExtraFieldRefs(ctx, conn, scope)
}

// resolveExtraFieldRefs resolves secret:// references held by single string
// values inside the extra document (ADR-041 section 2): S3's secret_key or
// SFTP's private_key as a reference to one secret, beside plain values
// such as access_key and host. Each resolved value joins the run's
// redaction set. An error names the field and the reference, never the
// value.
func (cr *ConnectionResolver) resolveExtraFieldRefs(ctx context.Context, conn *models.Connection, scope secrets.Scope) error {
	if !strings.Contains(conn.Extra, secretstore.Scheme+"://") {
		return nil
	}
	var doc map[string]interface{}
	if err := json.Unmarshal([]byte(conn.Extra), &doc); err != nil {
		return nil
	}
	changed := false
	var walk func(m map[string]interface{}, prefix string) error
	walk = func(m map[string]interface{}, prefix string) error {
		for key, raw := range m {
			path := prefix + key
			switch v := raw.(type) {
			case string:
				if !secretstore.IsRef(v) {
					continue
				}
				plain, err := cr.secrets.ResolveIn(ctx, scope, v)
				if err != nil {
					return credentialError(conn.ConnID, "extra."+path, v, err)
				}
				m[key] = plain
				addRunSecret(scope.RunID, plain)
				changed = true
			case map[string]interface{}:
				if err := walk(v, path+"."); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(doc, ""); err != nil {
		return err
	}
	if changed {
		b, err := json.Marshal(doc)
		if err != nil {
			return fmt.Errorf("connection %q: encode extra settings: %w", conn.ConnID, err)
		}
		conn.Extra = string(b)
	}
	return nil
}

// ResolveCredentials resolves conn's password_ref and extra_ref in place,
// through the same path a run uses, and returns the same error a run would
// fail with. For callers that already hold the connection and have
// decided access themselves, such as the API's connection test.
func (cr *ConnectionResolver) ResolveCredentials(conn *models.Connection) error {
	return cr.resolveCredentials(conn, secrets.Scope{WorkspaceID: conn.WorkspaceID})
}

// credentialError names the connection, the field, where its value was
// to come from, and why it could not be read. Never the value, and never
// an encrypted:// reference's ciphertext: that is described, not quoted.
func credentialError(connID, field, ref string, err error) error {
	source := ref
	if strings.HasPrefix(ref, "encrypted://") {
		source = "its stored encrypted value"
	}
	return fmt.Errorf("connection %q: %s: could not resolve %s: %w", connID, field, source, err)
}

// ResolveConnection returns a connection with its credentials resolved.
//
// Most nodes want a URI and get one from Resolve. dbt is different: it needs
// the fields separately, because a dbt profile is structured YAML rather
// than a connection string, so it cannot go through the URI path without
// being taken apart again on the other side.
//
// The returned Connection carries plaintext credentials in memory, the same
// contract Resolve already has, and must not be persisted or logged.
func (cr *ConnectionResolver) ResolveConnection(connID string) (*models.Connection, error) {
	return cr.ResolveConnectionIn(connID, "")
}

// ResolveConnectionIn is ResolveConnection for a pipeline in workspaceID:
// another workspace's connection is refused before its credentials are
// resolved.
func (cr *ConnectionResolver) ResolveConnectionIn(connID, workspaceID string) (*models.Connection, error) {
	return cr.ResolveConnectionScoped(connID, secrets.Scope{WorkspaceID: workspaceID})
}

// ResolveConnectionScoped is ResolveConnectionIn for one node of one run;
// see ResolveWithWarningsScoped.
func (cr *ConnectionResolver) ResolveConnectionScoped(connID string, scope secrets.Scope) (*models.Connection, error) {
	workspaceID := scope.WorkspaceID
	if connID == "" {
		return nil, fmt.Errorf("no conn_id given")
	}
	conn, err := cr.store.GetConnection(connID)
	if err != nil {
		return nil, fmt.Errorf("conn_id %q not found: %w", connID, err)
	}
	if !sameWorkspace(conn, workspaceID) {
		return nil, fmt.Errorf(notInWorkspace, connID)
	}
	if err := cr.ValidateConnection(conn); err != nil {
		return nil, err
	}
	if err := cr.resolveCredentials(conn, scope); err != nil {
		return nil, err
	}
	return conn, nil
}

// ResolveConnectionByID returns a connection with its credentials already
// resolved to plaintext.
//
// This exists so the control plane can resolve on a worker's behalf. A
// worker that holds no encryption key cannot turn an `encrypted://` ref
// into a password, and giving it the key to do so is exactly what the
// API-only worker exists to avoid: the key decrypts every stored
// credential in the deployment, not the one connection a job needs.
//
// So the resolution happens here, where the key already legitimately
// lives, and only the result crosses the wire. The caller is responsible
// for deciding WHICH connections a given requester may resolve; this
// answers "what is this one", not "may you have it".
//
// The returned value carries plaintext in Password and Extra and is
// never persisted in that form -- the same in-memory-only contract the
// model's own field comments already state.
func (cr *ConnectionResolver) ResolveConnectionByID(connID string) (*models.Connection, error) {
	if cr == nil || cr.store == nil {
		return nil, fmt.Errorf("resolve connection %q: no connection store", connID)
	}
	conn, err := cr.store.GetConnection(connID)
	if err != nil {
		return nil, err
	}
	if conn == nil {
		return nil, fmt.Errorf("resolve connection %q: not found", connID)
	}
	if err := cr.ValidateConnection(conn); err != nil {
		return nil, err
	}
	if err := cr.resolveCredentials(conn, secrets.Scope{WorkspaceID: conn.WorkspaceID}); err != nil {
		return nil, err
	}
	return conn, nil
}
