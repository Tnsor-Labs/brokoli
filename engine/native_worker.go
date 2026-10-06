package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// NativeWorkerLauncher starts the executable that contains the optional native
// worker support. Args is intentionally configurable so an embedding command
// can select its private child entry point and tests can use a helper process.
type NativeWorkerLauncher struct {
	Executable string
	Args       []string
	Env        []string
}

// RunFlightSQL writes one request to a fresh child and atomically publishes its
// verified IPC artifact to request.OutputPath.
func (l NativeWorkerLauncher) RunFlightSQL(ctx context.Context, request NativeFlightSQLRequest) (NativeFlightSQLResponse, error) {
	return l.RunNativeADBC(ctx, request.nativeADBCRequest())
}

// RunNativeADBC writes one generic ADBC request to a fresh child and atomically
// publishes its verified IPC artifact to request.OutputPath.
func (l NativeWorkerLauncher) RunNativeADBC(ctx context.Context, request NativeADBCRequest) (NativeADBCResponse, error) {
	if request.OutputPath == "" {
		return NativeADBCResponse{}, errors.New("native worker output path is required")
	}
	executable := l.Executable
	if executable == "" {
		var err error
		executable, err = os.Executable()
		if err != nil {
			return NativeADBCResponse{}, fmt.Errorf("locate native worker executable: %w", err)
		}
	}
	args := l.Args
	if len(args) == 0 {
		args = []string{"worker", "task"}
	}
	tempDir, err := os.MkdirTemp("", "brokoli-native-worker-")
	if err != nil {
		return NativeADBCResponse{}, fmt.Errorf("create native worker directory: %w", err)
	}
	defer os.RemoveAll(tempDir)
	outputPath := filepath.Join(tempDir, "output.arrow")
	destinationPath := request.OutputPath
	request.OutputPath = outputPath
	if err := request.validate(); err != nil {
		return NativeADBCResponse{}, err
	}
	cmd := exec.CommandContext(ctx, executable, args...) // #nosec G204 -- executable and child arguments are trusted worker configuration.
	cmd.Env = append(os.Environ(), l.Env...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return NativeADBCResponse{}, fmt.Errorf("open native worker input: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return NativeADBCResponse{}, fmt.Errorf("open native worker output: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return NativeADBCResponse{}, fmt.Errorf("start native worker: %w", err)
	}
	if err := json.NewEncoder(stdin).Encode(request); err != nil {
		stdin.Close()
		cmd.Wait()
		return NativeADBCResponse{}, fmt.Errorf("write native worker request: %w", err)
	}
	if err := stdin.Close(); err != nil {
		cmd.Wait()
		return NativeADBCResponse{}, fmt.Errorf("close native worker input: %w", err)
	}
	response, responseErr := readNativeADBCResponse(stdout)
	waitErr := cmd.Wait()
	if responseErr != nil {
		return NativeADBCResponse{}, responseErr
	}
	if waitErr != nil {
		return NativeADBCResponse{}, errors.New("native worker exited unsuccessfully")
	}
	if response.Error != "" {
		return NativeADBCResponse{}, errors.New("native worker failed")
	}
	if err := verifyNativeWorkerOutput(outputPath, response); err != nil {
		return NativeADBCResponse{}, err
	}
	if err := copyNativeWorkerOutput(outputPath, destinationPath); err != nil {
		return NativeADBCResponse{}, err
	}
	return response, nil
}

// RunNativeFlightSQLWorker serves exactly one stdin request and stdout response.
// A command entry point can call this without exposing credentials in argv.
func RunNativeFlightSQLWorker(ctx context.Context, in io.Reader, out io.Writer) error {
	request, err := readNativeFlightSQLRequest(in)
	if err != nil {
		return writeNativeFlightSQLResponse(out, NativeFlightSQLResponse{Error: "invalid request"})
	}
	response, err := runNativeADBCWorker(ctx, request.nativeADBCRequest())
	if err != nil {
		response = NativeADBCResponse{Error: "native Flight SQL execution failed"}
	}
	return writeNativeFlightSQLResponse(out, response)
}

// RunNativeADBCWorker serves exactly one generic ADBC request and response.
func RunNativeADBCWorker(ctx context.Context, in io.Reader, out io.Writer) error {
	request, err := readNativeADBCRequest(in)
	if err != nil {
		return writeNativeADBCResponse(out, NativeADBCResponse{Error: "invalid request"})
	}
	response, err := runNativeADBCWorker(ctx, request)
	if err != nil {
		response = NativeADBCResponse{Error: "native ADBC execution failed"}
	}
	return writeNativeADBCResponse(out, response)
}

func verifyNativeWorkerOutput(path string, response NativeADBCResponse) error {
	if response.OutputSize < 0 || len(response.OutputSHA256) != sha256.Size*2 {
		return errors.New("native worker returned invalid output metadata")
	}
	f, err := os.Open(path)
	if err != nil {
		return errors.New("native worker output is unavailable")
	}
	defer f.Close()
	sum := sha256.New()
	size, err := io.Copy(sum, f)
	if err != nil {
		return errors.New("read native worker output")
	}
	if size != response.OutputSize || hex.EncodeToString(sum.Sum(nil)) != response.OutputSHA256 {
		return errors.New("native worker output verification failed")
	}
	return nil
}

func copyNativeWorkerOutput(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return errors.New("native worker output is unavailable")
	}
	defer in.Close()
	out, err := os.CreateTemp(filepath.Dir(destination), ".brokoli-native-output-")
	if err != nil {
		return fmt.Errorf("create native worker destination: %w", err)
	}
	tempPath := out.Name()
	defer os.Remove(tempPath)
	if err := out.Chmod(0o600); err != nil {
		out.Close()
		return fmt.Errorf("set native worker destination permissions: %w", err)
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return fmt.Errorf("copy native worker output: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close native worker destination: %w", closeErr)
	}
	if err := os.Rename(tempPath, destination); err != nil {
		return fmt.Errorf("publish native worker output: %w", err)
	}
	return nil
}
