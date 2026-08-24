package dbdialect

import (
	"fmt"
	"strings"

	gomysql "github.com/go-sql-driver/mysql"
)

// mysqld is the MySQL backend. Registered only because the differential
// corpus in engine/transform_sql_diff_test.go runs against a live MySQL and
// passes -- ADR-024's rule is that this registration IS the capability
// claim, and the corpus is the proof. (Named mysqld to keep the driver
// package's name free.)
type mysqld struct{}

func (mysqld) Name() string { return "mysql" }

// QuoteIdent backtick-quotes one identifier, doubling any embedded backtick,
// so a column name cannot terminate the identifier and inject SQL.
func (mysqld) QuoteIdent(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

func (m mysqld) QuoteQualifiedIdent(name string) string {
	parts := strings.Split(name, ".")
	if len(parts) > 2 {
		// More qualification than a schema and a table; quote whole so it
		// fails loudly rather than being silently reinterpreted.
		return m.QuoteIdent(name)
	}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, m.QuoteIdent(p))
	}
	return strings.Join(out, ".")
}

// QuoteLiteral escapes backslashes before doubling quotes -- MySQL treats a
// backslash as an escape inside literals unless NO_BACKSLASH_ESCAPES is set,
// which is not the default. The order matters: doubling first would leave
// the introduced quote escapable by a preceding backslash. Same rule as
// engine/sqlgen.go's mysql quoteString, proven against a live server by
// TestMySQLValuesRoundTripIntact.
func (mysqld) QuoteLiteral(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// ClassifyType maps go-sql-driver's DatabaseTypeName spellings (fields.go)
// onto the two kinds the compiler reasons about. The driver's text protocol
// hands Go every value as the server's own text rendering, which is what
// makes these classifications answerable at all: the Go side of every
// comparison is a string the server produced.
//
// Deliberately unclassified, each for a reason:
//
//   - BINARY, VARBINARY and the BLOBs: their bytes are the column's raw
//     value, but a literal in a generated statement is in the connection
//     character set, and equality between the two is not the byte equality
//     Go performs. Each would need its own demonstration.
//   - DATE, DATETIME, TIMESTAMP, TIME, YEAR: rendering depends on the
//     session time zone for TIMESTAMP, and none have a proven agreement.
//   - JSON, ENUM, SET, BIT, GEOMETRY, VECTOR: no comparison story at all.
func (mysqld) ClassifyType(name string) ColumnKind {
	n := strings.ToUpper(strings.TrimSpace(name))
	n = strings.TrimPrefix(n, "UNSIGNED ")
	switch n {
	case "CHAR", "VARCHAR", "TEXT", "TINYTEXT", "MEDIUMTEXT", "LONGTEXT":
		return KindText
	case "TINYINT", "SMALLINT", "MEDIUMINT", "INT", "BIGINT",
		"FLOAT", "DOUBLE", "DECIMAL":
		return KindNumeric
	default:
		return KindUnclassified
	}
}

func (mysqld) ProbeColumnsSQL(query string) string {
	return fmt.Sprintf("SELECT * FROM (%s) AS brokoli_probe LIMIT 0", query)
}

// CastToText is CHAR -- MySQL has no TEXT cast target. The result is the
// server's own text rendering, which is byte-for-byte what the text
// protocol already handed the Go side, so both backends compare the same
// string.
func (mysqld) CastToText(expr string) string {
	return "CAST(" + expr + " AS CHAR)"
}

// CastToFloat is DOUBLE (a cast target since 8.0.17; the upsert work
// already set an 8.0.20 floor). Go compares through strconv.ParseFloat, so
// the SQL side has to lose the same precision -- DECIMAL would be more
// accurate and would therefore disagree, exactly as NUMERIC would on
// Postgres.
func (mysqld) CastToFloat(expr string) string {
	return "CAST(" + expr + " AS DOUBLE)"
}

// ByteOrderedText converts to the connection character set first, then
// compares as binary. The conversion is the load-bearing half: CAST(col AS
// BINARY) alone yields the column's own encoding, and for a non-utf8mb4
// column those are not the bytes the driver handed Go. Converting to
// utf8mb4 makes both sides compare the same byte sequence, and BINARY
// comparison is NO PAD, so trailing spaces are significant exactly as they
// are in Go.
func (mysqld) ByteOrderedText(expr string) string {
	return "CAST(CONVERT(" + expr + " USING utf8mb4) AS BINARY)"
}

// SameServer reports whether two resolved mysql:// URIs address the same
// database on the same server, with the same strictness as the Postgres
// rule and for the same reason: getting this wrong does not degrade
// performance, it writes one customer's rows using another customer's
// connection, so every uncertain case answers false.
//
// The DSN is parsed by the driver's own parser rather than a
// reimplementation -- #338 exists because a second parser of the same
// format drifted from the first.
//
//   - Only TCP. A socket path is a different addressing scheme with its own
//     equivalence argument.
//
//   - Address as written, after the parser's default-port normalization; no
//     DNS resolution, for the reasons on the Postgres rule.
//
//   - User, password, and database compared exactly.
//
//   - Any DSN parameter outside a small client-only allowlist
//     disqualifies. charset and collation change session comparison
//     semantics; anything unrecognized is unknowable. Timeouts, parseTime,
//     loc and tls affect the client session, not which server the composed
//     statement runs on -- it executes entirely server-side -- so they are
//     allowed (and deliberately not compared).
//
//     The parameter keys are read from the raw DSN rather than the parsed
//     Config, because the parser stores charset in an unexported field: a
//     rule that inspected only the exported struct would silently allow
//     the one parameter with the clearest comparison-semantics impact.
func (mysqld) SameServer(a, b string) bool {
	ca, oka := parseMySQLTarget(a)
	cb, okb := parseMySQLTarget(b)
	if !oka || !okb {
		return false
	}
	return ca == cb
}

// mysqlTarget is the identity of a MySQL endpoint for SameServer's purposes.
type mysqlTarget struct {
	addr, database, user, password string
}

func parseMySQLTarget(raw string) (mysqlTarget, bool) {
	if !strings.HasPrefix(raw, "mysql://") {
		return mysqlTarget{}, false
	}
	dsn := strings.TrimPrefix(raw, "mysql://")
	cfg, err := gomysql.ParseDSN(dsn)
	if err != nil {
		return mysqlTarget{}, false
	}
	if cfg.Net != "tcp" {
		return mysqlTarget{}, false
	}
	if !mysqlParamsAreClientOnly(dsn) {
		return mysqlTarget{}, false
	}
	return mysqlTarget{
		addr:     cfg.Addr,
		database: cfg.DBName,
		user:     cfg.User,
		password: cfg.Passwd,
	}, true
}

// mysqlParamsAreClientOnly reports whether every parameter key in the DSN's
// query string is on the client-only allowlist. The split mirrors the
// driver's own: the database name starts after the LAST slash (a password
// may contain one), and parameters after the first question mark past it.
func mysqlParamsAreClientOnly(dsn string) bool {
	slash := strings.LastIndexByte(dsn, '/')
	if slash < 0 {
		return true
	}
	q := strings.IndexByte(dsn[slash:], '?')
	if q < 0 {
		return true
	}
	for _, kv := range strings.Split(dsn[slash+q+1:], "&") {
		key := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			key = kv[:i]
		}
		switch key {
		case "parseTime", "loc", "timeout", "readTimeout", "writeTimeout", "tls":
			// Client-session only; the composed statement never passes
			// through this client's value conversion or timeouts.
		default:
			return false
		}
	}
	return true
}
