package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

var testDriverIdentity = models.DriverIdentity{Name: "flightsql", Version: "1.0.0", LibrarySHA256: strings.Repeat("ab", 32)}

func validNativeRequest() NativeADBCRequest {
	return NativeADBCRequest{Driver: testDriverIdentity, URI: "grpc+tcp://flight.example.com:32010", Query: "SELECT 1"}
}

func TestNativeADBCRequestValidation(t *testing.T) {
	if err := validNativeRequest().validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	cases := map[string]func(*NativeADBCRequest){
		"no identity":       func(r *NativeADBCRequest) { r.Driver = models.DriverIdentity{} },
		"path-like version": func(r *NativeADBCRequest) { r.Driver.Version = "../../lib" },
		"no query":          func(r *NativeADBCRequest) { r.Query = "" },
		"driver option":     func(r *NativeADBCRequest) { r.Options = map[string]string{"driver": "/tmp/evil.so"} },
		"entrypoint option": func(r *NativeADBCRequest) { r.Options = map[string]string{"Entrypoint": "Evil"} },
		"too many options": func(r *NativeADBCRequest) {
			r.Options = map[string]string{}
			for i := 0; i <= nativeWorkerMaxOptions; i++ {
				r.Options[fmt.Sprintf("o%d", i)] = "v"
			}
		},
	}
	for name, mutate := range cases {
		request := validNativeRequest()
		mutate(&request)
		if err := request.validate(); err == nil {
			t.Errorf("%s: validate accepted it", name)
		}
	}
}

func TestNativeWorkerProtocolRejectsMultipleRequests(t *testing.T) {
	if _, err := readNativeADBCRequest(strings.NewReader(`{"driver":{"name":"x"}} {}`)); err == nil {
		t.Fatal("readNativeADBCRequest accepted multiple JSON values")
	}
}

func TestScrubSecretsRemovesWholeValuesAndTheirParts(t *testing.T) {
	text := "auth failed for Basic dXNlcjpodW50ZXIy and token dXNlcjpodW50ZXIy; id=42"
	got := scrubSecrets(text, []string{"Basic dXNlcjpodW50ZXIy", "42"})
	if strings.Contains(got, "dXNlcjpodW50ZXIy") {
		t.Fatalf("scrubbed text still has the credential: %q", got)
	}
	if !strings.Contains(got, "id=42") {
		t.Fatalf("a short value was scrubbed: %q", got)
	}
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// In a build without driver support the child must say so, in words that
// name the build, not fail with something generic.
func TestNativeWorkerChildReportsAnUnsupportedBuild(t *testing.T) {
	if NativeADBCWorkerEnabled() {
		t.Skip("this build supports native drivers")
	}
	payload := `{"driver":{"name":"flightsql","version":"1.0.0","library_sha256":"` + strings.Repeat("ab", 32) + `"},"uri":"grpc://x","query":"SELECT 1"}`
	var data, result bytes.Buffer
	if err := RunNativeADBCWorker(context.Background(), strings.NewReader(payload), nopWriteCloser{&data}, &result); err != nil {
		t.Fatal(err)
	}
	got, err := readNativeADBCResult(&result)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stage != nativeStageDriver || got.Error != ErrNativeADBCUnavailable.Error() {
		t.Fatalf("result = %+v, want the unsupported-build refusal", got)
	}
}

// helperLauncher runs this test binary as the native worker child, in the
// mode TestNativeWorkerHelperProcess acts out.
func helperLauncher(mode string) NativeWorkerLauncher {
	return NativeWorkerLauncher{
		Executable: os.Args[0],
		Args:       []string{"-test.run=^TestNativeWorkerHelperProcess$"},
		Env:        []string{"BROKOLI_NATIVE_WORKER_HELPER=" + mode},
		DriverDir:  os.TempDir(),
	}
}

func TestNativeWorkerLauncherStreamsTheResult(t *testing.T) {
	// The child loads third-party native code; it must not inherit the
	// server's secrets from the environment.
	t.Setenv("BROKOLI_SECRET_KEY", "server-encryption-key-sentinel")
	var out bytes.Buffer
	result, err := helperLauncher("ok").RunNativeADBC(context.Background(), validNativeRequest(), &out)
	if err != nil {
		t.Fatalf("RunNativeADBC: %v", err)
	}
	if result.Rows != 2 || strings.Join(result.Columns, ",") != "id" {
		t.Fatalf("result = %+v", result)
	}
	ds, err := readNativeRows(&out, 0)
	if err != nil {
		t.Fatalf("decode streamed result: %v", err)
	}
	if len(ds.Rows) != 2 {
		t.Fatalf("streamed rows = %d, want 2", len(ds.Rows))
	}
}

func TestNativeWorkerLauncherReportsTheStageAndScrubsCredentials(t *testing.T) {
	request := validNativeRequest()
	request.Options = map[string]string{"adbc.flight.sql.rpc.call_header.authorization": "Bearer very-secret-token"}
	request.secrets = sortedOptionValues(request.Options)
	_, err := helperLauncher("fail").RunNativeADBC(context.Background(), request, io.Discard)
	var nativeErr *NativeADBCError
	if !errors.As(err, &nativeErr) {
		t.Fatalf("error = %v, want a NativeADBCError", err)
	}
	if nativeErr.Stage != nativeStageConnect {
		t.Errorf("stage = %q, want %q", nativeErr.Stage, nativeStageConnect)
	}
	if strings.Contains(err.Error(), "very-secret-token") {
		t.Fatalf("error leaks the credential: %v", err)
	}
	if !strings.Contains(err.Error(), "authentication refused") || !strings.Contains(err.Error(), "driver said hello") {
		t.Fatalf("error lost the driver's message or output: %v", err)
	}
}

func TestNativeWorkerLauncherReportsACrash(t *testing.T) {
	_, err := helperLauncher("crash").RunNativeADBC(context.Background(), validNativeRequest(), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "exited unexpectedly") || !strings.Contains(err.Error(), "segfault-ish") {
		t.Fatalf("error = %v, want an unexpected exit with the child's output", err)
	}
}

// A consumer that has what it needs stops the child instead of draining an
// unbounded result.
func TestNativeWorkerLauncherStopsWhenTheConsumerStops(t *testing.T) {
	stop := errors.New("enough")
	start := time.Now()
	_, err := helperLauncher("endless").RunNativeADBC(context.Background(), validNativeRequest(), &failAfterWriter{n: 64 << 10, err: stop})
	if !errors.Is(err, stop) {
		t.Fatalf("error = %v, want the consumer's", err)
	}
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Fatalf("stopping took %s", elapsed)
	}
}

