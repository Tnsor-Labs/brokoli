// Package dbdialect describes a database backend in one place.
//
// ADR-024. Before this, a backend was described by about thirty-five branch
// points spread across the tree: five quoting helpers with three hardcoded
// Postgres, type classification written in one driver's vocabulary, and a
// dialect parameter threaded through the pushdown compiler whose only caller
// passed a literal. Two defects fixed shortly before this package existed
// were that sprawl expressing itself — an escaping rule and a URI builder,
// each correct for the backend it was written against.
//
// The surface here is deliberately derived from what the SQL pushdown
// compiler actually calls, rather than from what a database might plausibly
// need. Anything speculative would be an interface shaped by imagination and
// bent by the first real second backend.
//
// # Scope
//
// This covers the compiler's needs. engine/sqlgen.go still carries its own
// dialect record for statement generation — type maps, timestamp layouts,
// literal escaping. Merging the two is the obvious next step and is
// deliberately not done here: that record is on the write path, and a
// refactor that also changes behaviour cannot use the existing suite as its
// oracle.
package dbdialect

// ColumnKind is as much as the compiler needs to know about a column: whether
// comparing it in SQL will agree with comparing its rendered text in Go.
//
// Brokoli's transform rules compare the fmt.Sprintf("%v") rendering of a
// value, so the distinction that matters is text versus numeric. For a
// numeric column both sides end up numeric; for a text column both end up
// byte-wise, provided the SQL side is told to use a byte-ordered collation.
// Anything else is unclassified, and rules touching it are not compiled.
type ColumnKind int

const (
	// KindUnclassified is the safe answer. Dates, booleans, JSON, arrays,
	// enums, and any type name a backend reports that is not listed: each
	// would need its own demonstration that Go's rendering and SQL's
	// comparison agree, and a wrong guess produces rows that differ
	// depending on which backend ran.
	KindUnclassified ColumnKind = iota
	KindText
	KindNumeric
)

func (k ColumnKind) String() string {
	switch k {
	case KindText:
		return "text"
	case KindNumeric:
		return "numeric"
	default:
		return "unclassified"
	}
}

// Dialect is what a backend must answer for the SQL compiler to emit against
// it. Every method is about saying something in this backend's SQL; none of
// them execute anything.
type Dialect interface {
	// Name is the dialect's name as configuration spells it.
	Name() string

	// QuoteIdent renders one identifier so that a column called `select`, or
	// one containing the quote character, cannot alter the statement.
	QuoteIdent(name string) string

	// QuoteQualifiedIdent renders a possibly schema-qualified name, so
	// "public.orders" becomes two quoted parts rather than one identifier
	// containing a dot.
	QuoteQualifiedIdent(name string) string

	// QuoteLiteral renders a string as a literal, escaped for this backend.
	// What "escaped" means differs: doubling the quote is enough where a
	// backslash is an ordinary character and is not enough where it escapes.
	QuoteLiteral(s string) string

	// ClassifyType maps a driver's reported column type onto a ColumnKind.
	// The names differ per driver — pgx says INT4 and BPCHAR where another
	// driver says INT and CHAR — which is why this belongs to the backend
	// rather than to the compiler.
	ClassifyType(databaseTypeName string) ColumnKind

	// ProbeColumnsSQL wraps a query so it returns the column description and
	// no rows. LIMIT 0 is not universal; SQL Server wants TOP 0 and Oracle
	// FETCH FIRST 0 ROWS ONLY.
	ProbeColumnsSQL(query string) string

	// CastToText renders an expression as text, for comparing a value the way
	// Go compares its rendering.
	CastToText(expr string) string

	// CastToFloat renders an expression as the same double-precision type Go
	// gets from strconv.ParseFloat — deliberately lossy in the same way, so
	// a bigint past 2^53 compares identically on both sides. A more accurate
	// cast would disagree.
	CastToFloat(expr string) string

	// ByteOrderedText renders a text expression so it compares byte-wise
	// rather than by the database's default collation, because Go compares
	// bytes.
	ByteOrderedText(expr string) string
}

// Addresser is an optional capability: whether two resolved connection URIs
// address the same database on the same server, which is what lets a segment
// run inside the database instead of through the engine.
//
// A backend that does not implement it simply never pushes down — the absence
// degrades to today's behaviour rather than to an error, which is the
// convention every optional capability in this codebase follows.
type Addresser interface {
	// SameServer reports whether a query against one URI may be read
	// directly by a write against the other.
	//
	// Implementations answer false for every uncertain case. Getting this
	// wrong does not make a run slow, it runs one connection's query inside
	// another connection's transaction.
	SameServer(a, b string) bool
}

// For returns the dialect of that name, and whether one is known.
//
// It answers (nil, false) rather than an error for an unknown name, matching
// how every other capability lookup in this codebase reports "not supported":
// the caller falls back to the path it would have taken anyway.
func For(name string) (Dialect, bool) {
	d, ok := registry[name]
	return d, ok
}

// A backend appears here only with the proof its registration claims.
// For postgres and mysql the claim is full compilation and the proof is the
// differential corpus in engine/transform_sql_diff_test.go. For clickhouse
// (ADR-027 phase 1) the claim is deliberately narrower -- read-side only:
// it implements no Addresser, so sameServer never answers yes and the
// compiler can never form a ClickHouse segment. Widening that claim means
// implementing Addresser WITH the corpus passing against a live ClickHouse,
// not editing this map.
var registry = map[string]Dialect{
	"postgres":   postgres{},
	"mysql":      mysqld{},
	"clickhouse": clickhouse{},
}
