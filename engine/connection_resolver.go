package engine

import (
	"context"
	"encoding/json"
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

// Resolve checks if the config has a conn_id and replaces connection fields with resolved values.
// Returns the config unchanged if no conn_id is present (backward compatible).
func (cr *ConnectionResolver) Resolve(config map[string]interface{}, nodeType models.NodeType) map[string]interface{} {
	connID, ok := config["conn_id"].(string)
	if !ok || connID == "" {
		return config
	}

	conn, err := cr.store.GetConnection(connID)
	if err != nil {
		log.Printf("[conn-resolver] WARNING: conn_id %q not found: %v", connID, err)
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
		resolved["uri"] = conn.BuildURI()

	case models.NodeTypeSourceAPI:
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

	return resolved
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