type failAfterWriter struct {
	n   int
	err error
}

func (w *failAfterWriter) Write(p []byte) (int, error) {
	if w.n <= 0 {
		return 0, w.err
	}
	w.n -= len(p)
	return len(p), nil
}

// TestNativeWorkerHelperProcess is the child for the launcher tests. It
// speaks the real protocol: the request on stdin, the Arrow stream on fd 3,
// the result on fd 4.
func TestNativeWorkerHelperProcess(t *testing.T) {
	mode := os.Getenv("BROKOLI_NATIVE_WORKER_HELPER")
	if mode == "" {
		return
	}
	data, result := os.NewFile(nativeWorkerDataFD, "data"), os.NewFile(nativeWorkerResultFD, "result")
	// Noise on stdout must not disturb the protocol.
	fmt.Println("driver said hello")
	request, err := readNativeADBCRequest(os.Stdin)
	if err != nil {
		_ = writeNativeADBCResult(result, NativeADBCResult{Stage: nativeStageRequest, Error: err.Error()})
		os.Exit(0)
	}
	switch mode {
	case "ok":
		if os.Getenv("BROKOLI_SECRET_KEY") != "" {
			_ = data.Close()
			_ = writeNativeADBCResult(result, NativeADBCResult{Stage: nativeStageDriver, Error: "the server environment leaked into the worker"})
			os.Exit(0)
		}
		_ = EncodeArrowIPC(data, &common.DataSet{Columns: []string{"id"}, Rows: []common.DataRow{{"id": int64(1)}, {"id": int64(2)}}})
		_ = data.Close()
		_ = writeNativeADBCResult(result, NativeADBCResult{Rows: 2, Columns: []string{"id"}})
	case "fail":
		token := request.Options["adbc.flight.sql.rpc.call_header.authorization"]
		fmt.Fprintln(os.Stderr, "sending", token)
		_ = data.Close()
		_ = writeNativeADBCResult(result, NativeADBCResult{Stage: nativeStageConnect, Error: "authentication refused for " + token})
	case "crash":
		fmt.Fprintln(os.Stderr, "segfault-ish")
		os.Exit(3)
	case "endless":
		chunk := bytes.Repeat([]byte("x"), 32<<10)
		for {
			if _, err := data.Write(chunk); err != nil {
				os.Exit(0)
			}
		}
	}
	os.Exit(0)
}
