//go:build !adbc || !cgo

package engine

import (
	"context"
	"io"
)

// NativeFlightSQLWorkerEnabled reports whether this binary can load the
// optional ADBC driver in its isolated child process.
func NativeFlightSQLWorkerEnabled() bool { return false }

func NativeADBCWorkerEnabled() bool { return false }

func StreamNativeFlightSQLToArrowIPC(context.Context, string, string, string, map[string]string, io.Writer) (int64, error) {
	return 0, ErrNativeFlightSQLUnavailable
}

func runNativeADBCWorker(context.Context, NativeADBCRequest) (NativeADBCResponse, error) {
	return NativeADBCResponse{}, ErrNativeADBCUnavailable
}

func runNativeFlightSQLWorker(context.Context, NativeFlightSQLRequest) (NativeFlightSQLResponse, error) {
	return NativeFlightSQLResponse{}, ErrNativeFlightSQLUnavailable
}
