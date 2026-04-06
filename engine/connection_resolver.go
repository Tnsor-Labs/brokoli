package engine

import (
	"encoding/json"
	"log"

	"github.com/Tnsor-Labs/brokoli/crypto"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// ConnectionResolver resolves conn_id in node configs to actual connection URIs and headers.
type ConnectionResolver struct {
	store  store.Store
	crypto *crypto.Config
}

// NewConnectionResolver creates a new resolver.
func NewConnectionResolver(s store.Store, c *crypto.Config) *ConnectionResolver {
	return &ConnectionResolver{store: s, crypto: c}
}

// Resolve checks if the config has a conn_id and replaces connection fields with resolved values.
// Returns the config unchanged if no conn_id is present (backward compatible).
func (cr *ConnectionResolver) Resolve(config map[string]interface{}, nodeType models.NodeType) map[string]interface{} {
	connID, ok := config["conn_id"].(string)
	if !ok || connID == "" {
		return config // no connection reference, keep raw config
	}

	conn, err := cr.store.GetConnection(connID)
	if err != nil {
		log.Printf("[conn-resolver] WARNING: conn_id %q not found: %v", connID, err)
		return config
	}

	// Decrypt password
	if conn.Password != "" && cr.crypto != nil {
		if dec, err := cr.crypto.Decrypt(conn.Password); err == nil {
			conn.Password = dec
		}
	}

	// Decrypt extras
	var extra map[string]interface{}
	if conn.Extra != "" && cr.crypto != nil {
		if dec, err := cr.crypto.Decrypt(conn.Extra); err == nil {
			json.Unmarshal([]byte(dec), &extra)
		}
	}

	// Inject connection fields based on node type
	resolved := make(map[string]interface{}, len(config))
	for k, v := range config {
		resolved[k] = v
	}

	switch nodeType {
	case models.NodeTypeSourceDB, models.NodeTypeSinkDB:
		// Build URI from connection
		resolved["uri"] = conn.BuildURI()

	case models.NodeTypeSourceAPI:
		// Set base URL and inject headers
		baseURL := conn.BuildURI()
		// If node has a relative URL path, append it to base
		if path, ok := config["url"].(string); ok && path != "" && path[0] == '/' {
			resolved["url"] = baseURL + path
		} else if _, ok := config["url"].(string); !ok || config["url"] == "" {
			resolved["url"] = baseURL
		}
		// Merge headers from connection extras
		if extra != nil {
			if connHeaders, ok := extra["headers"].(map[string]interface{}); ok {
				// Merge: connection headers as base, node headers override
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
		// Basic auth from connection
		if conn.Login != "" {
			resolved["auth_user"] = conn.Login
			resolved["auth_password"] = conn.Password
		}
	}

	return resolved
}
