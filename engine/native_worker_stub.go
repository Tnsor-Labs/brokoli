//go:build !adbc || !cgo

package engine

import (
	"context"
	"io"
)

// NativeADBCWorkerEnabled reports whether this binary can load a native ADBC
// driver in its isolated child process.
func NativeADBCWorkerEnabled() bool { return false }

func streamNativeADBC(context.Context, string, string, NativeADBCRequest, io.Writer) (int64, []string, error) {
	return 0, nil, ErrNativeADBCUnavailable
}
