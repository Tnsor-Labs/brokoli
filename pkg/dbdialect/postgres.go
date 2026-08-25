package dbdialect

import (
	"net/url"
	"strings"
)

// postgres is the reference implementation. Every capability the compiler has
// is proven against it first, and a second backend claims a capability only
// when a differential test shows it agrees with this one (ADR-024).
type postgres struct{}

func (postgres) Name() string { return "postgres" }

// QuoteIdent double-quotes an identifier, doubling any embedded quote, so a
// column name cannot terminate the identifier and inject SQL.
func (postgres) QuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// QuoteQualifiedIdent quotes a possibly schema-qualified name so that
// "public.orders" becomes "public"."orders" and a name containing a dot or a
// quote cannot escape its identifier.
func (p postgres) QuoteQualifiedIdent(name string) string {
	parts := strings.Split(name, ".")
	if len(parts) > 2 {
		// More qualification than a schema and a table; quote the whole thing
		// so it fails loudly rather than being silently reinterpreted.
		return p.QuoteIdent(name)
	}
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		out = append(out, p.QuoteIdent(part))
	}
	return strings.Join(out, ".")
}

// QuoteLiteral renders a string as a SQL literal, doubling embedded quotes so
// a value cannot close the literal and inject SQL.
//
// Doubling alone is correct here and would not be on every backend:
// standard_conforming_strings has been on since 9.1, so a backslash is an
// ordinary character. Escaping it — as MySQL requires — would corrupt values
// in the other direction, turning one backslash into two.
func (postgres) QuoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// ClassifyType maps pgx's reported type names. Unknown names are left
// unclassified rather than guessed.
func (postgres) ClassifyType(name string) ColumnKind {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "TEXT", "VARCHAR", "CHAR", "BPCHAR", "NAME":
		return KindText
	// CITEXT is deliberately absent. It compares case-insensitively under a
	// non-deterministic collation, so SQL would match 'ALICE' against 'alice'
	// where the Go matcher, comparing bytes, would not.
	case "INT2", "INT4", "INT8", "SMALLINT", "INTEGER", "BIGINT",
		"FLOAT4", "FLOAT8", "REAL", "DOUBLE PRECISION", "NUMERIC", "DECIMAL":
		return KindNumeric
	default:
		return KindUnclassified
	}
}

func (postgres) ProbeColumnsSQL(query string) string {
	return "SELECT * FROM (" + query + ") AS brokoli_probe LIMIT 0"
}

func (postgres) CastToText(expr string) string  { return "CAST(" + expr + " AS TEXT)" }
func (postgres) CastToFloat(expr string) string { return "CAST(" + expr + " AS DOUBLE PRECISION)" }

// ByteOrderedText compares under the C collation, because Go compares bytes
// and the database's default collation does not.
func (postgres) ByteOrderedText(expr string) string { return expr + ` COLLATE "C"` }

// SameServer reports whether two resolved URIs address the same database on
// the same Postgres server.
//
// Deliberately strict about what counts as the same. Getting this wrong does
// not degrade performance, it writes one customer's rows using another
// customer's connection, so every uncertain case answers false:
//
//   - Host is compared as written, without DNS resolution. Two names that
//     resolve to one address are not treated as equal: resolution can change
//     between the check and the write, and a name is what the operator
//     configured.
//   - The port must match after defaulting, and the database name must match.
//   - Any difference in the credentials means false. The same server reached
//     as two different roles is two different sets of permissions, and
//     collapsing them would run one role's query inside the other's write.
func (postgres) SameServer(a, b string) bool {
	pa, oka := parsePGTarget(a)
	pb, okb := parsePGTarget(b)
	if !oka || !okb {
		return false
	}
	return pa == pb
}

// pgTarget is the identity of a Postgres endpoint for SameServer's purposes.
type pgTarget struct {
	host, port, database, user, password string
}

func parsePGTarget(raw string) (pgTarget, bool) {
	if raw == "" {
		return pgTarget{}, false
	}
	if !strings.HasPrefix(raw, "postgres://") && !strings.HasPrefix(raw, "postgresql://") {
		return pgTarget{}, false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return pgTarget{}, false
	}
	port := u.Port()
	if port == "" {
		port = "5432"
	}
	// A query string can carry connection parameters that change which server
	// is reached or how, so any of them makes the comparison unsafe to make
	// on host and port alone. Refusing here costs a pushdown; guessing costs
	// correctness.
	for key := range u.Query() {
		switch key {
		case "sslmode", "application_name", "connect_timeout":
			// Do not change which database is reached.
		default:
			return pgTarget{}, false
		}
	}
	pw, _ := u.User.Password()
	return pgTarget{
		host:     u.Hostname(),
		port:     port,
		database: strings.TrimPrefix(u.Path, "/"),
		user:     u.User.Username(),
		password: pw,
	}, true
}
