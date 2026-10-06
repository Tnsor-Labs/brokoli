package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	nativeFlightSQLEntrypoint    = "AdbcDriverFlightSQLInit"
	nativeWorkerMaxRequestBytes  = 1 << 20
	nativeWorkerMaxResponseBytes = 64 << 10
	nativeWorkerMaxHeaders       = 128
	nativeWorkerMaxHeaderBytes   = 16 << 10
	nativeWorkerMaxOptions       = 128
	nativeWorkerMaxOptionBytes   = 16 << 10
)

// ErrNativeFlightSQLUnavailable reports that this executable was built without
// the native ADBC Flight SQL worker capability.
var ErrNativeFlightSQLUnavailable = errors.New("native Flight SQL worker is unavailable in this build")

// ErrNativeADBCUnavailable reports that this executable was built without the
// optional isolated native ADBC worker capability.
var ErrNativeADBCUnavailable = errors.New("native ADBC worker is unavailable in this build")

// NativeFlightSQLRequest is the one-shot input to an isolated Flight SQL
// worker. Credentials belong in Headers and are sent only over stdin.
type NativeADBCRequest struct {
	Library    string            `json:"library"`
	Entrypoint string            `json:"entrypoint"`
	URI        string            `json:"uri"`
	Query      string            `json:"query"`
	Options    map[string]string `json:"options,omitempty"`
	OutputPath string            `json:"output_path"`
}

// NativeFlightSQLResponse describes the Arrow IPC file written by a worker.
// Error is deliberately a stable, credential-free classification.
type NativeADBCResponse struct {
	Rows         int64  `json:"rows,omitempty"`
	OutputSize   int64  `json:"output_size,omitempty"`
	OutputSHA256 string `json:"output_sha256,omitempty"`
	Error        string `json:"error,omitempty"`
}

// NativeFlightSQLRequest remains an adapter for callers that use Flight SQL
// headers. New native worker callers should use NativeADBCRequest.
type NativeFlightSQLRequest struct {
	Library    string            `json:"library"`
	URI        string            `json:"uri"`
	Query      string            `json:"query"`
	Headers    map[string]string `json:"headers,omitempty"`
	OutputPath string            `json:"output_path"`
}

type NativeFlightSQLResponse = NativeADBCResponse

func (r NativeFlightSQLRequest) nativeADBCRequest() NativeADBCRequest {
	options := make(map[string]string, len(r.Headers))
	for name, value := range r.Headers {
		options["adbc.flight.sql.rpc.call_header."+name] = value
	}
	return NativeADBCRequest{Library: r.Library, Entrypoint: "AdbcDriverFlightSQLInit", URI: r.URI, Query: r.Query, Options: options, OutputPath: r.OutputPath}
}

func readNativeADBCRequest(r io.Reader) (NativeADBCRequest, error) {
	var request NativeADBCRequest
	if err := decodeNativeWorkerJSON(r, nativeWorkerMaxRequestBytes, &request); err != nil {
		return NativeADBCRequest{}, fmt.Errorf("decode native worker request: %w", err)
	}
	if err := request.validate(); err != nil {
		return NativeADBCRequest{}, err
	}
	return request, nil
}

func writeNativeADBCResponse(w io.Writer, response NativeADBCResponse) error {
	if err := json.NewEncoder(w).Encode(response); err != nil {
		return fmt.Errorf("encode native worker response: %w", err)
	}
	return nil
}

func readNativeADBCResponse(r io.Reader) (NativeADBCResponse, error) {
	var response NativeADBCResponse
	if err := decodeNativeWorkerJSON(r, nativeWorkerMaxResponseBytes, &response); err != nil {
		return NativeADBCResponse{}, fmt.Errorf("decode native worker response: %w", err)
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

func (r NativeADBCRequest) validate() error {
	if r.Library == "" || r.Entrypoint == "" || r.URI == "" || r.Query == "" || r.OutputPath == "" {
		return errors.New("native worker request is incomplete")
	}
	if len(r.Options) > nativeWorkerMaxOptions {
		return errors.New("native worker request has too many options")
	}
	for name, value := range r.Options {
		if name == "" || len(name)+len(value) > nativeWorkerMaxOptionBytes {
			return errors.New("native worker request has an invalid option")
		}
	}
	return nil
}

func readNativeFlightSQLRequest(r io.Reader) (NativeFlightSQLRequest, error) {
	var request NativeFlightSQLRequest
	if err := decodeNativeWorkerJSON(r, nativeWorkerMaxRequestBytes, &request); err != nil {
		return NativeFlightSQLRequest{}, fmt.Errorf("decode native worker request: %w", err)
	}
	if err := request.nativeADBCRequest().validate(); err != nil {
		return NativeFlightSQLRequest{}, err
	}
	return request, nil
}
func writeNativeFlightSQLResponse(w io.Writer, response NativeFlightSQLResponse) error {
	return writeNativeADBCResponse(w, response)
}
func readNativeFlightSQLResponse(r io.Reader) (NativeFlightSQLResponse, error) {
	return readNativeADBCResponse(r)
}
