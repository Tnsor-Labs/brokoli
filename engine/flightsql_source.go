package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Tnsor-Labs/brokoli/pkg/artifact"
	"github.com/apache/arrow-go/v18/arrow/ipc"
)

// runFlightSQLSource writes the native child's already-verified Arrow stream
// into the normal blob store. It deliberately has no dataset/database/sql path.
func runFlightSQLSource(ctx context.Context, worker NativeFlightSQLRunner, outputs *nodeOutputs, library, uri, query string, headers map[string]string) (*artifact.DatasetRef, error) {
	return runNativeADBCSource(ctx, flightSQLRunnerAdapter{worker}, outputs, NativeFlightSQLRequest{Library: library, URI: uri, Query: query, Headers: headers}.nativeADBCRequest())
}

type flightSQLRunnerAdapter struct{ NativeFlightSQLRunner }

func (a flightSQLRunnerAdapter) RunNativeADBC(ctx context.Context, request NativeADBCRequest) (NativeADBCResponse, error) {
	if a.NativeFlightSQLRunner == nil {
		return NativeADBCResponse{}, fmt.Errorf("native ADBC source_db requires an enabled isolated native worker")
	}
	headers := make(map[string]string)
	for key, value := range request.Options {
		const prefix = "adbc.flight.sql.rpc.call_header."
		if len(key) > len(prefix) && key[:len(prefix)] == prefix {
			headers[key[len(prefix):]] = value
		}
	}
	return a.RunFlightSQL(ctx, NativeFlightSQLRequest{Library: request.Library, URI: request.URI, Query: request.Query, Headers: headers, OutputPath: request.OutputPath})
}

// runNativeADBCSource writes the native child's already-verified Arrow stream
// into the normal blob store without a database/sql path.
func runNativeADBCSource(ctx context.Context, worker NativeADBCRunner, outputs *nodeOutputs, request NativeADBCRequest) (*artifact.DatasetRef, error) {
	if worker == nil {
		return nil, fmt.Errorf("native ADBC source_db requires an enabled isolated native worker")
	}
	if !outputs.spillEnabled() {
		return nil, fmt.Errorf("native ADBC source_db requires a durable artifact store")
	}
	dir, err := os.MkdirTemp("", "brokoli-flightsql-")
	if err != nil {
		return nil, fmt.Errorf("create Flight SQL staging directory: %w", err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "output.arrow")
	request.OutputPath = path
	response, err := worker.RunNativeADBC(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("execute native ADBC: %w", err)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open native ADBC output: %w", err)
	}
	defer f.Close()
	reader, err := ipc.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("verify native ADBC Arrow IPC: %w", err)
	}
	columns := make([]string, len(reader.Schema().Fields()))
	for i, field := range reader.Schema().Fields() {
		columns[i] = field.Name
	}
	reader.Release()
	if _, err := f.Seek(0, 0); err != nil {
		return nil, fmt.Errorf("rewind native ADBC output: %w", err)
	}
	ref, err := outputs.blobs.Put(ctx, outputs.namespace, f, artifact.PutOptions{MediaType: artifact.MediaTypeArrowIPC})
	if err != nil {
		return nil, fmt.Errorf("store native ADBC Arrow IPC: %w", err)
	}
	return &artifact.DatasetRef{ArtifactRef: *ref, Format: artifact.FormatArrowIPC, Columns: columns, RowCount: response.Rows}, nil
}
