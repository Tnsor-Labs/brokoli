package dbdialect

import (
	"sort"
	"strings"
)

// URI claims (#361, finishing ADR-024): which backend owns a connection-URI
// scheme, which database/sql driver opens it, and how the URI becomes that
// driver's DSN. This used to be a switch in engine/database.go -- per-backend
// data in a place that was not the backend's, so adding a backend meant
// editing someone else's table. Now a backend states its own claims next to
// the rest of its vocabulary, and the engine's detectDriver/dialectForURI are
// adapters over the registry.
//
// Deliberately NOT claims (recorded here because the fold's honesty is the
// point):
//   - snowflake:// maps to a driver with no dialect owner in this registry;
//     it stays in the engine adapter until snowflake earns a registration.
//   - the .db/.sqlite filename suffixes and the schemeless-string Postgres
//     default are heuristics about strings with no scheme to claim; they
//     stay in the adapter, pinned by its mapping tests.

// URIClaim ties one scheme to its backend.
type URIClaim struct {
	// Scheme without the "://", e.g. "postgres".
	Scheme string
	// Driver is the database/sql driver name that opens claimed URIs.
	Driver string
	// Dialect is the registry name statement generation should target.
	Dialect string
	// DSN converts a claimed URI into the driver's DSN. nil means the URI
	// itself is the DSN.
	DSN func(uri string) string
}

// URIClaimer is an optional capability: the schemes a backend owns. Same
// convention as every optional capability here -- absence degrades to the
// engine adapter's own leftovers, never to an error.
type URIClaimer interface {
	URIClaims() []URIClaim
}

// AllURIClaims collects every registered backend's claims, sorted by scheme
// so iteration order (and any message built from it) is deterministic --
// registry map order is not.
func AllURIClaims() []URIClaim {
	var out []URIClaim
	for _, d := range registry {
		if c, ok := d.(URIClaimer); ok {
			out = append(out, c.URIClaims()...)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Scheme < out[j].Scheme })
	return out
}

func stripScheme(scheme string) func(string) string {
	prefix := scheme + "://"
	return func(uri string) string { return strings.TrimPrefix(uri, prefix) }
}

func (postgres) URIClaims() []URIClaim {
	return []URIClaim{
		{Scheme: "postgres", Driver: "pgx", Dialect: "postgres"},
		{Scheme: "postgresql", Driver: "pgx", Dialect: "postgres"},
		// Redshift is Postgres-compatible: convert the scheme, open with
		// pgx, speak postgres.
		{Scheme: "redshift", Driver: "pgx", Dialect: "postgres",
			DSN: func(uri string) string {
				return "postgres://" + strings.TrimPrefix(uri, "redshift://")
			}},
	}
}

func (mysqld) URIClaims() []URIClaim {
	// go-sql-driver wants the DSN without the scheme.
	return []URIClaim{{Scheme: "mysql", Driver: "mysql", Dialect: "mysql", DSN: stripScheme("mysql")}}
}

func (clickhouse) URIClaims() []URIClaim {
	// clickhouse-go accepts the URI itself as its DSN.
	return []URIClaim{{Scheme: "clickhouse", Driver: "clickhouse", Dialect: "clickhouse"}}
}

func (sqlited) URIClaims() []URIClaim {
	return []URIClaim{{Scheme: "sqlite", Driver: "sqlite", Dialect: "sqlite", DSN: stripScheme("sqlite")}}
}

func (sqlserver) URIClaims() []URIClaim {
	return []URIClaim{
		{Scheme: "sqlserver", Driver: "sqlserver", Dialect: "sqlserver"},
		{Scheme: "mssql", Driver: "sqlserver", Dialect: "sqlserver"},
	}
}
