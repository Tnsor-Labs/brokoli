package engine

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// sqlColumnKind is as much as the prefix compiler needs to know about a
// column: whether comparing it in SQL will agree with comparing its rendered
// text in Go.
//
// The distinction that matters is text versus numeric, because Brokoli's
// transform rules compare the fmt.Sprintf("%v") rendering of a value while
// SQL compares the value. For a numeric column both end up numeric; for a
// text column both end up byte-wise, provided the SQL side is told to use the
// C collation. Anything else is not classified, and rules touching it are not
// compiled.
type sqlColumnKind int

const (
	kindUnclassified sqlColumnKind = iota
	kindText
	kindNumeric
)

// describeQueryColumns asks the server what a query's columns are without
// running it. LIMIT 0 returns the row description and no rows, so this costs a
// round trip rather than the query.
func describeQueryColumns(ctx context.Context, uri, query string) (map[string]sqlColumnKind, error) {
	driver, dsn, err := DetectDriver(uri)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", driver, err)
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, fmt.Sprintf("SELECT * FROM (%s) AS brokoli_probe LIMIT 0", query))
	if err != nil {
		return nil, fmt.Errorf("describe columns: %w", err)
	}
	defer rows.Close()

	types, err := rows.ColumnTypes()
	if err != nil {
		return nil, err
	}
	out := make(map[string]sqlColumnKind, len(types))
	for _, ct := range types {
		out[ct.Name()] = classifyDatabaseType(ct.DatabaseTypeName())
	}
	return out, rows.Err()
}

// classifyDatabaseType maps a driver's type name onto the only two categories
// the compiler can reason about. Unknown names are left unclassified rather
// than guessed: a wrong guess here produces rows that differ depending on
// which backend ran, which is the failure this whole exercise exists to avoid.
func classifyDatabaseType(name string) sqlColumnKind {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "TEXT", "VARCHAR", "CHAR", "BPCHAR", "NAME":
		return kindText
	// CITEXT is deliberately absent. It compares case-insensitively under a
	// non-deterministic collation, so SQL would match 'ALICE' against 'alice'
	// where the Go matcher, comparing bytes, would not.
	case "INT2", "INT4", "INT8", "SMALLINT", "INTEGER", "BIGINT",
		"FLOAT4", "FLOAT8", "REAL", "DOUBLE PRECISION", "NUMERIC", "DECIMAL":
		return kindNumeric
	default:
		// Dates, booleans, JSON, arrays, enums, and anything a driver reports
		// under a name not listed here. Each would need its own demonstration
		// that Go's rendering and SQL's comparison agree.
		return kindUnclassified
	}
}
