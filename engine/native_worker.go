package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"sync"

	"github.com/Tnsor-Labs/brokoli/pkg/codeexec"
	"github.com/Tnsor-Labs/brokoli/pkg/drivers"
)

// NativeADBCRunner executes one native ADBC query in isolation and streams
// its result into out as an Arrow IPC stream.
//
// An error from out stops the query: the worker is killed and that error is
// returned unchanged, which is how a reader that has seen enough rows (a
// dry run) ends one early.
type NativeADBCRunner interface {
	RunNativeADBC(ctx context.Context, request NativeADBCRequest, out io.Writer) (NativeADBCResult, error)
}

// NativeWorkerLauncher runs each native query in a fresh `worker task` child
// of this executable, so a crashing or misbehaving C driver takes down one
// child and not the server.
//
// The child is given an allowlisted environment, never this process's own:
// it loads third-party native code, which must not be able to read the
// server's encryption key, database DSN or cloud credentials from its
// environment. BROKOLI_NATIVE_PASS_ENV widens it the way
// BROKOLI_CODE_PASS_ENV does for code nodes. Resource ceilings come from
// BROKOLI_NATIVE_MEMORY_MB, BROKOLI_NATIVE_CPU_SECONDS and
// BROKOLI_NATIVE_OPEN_FILES, applied by the child before any driver loads.
type NativeWorkerLauncher struct {
	// Executable defaults to os.Executable(). An embedder whose binary does
	// not register core's `worker task` command must point it at one that
	// does.
	Executable string
	// Args defaults to {"worker", "task"}.
	Args []string
	// Env is appended to the child's allowlisted environment.
	Env []string
	// DriverDir is where the child looks pinned identities up. Defaults to
	// drivers.DefaultDir().
	DriverDir string
}

// nativeWorkerOutputTail bounds how much of the driver's own stdout and
// stderr is kept for an error report.
const nativeWorkerOutputTail = 8 << 10

// nativeWorkerExtraEnvVar names the operator's list of further variables
// the worker may see, as BROKOLI_CODE_PASS_ENV does for code nodes.
const nativeWorkerExtraEnvVar = "BROKOLI_NATIVE_PASS_ENV"

// RunNativeADBC implements NativeADBCRunner.
func (l NativeWorkerLauncher) RunNativeADBC(ctx context.Context, request NativeADBCRequest, out io.Writer) (NativeADBCResult, error) {
	if err := request.validate(); err != nil {
		return NativeADBCResult{}, err
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return NativeADBCResult{}, fmt.Errorf("encode native worker request: %w", err)
	}
	executable := l.Executable
	if executable == "" {
		if executable, err = os.Executable(); err != nil {
			return NativeADBCResult{}, fmt.Errorf("locate native worker executable: %w", err)
		}
	}
	args := l.Args
	if len(args) == 0 {
		args = []string{"worker", "task"}
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	dataR, dataW, err := os.Pipe()
	if err != nil {
		return NativeADBCResult{}, fmt.Errorf("open native worker data channel: %w", err)
	}
	defer dataR.Close()
	resultR, resultW, err := os.Pipe()
	if err != nil {
		_ = dataW.Close() // Nothing was started; both ends are ours to close.
		return NativeADBCResult{}, fmt.Errorf("open native worker result channel: %w", err)
	}
	defer resultR.Close()

	tail := &tailBuffer{limit: nativeWorkerOutputTail}
	cmd := exec.CommandContext(ctx, executable, args...) // #nosec G204 -- executable and child arguments are trusted worker configuration.
	cmd.Env = l.env()
	cmd.Stdin = bytes.NewReader(payload)
	cmd.Stdout, cmd.Stderr = tail, tail
	cmd.ExtraFiles = []*os.File{dataW, resultW} // fds 3 and 4 in the child
	startErr := cmd.Start()
	// The child holds its own copies; ours must close, or reading the data
	// channel would never see EOF.
	_ = dataW.Close()
	_ = resultW.Close()
	if startErr != nil {
		return NativeADBCResult{}, fmt.Errorf("start native worker: %w", startErr)
	}

	type decoded struct {
		result NativeADBCResult
		err    error
	}
	resultCh := make(chan decoded, 1)
	go func() {
		result, err := readNativeADBCResult(resultR)
		resultCh <- decoded{result, err}
	}()

	_, copyErr := io.Copy(out, dataR)
	if copyErr != nil {
		// The consumer stopped reading. Stop the child rather than let it
		// block on a full pipe, or keep a remote query running for nothing.
		cancel()
	}
	waitErr := cmd.Wait()
	got := <-resultCh

	if copyErr != nil {
		return NativeADBCResult{}, copyErr
	}
	if err := ctx.Err(); err != nil && waitErr != nil {
		return NativeADBCResult{}, err
	}
	output := scrubSecrets(tail.String(), request.secrets)
	if got.err == nil && got.result.Error != "" {
		return NativeADBCResult{}, &NativeADBCError{
			Stage:   got.result.Stage,
			Message: scrubSecrets(got.result.Error, request.secrets),
			Output:  output,
		}
	}
	if waitErr != nil {
		return NativeADBCResult{}, &NativeADBCError{Message: "worker exited unexpectedly: " + waitErr.Error(), Output: output}
	}
	if got.err != nil {
		return NativeADBCResult{}, &NativeADBCError{Message: "worker returned no result: " + got.err.Error(), Output: output}
	}
	return got.result, nil
}

func (l NativeWorkerLauncher) env() []string {
	env := codeexec.AllowlistedEnv(nativeWorkerExtraEnvVar,
		// TLS trust and Kerberos configuration are how a driver reaches a
		// database securely; they are paths, not credentials.
		"SSL_CERT_FILE", "SSL_CERT_DIR", "KRB5_CONFIG",
		"BROKOLI_NATIVE_MEMORY_MB", "BROKOLI_NATIVE_CPU_SECONDS", "BROKOLI_NATIVE_OPEN_FILES")
	dir := l.DriverDir
	if dir == "" {
		dir = drivers.DefaultDir()
	}
	env = append(env, "BROKOLI_DRIVER_DIR="+dir)
	return append(env, l.Env...)
}

// tailBuffer keeps the last limit bytes written to it.
type tailBuffer struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.limit; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(bytes.TrimSpace(t.buf))
}

