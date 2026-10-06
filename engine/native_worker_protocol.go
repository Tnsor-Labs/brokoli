package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/drivers"
)

// The isolated native worker protocol. The parent starts `worker task` and
// talks to it over three channels, none of them argv or the environment:
//
//	stdin  one NativeADBCRequest (JSON), then EOF
//	fd 3   the result as an Arrow IPC stream
//	fd 4   one NativeADBCResult (JSON), written last
//
// stdout and stderr belong to the driver: native libraries print, and a
// protocol on stdout would be corrupted by the first diagnostic a driver
// wrote there. The parent keeps a bounded tail of both for error reports.
const (
	nativeWorkerMaxRequestBytes = 1 << 20
	nativeWorkerMaxResultBytes  = 64 << 10
	nativeWorkerMaxOptions      = 128
	nativeWorkerMaxOptionBytes  = 16 << 10

	// nativeWorkerDataFD and nativeWorkerResultFD are the child's view of
	// cmd.ExtraFiles[0] and [1].
	nativeWorkerDataFD   = 3
	nativeWorkerResultFD = 4
)

// ErrNativeADBCUnavailable reports that this executable was built without
// the optional isolated native ADBC worker capability.
var ErrNativeADBCUnavailable = errors.New("this Brokoli build cannot load native ADBC drivers; run it on a worker built with native driver support")

// NativeADBCRequest is one query for the isolated worker. It names the
// driver by its pinned identity, never by a path: the worker looks the
// identity up in its own driver directory and verifies the library's digest
// immediately before loading it, so nothing a pipeline or connection can set
// chooses which code is loaded.
type NativeADBCRequest struct {
	Driver  models.DriverIdentity `json:"driver"`
	URI     string                `json:"uri"`
	Query   string                `json:"query"`
	Options map[string]string     `json:"options,omitempty"`

	// secrets are values to scrub from any error text before it leaves the
	// parent: credentials can appear in a driver's message or its output.
	secrets []string
}

// NativeADBCResult reports a finished query. On failure Stage names the step
// that failed and Error carries the driver's message, which the parent
// scrubs of credentials before reporting it.
type NativeADBCResult struct {
	Rows    int64    `json:"rows"`
	Columns []string `json:"columns,omitempty"`
	Stage   string   `json:"stage,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// Stages a native query moves through. A failure names the one it stopped at,
// so "the driver could not be loaded" and "the server refused the query" are
// different messages.
const (
	nativeStageRequest = "read request"
	nativeStageDriver  = "load driver"
	nativeStageOpen    = "open database"
	nativeStageConnect = "connect"
	nativeStageQuery   = "execute query"
	nativeStageRead    = "read results"
	nativeStageWrite   = "write results"
)

// reservedNativeOptions are set by the worker from the verified manifest and
// the connection; an option of the same name would replace them.
var reservedNativeOptions = map[string]bool{"driver": true, "entrypoint": true, "uri": true}

func (r NativeADBCRequest) validate() error {
	if !drivers.ValidIdentity(r.Driver) {
		return errors.New("native worker request has no valid driver identity")
	}
	if r.URI == "" || r.Query == "" {
		return errors.New("native worker request needs a URI and a query")
	}
	if len(r.Options) > nativeWorkerMaxOptions {
		return errors.New("native worker request has too many options")
	}
	for name, value := range r.Options {
		if name == "" || len(name)+len(value) > nativeWorkerMaxOptionBytes {
			return errors.New("native worker request has an invalid option")
		}
		if reservedNativeOptions[strings.ToLower(name)] {
			return fmt.Errorf("native worker option %q is set by the worker, not the connection", name)
		}
	}
	return nil
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

func writeNativeADBCResult(w io.Writer, result NativeADBCResult) error {
	if err := json.NewEncoder(w).Encode(result); err != nil {
		return fmt.Errorf("encode native worker result: %w", err)
	}
	return nil
}

func readNativeADBCResult(r io.Reader) (NativeADBCResult, error) {
	var result NativeADBCResult
	if err := decodeNativeWorkerJSON(r, nativeWorkerMaxResultBytes, &result); err != nil {
		return NativeADBCResult{}, fmt.Errorf("decode native worker result: %w", err)
	}
	return result, nil
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

// NativeADBCError is a failed native query, with the stage it failed at, the
// driver's message, and the end of what the driver printed. Every part has
// been scrubbed of the request's credentials.
type NativeADBCError struct {
	Stage   string
	Message string
	Output  string
}

func (e *NativeADBCError) Error() string {
	var b strings.Builder
	b.WriteString("native ADBC ")
	if e.Stage != "" {
		b.WriteString(e.Stage)
		b.WriteString(" failed")
	} else {
		b.WriteString("worker failed")
	}
	if e.Message != "" {
		b.WriteString(": ")
		b.WriteString(e.Message)
	}
	if e.Output != "" {
		b.WriteString(" (driver output: ")
		b.WriteString(e.Output)
		b.WriteString(")")
	}
	return b.String()
}

// scrubSecrets replaces every occurrence of a secret, and of each
// whitespace-separated part of one, in text. Parts matter because a
// credential is often sent as "Bearer <token>" or "Basic <base64>" and a
// driver may echo only the token. Values under eight bytes are left alone,
// as the run redaction does: masking every "1" or "id" would destroy the
// message without protecting anything.
func scrubSecrets(text string, secrets []string) string {
	const minSecret = 8
	var needles []string
	for _, secret := range secrets {
		if len(secret) >= minSecret {
			needles = append(needles, secret)
		}
		for _, part := range strings.Fields(secret) {
			if len(part) >= minSecret && part != secret {
				needles = append(needles, part)
			}
		}
	}
	// Longest first, so a whole secret is replaced before any part of it.
	sort.Slice(needles, func(i, j int) bool { return len(needles[i]) > len(needles[j]) })
	for _, needle := range needles {
		text = strings.ReplaceAll(text, needle, "[redacted]")
	}
	return text
}
