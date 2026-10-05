package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	nativeWorkerMaxRequestBytes  = 1 << 20
	nativeWorkerMaxResponseBytes = 64 << 10
	nativeWorkerMaxHeaders       = 128
	nativeWorkerMaxHeaderBytes   = 16 << 10
)

// ErrNativeFlightSQLUnavailable reports that this executable was built without
// the native ADBC Flight SQL worker capability.
var ErrNativeFlightSQLUnavailable = errors.New("native Flight SQL worker is unavailable in this build")

// NativeFlightSQLRequest is the one-shot input to an isolated Flight SQL
// worker. Credentials belong in Headers and are sent only over stdin.
type NativeFlightSQLRequest struct {
	Library    string            `json:"library"`
	URI        string            `json:"uri"`
	Query      string            `json:"query"`
	Headers    map[string]string `json:"headers,omitempty"`
	OutputPath string            `json:"output_path"`
}

// NativeFlightSQLResponse describes the Arrow IPC file written by a worker.
// Error is deliberately a stable, credential-free classification.
type NativeFlightSQLResponse struct {
	Rows         int64  `json:"rows,omitempty"`
	OutputSize   int64  `json:"output_size,omitempty"`
	OutputSHA256 string `json:"output_sha256,omitempty"`
	Error        string `json:"error,omitempty"`
}

func readNativeFlightSQLRequest(r io.Reader) (NativeFlightSQLRequest, error) {
	var request NativeFlightSQLRequest
	if err := decodeNativeWorkerJSON(r, nativeWorkerMaxRequestBytes, &request); err != nil {
		return NativeFlightSQLRequest{}, fmt.Errorf("decode native worker request: %w", err)
	}
	if err := request.validate(); err != nil {
		return NativeFlightSQLRequest{}, err
	}
	return request, nil
}

func writeNativeFlightSQLResponse(w io.Writer, response NativeFlightSQLResponse) error {
	if err := json.NewEncoder(w).Encode(response); err != nil {
		return fmt.Errorf("encode native worker response: %w", err)
	}
	return nil
}

func readNativeFlightSQLResponse(r io.Reader) (NativeFlightSQLResponse, error) {
	var response NativeFlightSQLResponse
	if err := decodeNativeWorkerJSON(r, nativeWorkerMaxResponseBytes, &response); err != nil {
		return NativeFlightSQLResponse{}, fmt.Errorf("decode native worker response: %w", err)
	}
	return response, nil
}

func decodeNativeWorkerJSON(r io.Reader, limit int64, value any) error {
	limited := &nativeWorkerCountingReader{r: io.LimitReader(r, limit+1)}
	decoder := json.NewDecoder(limited)
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("expected one JSON value")
		}
		return err
	}
	if limited.n > limit {
		return errors.New("JSON value exceeds limit")
	}
	return nil
}

type nativeWorkerCountingReader struct {
	r io.Reader
	n int64
}

func (r *nativeWorkerCountingReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.n += int64(n)
	return n, err
}

func (r NativeFlightSQLRequest) validate() error {
	if r.Library == "" || r.URI == "" || r.Query == "" || r.OutputPath == "" {
		return errors.New("native worker request is incomplete")
	}
	if len(r.Headers) > nativeWorkerMaxHeaders {
		return errors.New("native worker request has too many headers")
	}
	for name, value := range r.Headers {
		if name == "" || len(name)+len(value) > nativeWorkerMaxHeaderBytes {
			return errors.New("native worker request has an invalid header")
		}
	}
	return nil
}
