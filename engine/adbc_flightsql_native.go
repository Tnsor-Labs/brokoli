//go:build adbc && cgo

package engine

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/apache/arrow-adbc/go/adbc"
	"github.com/apache/arrow-adbc/go/adbc/drivermgr"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/ipc"
)

const nativeFlightSQLEntrypoint = "AdbcDriverFlightSQLInit"

// NativeADBCWorkerEnabled reports whether this binary can load a native ADBC
// driver in its isolated child process.
func NativeADBCWorkerEnabled() bool { return true }

// ConsumeNativeFlightSQL executes query through a native Flight SQL ADBC
// library in this process. Its reader is valid only for the duration of
// consume. It exists for benchmarks comparing driver paths; pipelines run
// native drivers only through the isolated worker.
func ConsumeNativeFlightSQL(ctx context.Context, library, uri, query string, headers map[string]string, consume func(array.RecordReader) error) (rows int64, err error) {
	if library == "" {
		return 0, fmt.Errorf("native Flight SQL ADBC library path is required")
	}
	if consume == nil {
		return 0, fmt.Errorf("consume native Flight SQL: nil record consumer")
	}
	options := make(map[string]string, len(headers))
	for name, value := range headers {
		options["adbc.flight.sql.rpc.call_header."+name] = value
	}
	err = withNativeADBCReader(ctx, library, nativeFlightSQLEntrypoint, uri, query, options, func(reader array.RecordReader) error {
		for reader.Next() {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := consume(reader); err != nil {
				return err
			}
			rows += reader.RecordBatch().NumRows()
		}
		return atStage(nativeStageRead, reader.Err())
	})
	return rows, err
}

// streamNativeADBC runs request through the verified library and writes the
// result to out as an Arrow IPC stream, schema included even when empty.
func streamNativeADBC(ctx context.Context, library, entrypoint string, request NativeADBCRequest, out io.Writer) (rows int64, columns []string, err error) {
	err = withNativeADBCReader(ctx, library, entrypoint, request.URI, request.Query, request.Options, func(reader array.RecordReader) error {
		for _, field := range reader.Schema().Fields() {
			columns = append(columns, field.Name)
		}
		writer := ipc.NewWriter(out, ipc.WithSchema(reader.Schema()))
		for reader.Next() {
			if err := ctx.Err(); err != nil {
				_ = writer.Close() // The cancellation is the error worth reporting.
				return err
			}
			if err := writer.Write(reader.RecordBatch()); err != nil {
				_ = writer.Close() // The write failure is the error worth reporting.
				return atStage(nativeStageWrite, err)
			}
			rows += reader.RecordBatch().NumRows()
		}
		if err := reader.Err(); err != nil {
			_ = writer.Close() // The read failure is the error worth reporting.
			return atStage(nativeStageRead, err)
		}
		return atStage(nativeStageWrite, writer.Close())
	})
	return rows, columns, err
}

// withNativeADBCReader loads library through drivermgr and runs one query.
// driver, entrypoint and uri are set last, so no option can replace them.
func withNativeADBCReader(ctx context.Context, library, entrypoint, uri, query string, extra map[string]string, consume func(array.RecordReader) error) (err error) {
	options := make(map[string]string, len(extra)+3)
	for name, value := range extra {
		options[name] = value
	}
	options["driver"] = library
	options["entrypoint"] = entrypoint
	options[adbc.OptionKeyURI] = uri

	var driver drivermgr.Driver
	db, err := driver.NewDatabase(options)
	if err != nil {
		return atStage(nativeStageOpen, err)
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	conn, err := db.Open(ctx)
	if err != nil {
		return atStage(nativeStageConnect, err)
	}
	defer func() { err = errors.Join(err, conn.Close()) }()
	stmt, err := conn.NewStatement()
	if err != nil {
		return atStage(nativeStageQuery, err)
	}
	defer func() { err = errors.Join(err, stmt.Close()) }()
	if err := stmt.SetSqlQuery(query); err != nil {
		return atStage(nativeStageQuery, err)
	}
	reader, _, err := stmt.ExecuteQuery(ctx)
	if err != nil {
		return atStage(nativeStageQuery, err)
	}
	defer reader.Release()
	return consume(reader)
}
