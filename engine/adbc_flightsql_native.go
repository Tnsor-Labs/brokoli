//go:build adbc && cgo

package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/apache/arrow-adbc/go/adbc"
	"github.com/apache/arrow-adbc/go/adbc/drivermgr"
	"github.com/apache/arrow-go/v18/arrow/array"
)

const nativeFlightSQLEntrypoint = "AdbcDriverFlightSQLInit"

// ConsumeNativeFlightSQL executes query through the native Flight SQL ADBC
// driver. Its reader is valid only for the duration of consume.
func ConsumeNativeFlightSQL(ctx context.Context, library, uri, query string, headers map[string]string, consume func(array.RecordReader) error) (rows int64, err error) {
	if library == "" {
		return 0, fmt.Errorf("native Flight SQL ADBC library path is required")
	}
	if consume == nil {
		return 0, fmt.Errorf("consume native Flight SQL: nil record consumer")
	}

	options := map[string]string{
		"driver":          library,
		"entrypoint":      nativeFlightSQLEntrypoint,
		adbc.OptionKeyURI: uri,
	}
	for name, value := range headers {
		options["adbc.flight.sql.rpc.call_header."+name] = value
	}

	var driver drivermgr.Driver
	db, err := driver.NewDatabase(options)
	if err != nil {
		return 0, fmt.Errorf("open native Flight SQL database: %w", err)
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	conn, err := db.Open(ctx)
	if err != nil {
		return 0, fmt.Errorf("open native Flight SQL connection: %w", err)
	}
	defer func() { err = errors.Join(err, conn.Close()) }()
	stmt, err := conn.NewStatement()
	if err != nil {
		return 0, fmt.Errorf("create native Flight SQL statement: %w", err)
	}
	defer func() { err = errors.Join(err, stmt.Close()) }()
	if err := stmt.SetSqlQuery(query); err != nil {
		return 0, fmt.Errorf("set native Flight SQL query: %w", err)
	}

	reader, _, err := stmt.ExecuteQuery(ctx)
	if err != nil {
		return 0, fmt.Errorf("execute native Flight SQL query: %w", err)
	}
	defer reader.Release()
	for reader.Next() {
		if err := ctx.Err(); err != nil {
			return rows, err
		}
		if err := consume(reader); err != nil {
			return rows, err
		}
		rows += reader.RecordBatch().NumRows()
	}
	if err := reader.Err(); err != nil {
		return rows, fmt.Errorf("read native Flight SQL Arrow batches: %w", err)
	}
	return rows, nil
}
