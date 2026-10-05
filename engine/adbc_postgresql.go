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

const nativePostgreSQLEntrypoint = "AdbcDriverPostgresqlInit"

// ConsumeNativePostgreSQL executes query through the native PostgreSQL ADBC
// driver. Its reader is valid only while consume is running.
func ConsumeNativePostgreSQL(ctx context.Context, library, uri, query string, consume func(array.RecordReader) error) (rows int64, err error) {
	if library == "" {
		return 0, fmt.Errorf("native PostgreSQL ADBC library path is required")
	}
	if consume == nil {
		return 0, fmt.Errorf("consume native PostgreSQL: nil record consumer")
	}

	var driver drivermgr.Driver
	db, err := driver.NewDatabase(map[string]string{
		"driver":          library,
		"entrypoint":      nativePostgreSQLEntrypoint,
		adbc.OptionKeyURI: uri,
	})
	if err != nil {
		return 0, fmt.Errorf("open native PostgreSQL database: %w", err)
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	conn, err := db.Open(ctx)
	if err != nil {
		return 0, fmt.Errorf("open native PostgreSQL connection: %w", err)
	}
	defer func() { err = errors.Join(err, conn.Close()) }()
	stmt, err := conn.NewStatement()
	if err != nil {
		return 0, fmt.Errorf("create native PostgreSQL statement: %w", err)
	}
	defer func() { err = errors.Join(err, stmt.Close()) }()
	if err := stmt.SetSqlQuery(query); err != nil {
		return 0, fmt.Errorf("set native PostgreSQL query: %w", err)
	}

	reader, _, err := stmt.ExecuteQuery(ctx)
	if err != nil {
		return 0, fmt.Errorf("execute native PostgreSQL query: %w", err)
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
		return rows, fmt.Errorf("read native PostgreSQL Arrow batches: %w", err)
	}
	return rows, nil
}
