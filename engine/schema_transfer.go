package engine

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/Tnsor-Labs/brokoli/pkg/dbdialect"
)

// Carrying a source table's real column types to a destination, instead of
// guessing them from a sample of values (Tnsor-Labs/brokoli#360).
//
// inferColumnType renders each sampled value with %v and tries to parse it,
// which cannot recover what the source actually declared. A Postgres bigint
// whose sample happens to fit in an int32 produced a MySQL INT, and the
// migration then failed on the first row above 2^31 -- after the destination
// table had already been created wrong. An exact numeric became a float, and
// a timestamptz became text with its zone applied and then discarded.
//
// The source knows all of this and is already asked: describeQueryColumns
// probes the query for the pushdown compiler. This path asks the same
// question and keeps the answer.

// sourceColumnTypes reads the canonical column types of a query without
// running it, using the source dialect to interpret its own type names.
//
// Returns ok=false when the source backend has no type reader, which is the
// absent-capability-degrades path: the caller falls back to value inference
// rather than failing, because that is what a file source has always done.
func sourceColumnTypes(ctx context.Context, uri, query string) (map[string]dbdialect.ColumnType, []string, bool) {
	d, dOK := dbdialect.For(dialectForURI(uri))
	if !dOK {
		return nil, nil, false
	}
	reader, rOK := d.(dbdialect.TypeReader)
	if !rOK {
		return nil, nil, false
	}

	driver, dsn, err := DetectDriver(uri)
	if err != nil {
		return nil, nil, false
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, nil, false
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, d.ProbeColumnsSQL(query))
	if err != nil {
		return nil, nil, false
	}
	defer rows.Close()

	types, err := rows.ColumnTypes()
	if err != nil {
		return nil, nil, false
	}
	out := make(map[string]dbdialect.ColumnType, len(types))
	order := make([]string, 0, len(types))
	for _, ct := range types {
		precision, scale, precisionOK := ct.DecimalSize()
		length, lengthOK := ct.Length()
		nullable, nullableKnown := ct.Nullable()
		if !nullableKnown {
			// The permissive answer: a NOT NULL the source did not
			// actually have would fail the load.
			nullable = true
		}
		out[ct.Name()] = reader.CanonicalType(
			ct.DatabaseTypeName(), precision, scale, precisionOK, length, lengthOK, nullable)
		order = append(order, ct.Name())
	}
	return out, order, len(out) > 0
}

// createTableFromSourceTypes renders CREATE TABLE for the destination from
// the source's canonical types, or reports why it cannot.
//
// A column the destination has no faithful type for is named in the error
// rather than substituted with text. A migration that half-works is worse
// than one that says which column it cannot carry: the first is discovered
// in the data, the second before anything moves.
func createTableFromSourceTypes(
	destDialect string,
	table string,
	columns []string,
	types map[string]dbdialect.ColumnType,
) (string, error) {
	d, ok := dbdialect.For(destDialect)
	if !ok {
		return "", fmt.Errorf("no dialect registered for %q", destDialect)
	}
	renderer, ok := d.(dbdialect.TypeRenderer)
	if !ok {
		return "", fmt.Errorf("dialect %q cannot render DDL types", destDialect)
	}

	var sb strings.Builder
	sb.WriteString("CREATE TABLE " + d.QuoteQualifiedIdent(table) + " (\n")
	for i, col := range columns {
		ct, known := types[col]
		if !known {
			return "", fmt.Errorf(
				"column %q has no type from the source, so the destination type would be a guess", col)
		}
		sqlType, ok := renderer.DDLType(ct)
		if !ok {
			return "", fmt.Errorf(
				"column %q is %s at the source and %s has no type that holds it without losing information; "+
					"create the destination table yourself and choose how to store it",
				col, ct, destDialect)
		}
		sb.WriteString("  " + d.QuoteIdent(col) + " " + sqlType)
		if !ct.Nullable {
			sb.WriteString(" NOT NULL")
		}
		if i < len(columns)-1 {
			sb.WriteString(",")
		}
		sb.WriteString("\n")
	}
	sb.WriteString(");")
	return sb.String(), nil
}
