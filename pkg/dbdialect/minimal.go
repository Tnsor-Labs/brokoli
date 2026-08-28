package dbdialect

import "strings"

// The write-vocabulary-only dialects (Tnsor-Labs/brokoli#361).
//
// SQLite, SQL Server and the generic fallback join the registry so the
// statement generator has ONE table of backends instead of two, and their
// registration claims deliberately less than any other tier: the write
// vocabulary (StatementWriter) and the required Dialect surface, nothing
// more. The same structural guard that kept ClickHouse's phase-1
// registration honest applies here twice over -- no Addresser, so
// sameServer never answers yes and the compiler can never form a segment
// against them; and no TypeReader/TypeRenderer, so the typed-DDL paths
// fall back to inference exactly as they did when these names were absent
// from the registry.
//
// ClassifyType answers unclassified for everything: a classification is
// trusted by the compiler, the compiler requires a differential corpus,
// and none of these three has one. The compile-facing methods below are
// dormant by construction, implemented plainly so the interface is
// satisfied without inventing behaviour nothing can reach.

type sqlited struct{}

func (sqlited) Name() string { return "sqlite" }
func (sqlited) QuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
func (d sqlited) QuoteQualifiedIdent(name string) string { return quoteDotted(d.QuoteIdent, name) }
func (sqlited) QuoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
func (sqlited) ClassifyType(string) ColumnKind { return KindUnclassified }
func (sqlited) ProbeColumnsSQL(query string) string {
	return "SELECT * FROM (" + query + ") AS brokoli_probe LIMIT 0"
}
func (sqlited) CastToText(expr string) string      { return "CAST(" + expr + " AS TEXT)" }
func (sqlited) CastToFloat(expr string) string     { return "CAST(" + expr + " AS REAL)" }
func (sqlited) ByteOrderedText(expr string) string { return expr }

type sqlserver struct{}

func (sqlserver) Name() string { return "sqlserver" }
func (sqlserver) QuoteIdent(name string) string {
	return "[" + strings.ReplaceAll(name, "]", "]]") + "]"
}
func (d sqlserver) QuoteQualifiedIdent(name string) string { return quoteDotted(d.QuoteIdent, name) }
func (sqlserver) QuoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
func (sqlserver) ClassifyType(string) ColumnKind { return KindUnclassified }
func (sqlserver) ProbeColumnsSQL(query string) string {
	return "SELECT TOP 0 * FROM (" + query + ") AS brokoli_probe"
}
func (sqlserver) CastToText(expr string) string      { return "CAST(" + expr + " AS NVARCHAR(MAX))" }
func (sqlserver) CastToFloat(expr string) string     { return "CAST(" + expr + " AS FLOAT)" }
func (sqlserver) ByteOrderedText(expr string) string { return expr }

type generic struct{}

func (generic) Name() string { return "generic" }
func (generic) QuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
func (d generic) QuoteQualifiedIdent(name string) string { return quoteDotted(d.QuoteIdent, name) }
func (generic) QuoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
func (generic) ClassifyType(string) ColumnKind { return KindUnclassified }
func (generic) ProbeColumnsSQL(query string) string {
	return "SELECT * FROM (" + query + ") AS brokoli_probe LIMIT 0"
}
func (generic) CastToText(expr string) string      { return "CAST(" + expr + " AS TEXT)" }
func (generic) CastToFloat(expr string) string     { return "CAST(" + expr + " AS FLOAT)" }
func (generic) ByteOrderedText(expr string) string { return expr }

// quoteDotted quotes a possibly schema-qualified name part by part, the
// same rule every other dialect applies: more than two parts is quoted
// whole so it fails loudly rather than being silently reinterpreted.
func quoteDotted(quote func(string) string, name string) string {
	parts := strings.Split(name, ".")
	if len(parts) > 2 {
		return quote(name)
	}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, quote(p))
	}
	return strings.Join(out, ".")
}
