package engine

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/Tnsor-Labs/brokoli/pkg/dbdialect"
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

// The kinds mirror dbdialect's, so a dialect's answer converts directly.
const (
	kindUnclassified = sqlColumnKind(dbdialect.KindUnclassified)
	kindText         = sqlColumnKind(dbdialect.KindText)
	kindNumeric      = sqlColumnKind(dbdialect.KindNumeric)
)

// describeQueryColumns asks the server what a query's columns are without
// running it. LIMIT 0 returns the row description and no rows, so this costs a
// round trip rather than the query.
func describeQueryColumns(ctx context.Context, uri, query string, d dbdialect.Dialect) (map[string]sqlColumnKind, error) {
	driver, dsn, err := DetectDriver(uri)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", driver, err)
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, d.ProbeColumnsSQL(query))
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
		out[ct.Name()] = sqlColumnKind(d.ClassifyType(ct.DatabaseTypeName()))
	}
	return out, rows.Err()
}
