package engine

import (
	"net/url"
	"os"
	"strings"
)

// TableRef is ADR-023's reference kind for rows that are still in a database.
// It says "these rows are what this query returns on this server" rather than
// carrying the rows themselves, so a consumer on the same server can read them
// without the engine ever seeing one.
//
// It never leaves the run that produced it. A TableRef is valid only inside
// its own execution; segment barriers stay BlobRef, which is where ADR-010's
// durability lives.
type TableRef struct {
	// ConnURI is the server the query runs on, resolved from conn_id.
	ConnURI string
	// Query returns the rows this reference stands for.
	Query string
	// Columns is the column order the query produces, so a consumer can plan
	// without executing it.
	Columns []string
	// Absorbed names the nodes whose work this reference already represents.
	// They ran no rows through the engine, and the segment's row count is not
	// known until the consumer executes -- at which point every node here is
	// credited with it, because the segment is one unit of work.
	Absorbed []string
}

// dataPlaneInterpreted reports whether the operator has forced every edge onto
// the interpreted path -- ADR-023's escape hatch, in the shape of
// BROKOLI_SINK_COPY=0. An incident response, not a pipeline concept.
func dataPlaneInterpreted() bool {
	v := strings.TrimSpace(os.Getenv("BROKOLI_DATA_PLANE"))
	return strings.EqualFold(v, "interpreted")
}

// sameServer reports whether two resolved connection URIs address the same
// database on the same server, so that a query against one can be read
// directly by a write against the other.
//
// It is deliberately strict about what counts as the same. Getting this wrong
// does not degrade performance, it writes one customer's rows using another
// customer's connection, so every uncertain case answers false:
//
//   - Only Postgres. Other backends need their own equivalence argument.
//   - Host is compared as written, without DNS resolution. Two names that
//     resolve to one address are not treated as equal: resolution can change
//     between the check and the write, and a name is what the operator
//     configured.
//   - The port must match after defaulting, and the database name must match.
//   - Any difference in the credentials means false. The same server reached
//     as two different roles is two different sets of permissions, and
//     collapsing them would run one role's query inside the other's write.
func sameServer(a, b string) bool {
	pa, oka := parsePGTarget(a)
	pb, okb := parsePGTarget(b)
	if !oka || !okb {
		return false
	}
	return pa == pb
}

// pgTarget is the identity of a Postgres endpoint for sameServer's purposes.
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
	if q := u.Query(); len(q) > 0 {
		for key := range q {
			switch key {
			case "sslmode", "application_name", "connect_timeout":
				// Do not change which database is reached.
			default:
				return pgTarget{}, false
			}
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

// quoteQualifiedIdentPG quotes a possibly schema-qualified table name so that
// "public.orders" becomes "public"."orders" and a name containing a dot or a
// quote cannot escape its identifier.
func quoteQualifiedIdentPG(name string) string {
	parts := strings.Split(name, ".")
	if len(parts) > 2 {
		// More qualification than a schema and a table; quote whole so it
		// fails loudly rather than being silently reinterpreted.
		return quoteIdentPG(name)
	}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, quoteIdentPG(p))
	}
	return strings.Join(out, ".")
}