// RunNativeADBCWorker is the `worker task` child: it serves exactly one
// request from in, streams the result to data, closes data, and writes the
// outcome to result. It returns an error only when it cannot report one.
func RunNativeADBCWorker(ctx context.Context, in io.Reader, data io.WriteCloser, result io.Writer) error {
	outcome := serveNativeADBC(ctx, in, data)
	if err := data.Close(); err != nil && outcome.Error == "" {
		outcome = NativeADBCResult{Stage: nativeStageWrite, Error: err.Error()}
	}
	return writeNativeADBCResult(result, outcome)
}

func serveNativeADBC(ctx context.Context, in io.Reader, data io.Writer) NativeADBCResult {
	if err := applyNativeWorkerLimits(nativeWorkerLimitsFromEnv()); err != nil {
		return NativeADBCResult{Stage: nativeStageRequest, Error: "apply resource limits: " + err.Error()}
	}
	request, err := readNativeADBCRequest(in)
	if err != nil {
		return NativeADBCResult{Stage: nativeStageRequest, Error: err.Error()}
	}
	if !NativeADBCWorkerEnabled() {
		return NativeADBCResult{Stage: nativeStageDriver, Error: ErrNativeADBCUnavailable.Error()}
	}
	// The identity is resolved here, in the process about to load it, and
	// the library is hashed immediately before the load.
	manifest, err := drivers.LoadIdentity(drivers.DefaultDir(), request.Driver)
	if err != nil {
		return NativeADBCResult{Stage: nativeStageDriver, Error: err.Error()}
	}
	rows, columns, err := streamNativeADBC(ctx, manifest.LibraryPath(), manifest.Entrypoint, request, data)
	if err != nil {
		stage := nativeStageQuery
		var staged *nativeStageError
		if errors.As(err, &staged) {
			stage, err = staged.stage, staged.err
		}
		return NativeADBCResult{Stage: stage, Error: err.Error()}
	}
	return NativeADBCResult{Rows: rows, Columns: columns}
}

// nativeStageError attributes a driver error to the step it came from.
type nativeStageError struct {
	stage string
	err   error
}

func (e *nativeStageError) Error() string { return e.stage + ": " + e.err.Error() }
func (e *nativeStageError) Unwrap() error { return e.err }

func atStage(stage string, err error) error {
	if err == nil {
		return nil
	}
	return &nativeStageError{stage: stage, err: err}
}

// nativeWorkerLimits are the child's resource ceilings, in the units
// setrlimit takes. Zero is unlimited.
type nativeWorkerLimits struct {
	MemoryBytes uint64
	CPUSeconds  uint64
	OpenFiles   uint64
}

func nativeWorkerLimitsFromEnv() nativeWorkerLimits {
	read := func(name string) uint64 {
		n, err := strconv.ParseUint(os.Getenv(name), 10, 32)
		if err != nil {
			return 0
		}
		return n
	}
	limits := nativeWorkerLimits{
		MemoryBytes: read("BROKOLI_NATIVE_MEMORY_MB") << 20,
		CPUSeconds:  read("BROKOLI_NATIVE_CPU_SECONDS"),
		OpenFiles:   read("BROKOLI_NATIVE_OPEN_FILES"),
	}
	if limits.OpenFiles == 0 {
		limits.OpenFiles = 1024
	}
	return limits
}

// sortedOptionValues is every option value, for scrubbing.
func sortedOptionValues(options map[string]string) []string {
	values := make([]string, 0, len(options))
	for _, value := range options {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}
