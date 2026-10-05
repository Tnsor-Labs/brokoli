package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeWorkerProtocolRejectsMultipleRequests(t *testing.T) {
	_, err := readNativeFlightSQLRequest(strings.NewReader(`{"library":"driver","uri":"uri","query":"query","output_path":"out"} {}`))
	if err == nil {
		t.Fatal("readNativeFlightSQLRequest accepted multiple JSON values")
	}
}

func TestRunNativeFlightSQLWorkerUnavailable(t *testing.T) {
	var output bytes.Buffer
	request := `{"library":"driver","uri":"uri","query":"query","output_path":"` + filepath.Join(t.TempDir(), "out") + `"}`
	err := RunNativeFlightSQLWorker(context.Background(), strings.NewReader(request), &output)
	if err != nil {
		t.Fatalf("RunNativeFlightSQLWorker: %v", err)
	}
	response, err := readNativeFlightSQLResponse(&output)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if response.Error == "" {
		t.Fatal("worker response did not report unavailable execution")
	}
}

func TestStreamNativeFlightSQLToArrowIPCUnavailable(t *testing.T) {
	_, err := StreamNativeFlightSQLToArrowIPC(context.Background(), "driver", "uri", "query", nil, io.Discard)
	if err == nil {
		t.Fatal("StreamNativeFlightSQLToArrowIPC succeeded with an invalid driver")
	}
	if errors.Is(err, ErrNativeFlightSQLUnavailable) {
		return
	}
}

func TestNativeWorkerLauncherVerifiesAndPublishesOutput(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "result.arrow")
	launcher := NativeWorkerLauncher{Executable: os.Args[0], Args: []string{"-test.run=^TestNativeWorkerHelperProcess$"}, Env: []string{"BROKOLI_NATIVE_WORKER_HELPER=1"}}
	response, err := launcher.RunFlightSQL(context.Background(), NativeFlightSQLRequest{
		Library: "driver", URI: "grpc://example.invalid", Query: "SELECT 1", Headers: map[string]string{"authorization": "secret"}, OutputPath: destination,
	})
	if err != nil {
		t.Fatalf("RunFlightSQL: %v", err)
	}
	if response.Rows != 1 {
		t.Errorf("Rows = %d, want 1", response.Rows)
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("read published output: %v", err)
	}
	if string(data) != "arrow-ipc" {
		t.Fatalf("published output = %q", data)
	}
}

func TestNativeWorkerLauncherRejectsInvalidOutput(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "result.arrow")
	launcher := NativeWorkerLauncher{Executable: os.Args[0], Args: []string{"-test.run=^TestNativeWorkerBadHelperProcess$"}, Env: []string{"BROKOLI_NATIVE_WORKER_HELPER=2"}}
	_, err := launcher.RunFlightSQL(context.Background(), NativeFlightSQLRequest{Library: "driver", URI: "uri", Query: "query", OutputPath: destination})
	if err == nil {
		t.Fatal("RunFlightSQL accepted invalid output metadata")
	}
	if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("destination exists after verification failure: %v", statErr)
	}
}

func TestNativeWorkerHelperProcess(t *testing.T) {
	if os.Getenv("BROKOLI_NATIVE_WORKER_HELPER") != "1" {
		return
	}
	request, err := readNativeFlightSQLRequest(os.Stdin)
	if err != nil {
		os.Exit(2)
	}
	data := []byte("arrow-ipc")
	if err := os.WriteFile(request.OutputPath, data, 0o600); err != nil {
		os.Exit(2)
	}
	sum := sha256.Sum256(data)
	if err := writeNativeFlightSQLResponse(os.Stdout, NativeFlightSQLResponse{Rows: 1, OutputSize: int64(len(data)), OutputSHA256: hex.EncodeToString(sum[:])}); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestNativeWorkerBadHelperProcess(t *testing.T) {
	if os.Getenv("BROKOLI_NATIVE_WORKER_HELPER") != "2" {
		return
	}
	request, err := readNativeFlightSQLRequest(os.Stdin)
	if err != nil {
		os.Exit(2)
	}
	if err := os.WriteFile(request.OutputPath, []byte("wrong"), 0o600); err != nil {
		os.Exit(2)
	}
	if err := writeNativeFlightSQLResponse(os.Stdout, NativeFlightSQLResponse{OutputSize: 5, OutputSHA256: strings.Repeat("0", sha256.Size*2)}); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}
