package engine

import (
	"os"
	"strings"

	"github.com/Tnsor-Labs/brokoli/pkg/dbdialect"
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
	// Dialect is the backend this query is written for, carried so a
	// consumer emits in the same SQL rather than re-deriving it and possibly
	// disagreeing.
	Dialect string
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
// The rules are the backend's — what counts as "the same server" is a
// property of how that backend addresses things — so this asks the dialect.
// A backend that does not implement dbdialect.Addresser never pushes down,
// which is the absent-capability-degrades rule rather than an error.
func sameServer(dialectName, a, b string) bool {
	d, ok := dbdialect.For(dialectName)
	if !ok {
		return false
	}
	addr, ok := d.(dbdialect.Addresser)
	if !ok {
		return false
	}
	return addr.SameServer(a, b)
}
