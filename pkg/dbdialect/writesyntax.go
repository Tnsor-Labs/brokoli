package dbdialect

// The write-path vocabulary (Tnsor-Labs/brokoli#361, finishing ADR-024).
//
// ADR-024 left statement GENERATION's dialect record in engine/sqlgen.go on
// purpose: the write path had no differential coverage to refactor against,
// and a mistake there corrupts destination tables rather than failing
// tests. That coverage exists now -- the bulk-write equivalence corpora run
// against live Postgres, MySQL and ClickHouse in CI -- so the record moves
// here, and a backend's write vocabulary lives in the backend's own file
// instead of as a row in engine code.
//
// WriteSyntax is data, not behaviour: the generator's rendering methods
// stay in engine/sqlgen.go and consume this. That split is deliberate --
// the rendering logic is shared and proven by the corpora; what differed
// per backend was only ever these values.

// WriteSyntax is what statement generation needs to know about a backend.
type WriteSyntax struct {
	// QuoteChar quotes identifiers ('"', '`', or '[' for SQL Server's
	// bracket style).
	QuoteChar string
	// StrQuote quotes string literals.
	StrQuote string
	// Terminator ends a statement.
	Terminator string
	// BoolTrue and BoolFalse are the boolean literals ("TRUE"/"FALSE",
	// "1"/"0", "true"/"false").
	BoolTrue, BoolFalse string
	// TypeMap renders an inferred pseudo-type (INTEGER, FLOAT, BOOLEAN,
	// TEXT, TIMESTAMP) as this backend's DDL spelling. This is the
	// value-sniffing fallback only; real column types go through
	// TypeRenderer (#360, #362).
	TypeMap map[string]string
	// TSLayout renders a time.Time literal; TSUTC says the literal
	// carries no zone, so the value converts to UTC first to keep the
	// instant from being reinterpreted in the session's timezone.
	TSLayout string
	TSUTC    bool
	// BackslashEscapes says a backslash escapes inside string literals
	// (MySQL and ClickHouse), so one in a value must itself be escaped.
	// Postgres deliberately not: standard_conforming_strings has been on
	// since 9.1, and escaping there corrupts the value the other way.
	BackslashEscapes bool
	// CreateTableSuffix follows a CREATE TABLE's column list. Empty
	// everywhere but ClickHouse, whose language requires an ENGINE
	// clause (ADR-027).
	CreateTableSuffix string
}

// StatementWriter is the write-vocabulary capability. Every registered
// dialect implements it -- a backend the statement generator cannot spell
// for could not be a backend at all -- and a test in engine/sqlgen pins
// that totality, so a new registration cannot forget it.
type StatementWriter interface {
	WriteSyntax() WriteSyntax
}

func (postgres) WriteSyntax() WriteSyntax {
	return WriteSyntax{
		QuoteChar: `"`, StrQuote: "'", Terminator: ";",
		BoolTrue: "TRUE", BoolFalse: "FALSE",
		TypeMap: map[string]string{
			"INTEGER": "INTEGER", "FLOAT": "DOUBLE PRECISION", "BOOLEAN": "BOOLEAN",
			"TEXT": "TEXT", "TIMESTAMP": "TIMESTAMP"},
		TSLayout: "2006-01-02 15:04:05.999999-07:00", TSUTC: false,
	}
}

func (mysqld) WriteSyntax() WriteSyntax {
	return WriteSyntax{
		QuoteChar: "`", StrQuote: "'", Terminator: ";",
		BoolTrue: "TRUE", BoolFalse: "FALSE", BackslashEscapes: true,
		TypeMap: map[string]string{
			"INTEGER": "INT", "FLOAT": "DOUBLE", "BOOLEAN": "BOOLEAN",
			"TEXT": "TEXT", "TIMESTAMP": "DATETIME"},
		TSLayout: "2006-01-02 15:04:05.999999", TSUTC: true,
	}
}

func (clickhouse) WriteSyntax() WriteSyntax {
	return WriteSyntax{
		QuoteChar: "`", StrQuote: "'", Terminator: ";",
		BoolTrue: "true", BoolFalse: "false", BackslashEscapes: true,
		TypeMap: map[string]string{
			"INTEGER": "Int64", "FLOAT": "Float64", "BOOLEAN": "Bool",
			"TEXT": "String", "TIMESTAMP": "DateTime64(6, 'UTC')"},
		TSLayout: "2006-01-02 15:04:05.999999", TSUTC: true,
		CreateTableSuffix: " ENGINE = MergeTree ORDER BY tuple()",
	}
}

func (sqlited) WriteSyntax() WriteSyntax {
	return WriteSyntax{
		QuoteChar: `"`, StrQuote: "'", Terminator: ";",
		BoolTrue: "1", BoolFalse: "0",
		TypeMap: map[string]string{
			"INTEGER": "INTEGER", "FLOAT": "REAL", "BOOLEAN": "INTEGER",
			"TEXT": "TEXT", "TIMESTAMP": "TEXT"},
		TSLayout: "2006-01-02T15:04:05.999999Z07:00", TSUTC: false,
	}
}

func (sqlserver) WriteSyntax() WriteSyntax {
	return WriteSyntax{
		QuoteChar: "[", StrQuote: "'", Terminator: ";",
		BoolTrue: "1", BoolFalse: "0",
		TypeMap: map[string]string{
			"INTEGER": "INT", "FLOAT": "FLOAT", "BOOLEAN": "BIT",
			"TEXT": "NVARCHAR(MAX)", "TIMESTAMP": "DATETIME2"},
		TSLayout: "2006-01-02 15:04:05.9999999", TSUTC: true,
	}
}

func (generic) WriteSyntax() WriteSyntax {
	return WriteSyntax{
		QuoteChar: `"`, StrQuote: "'", Terminator: ";",
		BoolTrue: "TRUE", BoolFalse: "FALSE",
		TypeMap: map[string]string{
			"INTEGER": "INTEGER", "FLOAT": "FLOAT", "BOOLEAN": "BOOLEAN",
			"TEXT": "TEXT", "TIMESTAMP": "TIMESTAMP"},
		TSLayout: "2006-01-02 15:04:05.999999", TSUTC: true,
	}
}
