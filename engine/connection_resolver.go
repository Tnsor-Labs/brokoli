package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/secrets"
	"github.com/Tnsor-Labs/brokoli/store"
)

// ConnectionResolver resolves conn_id in node configs to actual connection URIs and headers.
// Credentials are resolved via the secrets.Chain at execution time — the resolver never
// caches plaintext passwords beyond the scope of a single Resolve call.
type ConnectionResolver struct {
	store   store.Store
	secrets *secrets.Chain
}

// NewConnectionResolver creates a new resolver.
func NewConnectionResolver(s store.Store, sec *secrets.Chain) *ConnectionResolver {
	return &ConnectionResolver{store: s, secrets: sec}
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
func (cr *ConnectionResolver) ResolveWithWarnings(config map[string]interface{}, nodeType models.NodeType) (map[string]interface{}, []string) {
	var warnings []string
	resolved := cr.resolve(config, nodeType, func(format string, args ...interface{}) {
		warnings = append(warnings, fmt.Sprintf(format, args...))
	})
	return resolved, warnings
}

// Resolve checks if the config has a conn_id and replaces connection fields with resolved values.
// Returns the config unchanged if no conn_id is present (backward compatible).
//
// Callers that can reach the run's log should prefer ResolveWithWarnings.
func (cr *ConnectionResolver) Resolve(config map[string]interface{}, nodeType models.NodeType) map[string]interface{} {
	return cr.resolve(config, nodeType, nil)
}

func (cr *ConnectionResolver) resolve(config map[string]interface{}, nodeType models.NodeType, warn func(string, ...interface{})) map[string]interface{} {
	connID, ok := config["conn_id"].(string)
	if !ok || connID == "" {
		return config
	}

	conn, err := cr.store.GetConnection(connID)
	if err != nil {
		msg, args := "conn_id %q not found: %v", []interface{}{connID, err}
		log.Printf("[conn-resolver] WARNING: "+msg, args...)
		if warn != nil {
			warn(msg, args...)
		}
		return config
	}

	cr.resolveCredentials(conn)

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
		if !conn.BuildsURI() {
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

	return resolved
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
func (cr *ConnectionResolver) resolveCredentials(conn *models.Connection) {
	if cr.secrets == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if conn.PasswordRef != "" {
		if plain, err := cr.secrets.Resolve(ctx, conn.PasswordRef); err != nil {
			log.Printf("[conn-resolver] failed to resolve password for conn %q: %v", conn.ConnID, err)
		} else {
			conn.Password = plain
		}
	} else if conn.Password != "" {
		if plain, err := cr.secrets.Resolve(ctx, conn.Password); err == nil {
			conn.Password = plain
		}
	}

	if conn.ExtraRef != "" {
		if plain, err := cr.secrets.Resolve(ctx, conn.ExtraRef); err != nil {
			log.Printf("[conn-resolver] failed to resolve extra for conn %q: %v", conn.ConnID, err)
		} else {
			conn.Extra = plain
		}
	} else if conn.Extra != "" {
		if plain, err := cr.secrets.Resolve(ctx, conn.Extra); err == nil {
			conn.Extra = plain
		}
	}
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
	if connID == "" {
		return nil, fmt.Errorf("no conn_id given")
	}
	conn, err := cr.store.GetConnection(connID)
	if err != nil {
		return nil, fmt.Errorf("conn_id %q not found: %w", connID, err)
	}
	cr.resolveCredentials(conn)
	return conn, nil
}
