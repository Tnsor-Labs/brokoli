package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/artifact"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

type flightSQLTestWorker struct{ calls int }

func (w *flightSQLTestWorker) RunFlightSQL(_ context.Context, request NativeFlightSQLRequest) (NativeFlightSQLResponse, error) {
	w.calls++
	var data bytes.Buffer
	if err := EncodeArrowIPC(&data, &common.DataSet{Columns: []string{"id"}, Rows: []common.DataRow{{"id": int64(1)}}}); err != nil {
		return NativeFlightSQLResponse{}, err
	}
	if err := os.WriteFile(request.OutputPath, data.Bytes(), 0o600); err != nil {
		return NativeFlightSQLResponse{}, err
	}
	sum := sha256.Sum256(data.Bytes())
	return NativeFlightSQLResponse{Rows: 1, OutputSize: int64(data.Len()), OutputSHA256: hex.EncodeToString(sum[:])}, nil
}

func TestRunFlightSQLSourceStoresArrowReference(t *testing.T) {
	store := artifact.NewLocalDiskStore(t.TempDir())
	outputs := newNodeOutputs(store, "run-flight", 1)
	worker := &flightSQLTestWorker{}
	ref, err := runFlightSQLSource(context.Background(), worker, outputs, "driver.so", "grpc+tcp://example.invalid", "SELECT 1", nil)
	if err != nil {
		t.Fatalf("runFlightSQLSource: %v", err)
	}
	if worker.calls != 1 || ref.Format != artifact.FormatArrowIPC || ref.RowCount != 1 || len(ref.Columns) != 1 || ref.Columns[0] != "id" {
		t.Fatalf("unexpected reference: worker=%d ref=%+v", worker.calls, ref)
	}
	got, err := store.Open(context.Background(), &ref.ArtifactRef)
	if err != nil {
		t.Fatalf("stored artifact: %v", err)
	}
	defer got.Close()
	if data, err := io.ReadAll(got); err != nil || len(data) == 0 {
		t.Fatalf("stored artifact is unreadable: bytes=%d err=%v", len(data), err)
	}
}

func TestRunFlightSQLSourceRefusesMissingWorkerOrStore(t *testing.T) {
	_, err := runFlightSQLSource(context.Background(), nil, newNodeOutputs(artifact.NewLocalDiskStore(t.TempDir()), "run", 1), "driver", "uri", "SELECT 1", nil)
	if err == nil {
		t.Fatal("missing worker succeeded")
	}
	_, err = runFlightSQLSource(context.Background(), &flightSQLTestWorker{}, newNodeOutputs(nil, "", 0), "driver", "uri", "SELECT 1", nil)
	if err == nil {
		t.Fatal("missing store succeeded")
	}
}
