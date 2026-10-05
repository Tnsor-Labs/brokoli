//go:build adbc

package engine

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/apache/arrow-adbc/go/adbc"
	flightsql "github.com/apache/arrow-adbc/go/adbc/driver/flightsql"
	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/arrow/memory"
)

// StreamFlightSQLToArrowIPC executes query through the Go Flight SQL ADBC
// driver and writes its record batches directly to an Arrow IPC stream.
//
// It deliberately has no common.DataSet step: Flight SQL has already supplied
// columnar Arrow batches, and converting them to row maps before writing an
// Arrow artifact would discard the benefit this prototype is measuring.
func StreamFlightSQLToArrowIPC(ctx context.Context, uri, query string, headers map[string]string, out io.Writer) (columns []string, rows int64, err error) {
	var writer *ipc.Writer
	defer func() {
		if writer != nil {
			err = errors.Join(err, writer.Close())
		}
	}()
	return ConsumeFlightSQL(ctx, uri, query, headers, func(record arrow.RecordBatch) error {
		if writer == nil {
			// Flight SQL servers may add metadata to the schema on the first
			// record batch. The IPC writer requires an exact schema match.
			writer = ipc.NewWriter(out, ipc.WithSchema(record.Schema()))
		}
		if err := writer.Write(record); err != nil {
			return fmt.Errorf("write Flight SQL Arrow batch: %w", err)
		}
		return nil
	})
}

// ConsumeFlightSQL executes query and hands each Arrow record batch to consume.
// It preserves the driver-owned record batch for the duration of the callback;
// consumers that retain it after returning must call Retain first.
func ConsumeFlightSQL(ctx context.Context, uri, query string, headers map[string]string, consume func(arrow.RecordBatch) error) (columns []string, rows int64, err error) {
	if consume == nil {
		return nil, 0, fmt.Errorf("consume Flight SQL: nil record consumer")
	}
	err = withFlightSQLReader(ctx, uri, query, headers, func(reader array.RecordReader) error {
		for _, field := range reader.Schema().Fields() {
			columns = append(columns, field.Name)
		}
		for reader.Next() {
			if err := ctx.Err(); err != nil {
				return err
			}
			record := reader.RecordBatch()
			if err := consume(record); err != nil {
				return err
			}
			rows += record.NumRows()
		}
		if err := reader.Err(); err != nil {
			return fmt.Errorf("read Flight SQL Arrow batches: %w", err)
		}
		return nil
	})
	return columns, rows, err
}

// withFlightSQLReader holds every ADBC resource open until consume returns.
// A native consumer such as DuckDB may pull the reader synchronously through
// ArrowArrayStream, so returning the reader without this ownership boundary
// would release its statement and connection too early.
func withFlightSQLReader(ctx context.Context, uri, query string, headers map[string]string, consume func(array.RecordReader) error) (err error) {
	options := map[string]string{adbc.OptionKeyURI: uri}
	for name, value := range headers {
		options[flightsql.OptionRPCCallHeaderPrefix+name] = value
	}

	db, err := flightsql.NewDriver(memory.DefaultAllocator).NewDatabase(options)
	if err != nil {
		return fmt.Errorf("open Flight SQL database: %w", err)
	}
	defer func() { err = errors.Join(err, db.Close()) }()

	conn, err := db.Open(ctx)
	if err != nil {
		return fmt.Errorf("open Flight SQL connection: %w", err)
	}
	defer func() { err = errors.Join(err, conn.Close()) }()

	stmt, err := conn.NewStatement()
	if err != nil {
		return fmt.Errorf("create Flight SQL statement: %w", err)
	}
	defer func() { err = errors.Join(err, stmt.Close()) }()
	if err := stmt.SetSqlQuery(query); err != nil {
		return fmt.Errorf("set Flight SQL query: %w", err)
	}

	reader, _, err := stmt.ExecuteQuery(ctx)
	if err != nil {
		return fmt.Errorf("execute Flight SQL query: %w", err)
	}
	defer reader.Release()

	return consume(reader)
}
