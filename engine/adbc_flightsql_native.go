//go:build adbc && cgo

package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/apache/arrow-adbc/go/adbc"
	"github.com/apache/arrow-adbc/go/adbc/drivermgr"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/ipc"
)

// NativeFlightSQLWorkerEnabled reports whether this binary can load the
// optional ADBC driver in its isolated child process.
func NativeFlightSQLWorkerEnabled() bool { return true }

// NativeADBCWorkerEnabled reports whether this binary can load a manifest
// selected ADBC driver in its isolated child process.
func NativeADBCWorkerEnabled() bool { return true }

// ConsumeNativeFlightSQL executes query through the native Flight SQL ADBC
// driver. Its reader is valid only for the duration of consume.
func ConsumeNativeFlightSQL(ctx context.Context, library, uri, query string, headers map[string]string, consume func(array.RecordReader) error) (rows int64, err error) {
	if library == "" {
		return 0, fmt.Errorf("native Flight SQL ADBC library path is required")
	}
	if consume == nil {
		return 0, fmt.Errorf("consume native Flight SQL: nil record consumer")
	}

	err = withNativeFlightSQLReader(ctx, library, uri, query, headers, func(reader array.RecordReader) error {
		for reader.Next() {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := consume(reader); err != nil {
				return err
			}
			rows += reader.RecordBatch().NumRows()
		}
		if err := reader.Err(); err != nil {
			return fmt.Errorf("read native Flight SQL Arrow batches: %w", err)
		}
		return nil
	})
	return rows, err
}

func withNativeFlightSQLReader(ctx context.Context, library, uri, query string, headers map[string]string, consume func(array.RecordReader) error) (err error) {
	options := make(map[string]string, len(headers))
	for name, value := range headers {
		options["adbc.flight.sql.rpc.call_header."+name] = value
	}
	return withNativeADBCReader(ctx, NativeADBCRequest{Library: library, Entrypoint: nativeFlightSQLEntrypoint, URI: uri, Query: query, Options: options}, consume)
}

// withNativeADBCReader loads only the manifest-selected library and entrypoint
// through drivermgr; it deliberately has no database/sql fallback.
func withNativeADBCReader(ctx context.Context, request NativeADBCRequest, consume func(array.RecordReader) error) (err error) {
	options := make(map[string]string, len(request.Options)+3)
	for name, value := range request.Options {
		options[name] = value
	}
	// These are selected from the verified manifest, never caller options.
	options["driver"] = request.Library
	options["entrypoint"] = request.Entrypoint
	options[adbc.OptionKeyURI] = request.URI

	var driver drivermgr.Driver
	db, err := driver.NewDatabase(options)
	if err != nil {
		return fmt.Errorf("open native Flight SQL database: %w", err)
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	conn, err := db.Open(ctx)
	if err != nil {
		return fmt.Errorf("open native Flight SQL connection: %w", err)
	}
	defer func() { err = errors.Join(err, conn.Close()) }()
	stmt, err := conn.NewStatement()
	if err != nil {
		return fmt.Errorf("create native Flight SQL statement: %w", err)
	}
	defer func() { err = errors.Join(err, stmt.Close()) }()
	if err := stmt.SetSqlQuery(request.Query); err != nil {
		return fmt.Errorf("set native Flight SQL query: %w", err)
	}

	reader, _, err := stmt.ExecuteQuery(ctx)
	if err != nil {
		return fmt.Errorf("execute native Flight SQL query: %w", err)
	}
	defer reader.Release()
	return consume(reader)
}

// StreamNativeFlightSQLToArrowIPC executes a native Flight SQL query and
// writes an Arrow IPC stream, including a schema for an empty result.
func StreamNativeFlightSQLToArrowIPC(ctx context.Context, library, uri, query string, headers map[string]string, out io.Writer) (rows int64, err error) {
	if out == nil {
		return 0, errors.New("write native Flight SQL Arrow IPC: nil writer")
	}
	err = withNativeFlightSQLReader(ctx, library, uri, query, headers, func(reader array.RecordReader) error {
		writer := ipc.NewWriter(out, ipc.WithSchema(reader.Schema()))
		defer writer.Close()
		for reader.Next() {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := writer.Write(reader.RecordBatch()); err != nil {
				return fmt.Errorf("write native Flight SQL Arrow batch: %w", err)
			}
			rows += reader.RecordBatch().NumRows()
		}
		if err := reader.Err(); err != nil {
			return fmt.Errorf("read native Flight SQL Arrow batches: %w", err)
		}
		if err := writer.Close(); err != nil {
			return fmt.Errorf("close native Flight SQL Arrow IPC: %w", err)
		}
		return nil
	})
	return rows, err
}

func runNativeADBCWorker(ctx context.Context, request NativeADBCRequest) (NativeADBCResponse, error) {
	f, err := os.OpenFile(request.OutputPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return NativeADBCResponse{}, fmt.Errorf("create Arrow IPC output: %w", err)
	}
	rows := int64(0)
	writer := (*ipc.Writer)(nil)
	err = withNativeADBCReader(ctx, request, func(reader array.RecordReader) error {
		writer = ipc.NewWriter(f, ipc.WithSchema(reader.Schema()))
		for reader.Next() {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := writer.Write(reader.RecordBatch()); err != nil {
				return fmt.Errorf("write native Flight SQL Arrow batch: %w", err)
			}
			rows += reader.RecordBatch().NumRows()
		}
		if err := reader.Err(); err != nil {
			return fmt.Errorf("read native Flight SQL Arrow batches: %w", err)
		}
		return nil
	})
	if err != nil {
		f.Close()
		return NativeADBCResponse{}, err
	}
	if writer == nil {
		f.Close()
		return NativeADBCResponse{}, errors.New("native ADBC driver returned no schema")
	}
	if err := writer.Close(); err != nil {
		f.Close()
		return NativeADBCResponse{}, fmt.Errorf("close Arrow IPC output: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return NativeADBCResponse{}, fmt.Errorf("sync Arrow IPC output: %w", err)
	}
	if err := f.Close(); err != nil {
		return NativeADBCResponse{}, fmt.Errorf("close Arrow IPC output: %w", err)
	}
	f, err = os.Open(request.OutputPath)
	if err != nil {
		return NativeADBCResponse{}, fmt.Errorf("open Arrow IPC output: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return NativeADBCResponse{}, fmt.Errorf("stat Arrow IPC output: %w", err)
	}
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return NativeADBCResponse{}, fmt.Errorf("checksum Arrow IPC output: %w", err)
	}
	return NativeADBCResponse{Rows: rows, OutputSize: info.Size(), OutputSHA256: hex.EncodeToString(sum.Sum(nil))}, nil
}

func runNativeFlightSQLWorker(ctx context.Context, request NativeFlightSQLRequest) (NativeFlightSQLResponse, error) {
	return runNativeADBCWorker(ctx, request.nativeADBCRequest())
}
