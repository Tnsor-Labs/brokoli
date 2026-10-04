//go:build adbc && duckdb && cgo

package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/apache/arrow-adbc/go/adbc"
	"github.com/apache/arrow-adbc/go/adbc/drivermgr"
	"github.com/apache/arrow-go/v18/arrow/array"
)

// IngestFlightSQLIntoDuckDB streams a Flight SQL result into DuckDB through
// ArrowArrayStream. The result is never converted to common.DataSet, Arrow IPC
// files, or Go row maps.
func IngestFlightSQLIntoDuckDB(ctx context.Context, uri, query string, headers map[string]string, duckdbLibrary, duckdbPath, table string) (rows int64, err error) {
	if duckdbLibrary == "" {
		return 0, fmt.Errorf("DuckDB ADBC library path is required")
	}
	if table == "" {
		return 0, fmt.Errorf("DuckDB target table is required")
	}
	options := map[string]string{"driver": duckdbLibrary, "entrypoint": "duckdb_adbc_init"}
	if duckdbPath != "" {
		options["path"] = duckdbPath
	}
	var driver drivermgr.Driver
	db, err := driver.NewDatabase(options)
	if err != nil {
		return 0, fmt.Errorf("open DuckDB ADBC driver: %w", err)
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	conn, err := db.Open(ctx)
	if err != nil {
		return 0, fmt.Errorf("open DuckDB ADBC connection: %w", err)
	}
	defer func() { err = errors.Join(err, conn.Close()) }()

	err = withFlightSQLReader(ctx, uri, query, headers, func(reader array.RecordReader) error {
		var ingestErr error
		rows, ingestErr = adbc.IngestStream(ctx, conn, reader, table, adbc.OptionValueIngestModeCreate, adbc.IngestStreamOptions{})
		return ingestErr
	})
	if err != nil {
		return 0, fmt.Errorf("ingest Flight SQL stream into DuckDB: %w", err)
	}
	return rows, nil
}
