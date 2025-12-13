package store

import (
	"fmt"
	"strings"
)

// NewStore creates the appropriate store implementation based on the URI.
// - postgres:// or postgresql:// → PostgresStore
// - anything else → SQLiteStore (treats URI as file path)
func NewStore(uri string) (Store, error) {
	if strings.HasPrefix(uri, "postgres://") || strings.HasPrefix(uri, "postgresql://") {
		return NewPostgresStore(uri)
	}

	// Default to SQLite
	return NewSQLiteStore(uri)
}

// DriverName returns "postgres" or "sqlite" based on URI for display.
func DriverName(uri string) string {
	if strings.HasPrefix(uri, "postgres://") || strings.HasPrefix(uri, "postgresql://") {
		return "postgres"
	}
	return "sqlite"
}

// Describe returns a human-readable description of the store.
func Describe(uri string) string {
	if strings.HasPrefix(uri, "postgres://") || strings.HasPrefix(uri, "postgresql://") {
		// Mask password in URI
		masked := uri
		if idx := strings.Index(masked, "://"); idx > 0 {
			rest := masked[idx+3:]
			if atIdx := strings.Index(rest, "@"); atIdx > 0 {
				if colonIdx := strings.Index(rest[:atIdx], ":"); colonIdx > 0 {
					masked = masked[:idx+3] + rest[:colonIdx] + ":****@" + rest[atIdx+1:]
				}
			}
		}
		return fmt.Sprintf("PostgreSQL (%s)", masked)
	}
	return fmt.Sprintf("SQLite (%s)", uri)
}
