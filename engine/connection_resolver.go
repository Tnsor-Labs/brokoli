package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"reflect"
	"strings"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/identity"
	"github.com/Tnsor-Labs/brokoli/pkg/secrets"
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
}

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
	var warnings []string
	resolved, err := cr.resolve(config, nodeType, workspaceID, func(format string, args ...interface{}) {
		warnings = append(warnings, fmt.Sprintf(format, args...))
	})
	return resolved, warnings, err
}

// ResolveIn is Resolve for a pipeline in workspaceID; see
// ResolveWithWarningsIn.
func (cr *ConnectionResolver) ResolveIn(config map[string]interface{}, nodeType models.NodeType, workspaceID string) (map[string]interface{}, error) {
	return cr.resolve(config, nodeType, workspaceID, nil)
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
	return cr.resolve(config, nodeType, "", nil)
}

func (cr *ConnectionResolver) resolve(config map[string]interface{}, nodeType models.NodeType, workspaceID string, warn func(string, ...interface{})) (map[string]interface{}, error) {
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

	if err := cr.resolveCredentials(conn); err != nil {
		return config, err
	}

	// Parse decrypted extra into a map
	var extra map[string]interface{}
	if conn.Extra != "" {
		json.Unmarshal([]byte(conn.Extra), &extra)
	}

	// Inject connection fields based on node type
	resolved := make(map[string]interface{}, len(config))
	for k, v := range config {
		resolved[k] = v
	}

	switch nodeType {
	case models.NodeTypeSourceDB, models.NodeTypeSinkDB:
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
		resolved["uri"] = conn.BuildURI()

	case models.NodeTypeSourceAPI, models.NodeTypeSinkAPI:
		resolveAPIConnectionFields(config, resolved, conn, extra)
	}

	return resolved, nil
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
func (cr *ConnectionResolver) resolveCredentials(conn *models.Connection) error {
	if cr.secrets == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if conn.PasswordRef != "" {
		plain, err := cr.secrets.Resolve(ctx, conn.PasswordRef)
		if err != nil {
			return credentialError(conn.ConnID, "password", conn.PasswordRef, err)
		}
		conn.Password = plain
	} else if conn.Password != "" {
		if plain, err := cr.secrets.Resolve(ctx, conn.Password); err == nil {
			conn.Password = plain
		}
	}

	if conn.ExtraRef != "" {
		plain, err := cr.secrets.Resolve(ctx, conn.ExtraRef)
		if err != nil {
			return credentialError(conn.ConnID, "extra settings", conn.ExtraRef, err)
		}
		conn.Extra = plain
	} else if conn.Extra != "" {
		if plain, err := cr.secrets.Resolve(ctx, conn.Extra); err == nil {
			conn.Extra = plain
		}
	}
	return nil
}

// ResolveCredentials resolves conn's password_ref and extra_ref in place,
// through the same path a run uses, and returns the same error a run would
// fail with. For callers that already hold the connection and have
// decided access themselves, such as the API's connection test.
func (cr *ConnectionResolver) ResolveCredentials(conn *models.Connection) error {
	return cr.resolveCredentials(conn)
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
	if err := cr.resolveCredentials(conn); err != nil {
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
	if err := cr.resolveCredentials(conn); err != nil {
		return nil, err
	}
	return conn, nil
}
