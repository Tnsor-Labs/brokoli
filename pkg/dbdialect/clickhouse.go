package dbdialect

import "strings"

// clickhouse is the ClickHouse backend (ADR-027), read-side only in phase 1
// of Tnsor-Labs/brokoli#382.
//
// What "read-side" means here, concretely: this type deliberately does NOT
// implement Addresser, and that absence is load-bearing. Pushdown segments
// only form when sameServer answers yes, and sameServer requires the
// Addresser capability -- so with this registration the compiler can never
// compose a ClickHouse segment, which is ADR-027's "pushdown starts fully
// refused" enforced structurally rather than by a flag someone could forget.
// The write path is gated separately, by name, in the engine's sink and
// migrate handlers until phase 2 earns it.
//
// The methods below still matter for reads: ProbeColumnsSQL and the type
// reader in columntype_clickhouse.go are what let a migration out of
// ClickHouse carry real column types (#360's rule), and the quoting rules
// are shared vocabulary for everything later phases add.
type clickhouse struct{}

func (clickhouse) Name() string { return "clickhouse" }

// QuoteIdent backtick-quotes one identifier. ClickHouse accepts both
// backticks and double quotes; backticks are chosen so the escaping rule is
// shared with MySQL's rather than being a third convention.
func (clickhouse) QuoteIdent(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

func (c clickhouse) QuoteQualifiedIdent(name string) string {
	parts := strings.Split(name, ".")
	if len(parts) > 2 {
		// More qualification than a database and a table; quote whole so
		// it fails loudly rather than being silently reinterpreted.
		return c.QuoteIdent(name)
	}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, c.QuoteIdent(p))
	}
	return strings.Join(out, ".")
}

// QuoteLiteral escapes backslashes before doubling quotes -- ClickHouse
// treats a backslash as an escape inside string literals, the same rule as
// MySQL's and applied in the same order for the same reason: doubling first
// would leave the introduced quote escapable by a preceding backslash.
// Proven against the live server by TestClickHouseLiteralsRoundTrip.
func (clickhouse) QuoteLiteral(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// ClassifyType maps clickhouse-go's DatabaseTypeName spellings onto the two
// kinds the compiler reasons about. The driver reports ClickHouse's own type
// names, wrappers included -- Nullable(Int64), LowCardinality(String) -- so
// the wrappers are stripped before the name is judged.
//
// Classification is conservative on purpose. Nothing consumes it yet --
// phase 1 registers no Addresser, so no segment ever compiles -- but the
// answers must already be true, because a later phase will trust them:
//
//   - Integers and floats are numeric. Decimals are NOT: clickhouse-go
//     hands them to Go as a decimal value whose %v rendering ("10.25") is
//     not guaranteed to match ClickHouse's own text rendering for every
//     precision, and the compiler's contract is byte-agreement between the
//     two sides. Unclassified until a corpus proves otherwise.
//   - String is text. FixedString is NOT: it is padded with NUL bytes to
//     its declared width, so the SQL side compares the padded value while
//     Go compares the trimmed one the driver delivered.
//   - Everything else -- dates, UUIDs, enums, arrays, maps, JSON -- is
//     unclassified, each waiting on its own demonstration.
func (clickhouse) ClassifyType(databaseTypeName string) ColumnKind {
	name := unwrapClickHouseType(databaseTypeName)
	base := name
	if i := strings.IndexByte(base, '('); i > 0 {
		base = base[:i]
	}
	switch base {
	case "Int8", "Int16", "Int32", "Int64",
		"UInt8", "UInt16", "UInt32", "UInt64",
		"Float32", "Float64":
		return KindNumeric
	case "String":
		return KindText
	default:
		return KindUnclassified
	}
}

// ProbeColumnsSQL wraps a query so it returns the column description and no
// rows. ClickHouse supports LIMIT 0.
func (clickhouse) ProbeColumnsSQL(query string) string {
	return "SELECT * FROM (" + query + ") AS brokoli_probe LIMIT 0"
}

// CastToText renders an expression as text. Present because the Dialect
// interface requires it; nothing reaches it in phase 1 (no Addresser, no
// segments), and a later pushdown phase must prove it against the corpus
// before anything may.
func (c clickhouse) CastToText(expr string) string {
	return "CAST(" + expr + " AS String)"
}

// CastToFloat renders an expression as Float64 -- the same double precision
// Go's strconv.ParseFloat produces, deliberately lossy in the same way.
func (c clickhouse) CastToFloat(expr string) string {
	return "CAST(" + expr + " AS Float64)"
}

// ByteOrderedText is the identity: ClickHouse compares String byte-wise by
// default -- there is no session collation to override, which is the
// property the other backends need a COLLATE clause to get.
func (clickhouse) ByteOrderedText(expr string) string {
	return expr
}

// unwrapClickHouseType strips the wrapper types that decorate a column's
// underlying type: Nullable(Int64) and LowCardinality(String) -- possibly
// nested, as in Nullable(LowCardinality(String)) -- name an Int64 and
// Strings that a reader receives exactly as if unwrapped.
func unwrapClickHouseType(name string) string {
	name = strings.TrimSpace(name)
	for {
		switch {
		case strings.HasPrefix(name, "Nullable(") && strings.HasSuffix(name, ")"):
			name = name[len("Nullable(") : len(name)-1]
		case strings.HasPrefix(name, "LowCardinality(") && strings.HasSuffix(name, ")"):
			name = name[len("LowCardinality(") : len(name)-1]
		default:
			return name
		}
	}
}
