package engine

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/hc12r/brokolisql-go/pkg/common"
)

func makeTestRows(n int) []common.DataRow {
	rows := make([]common.DataRow, n)
	for i := range rows {
		rows[i] = common.DataRow{
			"id":   float64(i),
			"name": fmt.Sprintf("row_%d", i),
		}
	}
	return rows
}

func TestDataStream_SendReceive(t *testing.T) {
	stream := NewDataStream(10)
	ctx := context.Background()

	go func() {
		stream.Send(RowBatch{Columns: []string{"id", "name"}, Rows: makeTestRows(5)})
		stream.Send(RowBatch{Done: true})
		stream.Close()
	}()

	var totalRows int
	for {
		batch, more := stream.Receive(ctx)
		if batch.Error != nil {
			t.Fatalf("unexpected error: %v", batch.Error)
		}
		totalRows += len(batch.Rows)
		if !more {
			break
		}
	}

	if totalRows != 5 {
		t.Errorf("expected 5 rows, got %d", totalRows)
	}
}

func TestDataStream_SendRows_Batching(t *testing.T) {
	stream := NewDataStream(20)
	ctx := context.Background()
	rows := makeTestRows(12)

	go func() {
		stream.SendRows([]string{"id", "name"}, rows, 5) // 3 batches: 5, 5, 2
		stream.Close()
	}()

	var batchCount int
	var totalRows int
	for {
		batch, more := stream.Receive(ctx)
		if batch.Error != nil {
			t.Fatalf("error: %v", batch.Error)
		}
		if len(batch.Rows) > 0 {
			batchCount++
		}
		totalRows += len(batch.Rows)
		if !more {
			break
		}
	}

	if totalRows != 12 {
		t.Errorf("expected 12 total rows, got %d", totalRows)
	}
	if batchCount != 3 {
		t.Errorf("expected 3 batches, got %d", batchCount)
	}
}

func TestDataStream_Collect(t *testing.T) {
	stream := NewDataStream(10)
	ctx := context.Background()
	rows := makeTestRows(100)

	go func() {
		stream.SendRows([]string{"id", "name"}, rows, 25)
		stream.Close()
	}()

	ds, err := stream.Collect(ctx)
	if err != nil {
		t.Fatalf("collect error: %v", err)
	}
	if len(ds.Rows) != 100 {
		t.Errorf("expected 100 rows, got %d", len(ds.Rows))
	}
	if len(ds.Columns) != 2 {
		t.Errorf("expected 2 columns, got %d", len(ds.Columns))
	}
}

func TestDataStream_CollectEmpty(t *testing.T) {
	stream := NewDataStream(5)
	ctx := context.Background()

	go func() {
		stream.Send(RowBatch{Columns: []string{"a"}, Done: true})
		stream.Close()
	}()

	ds, err := stream.Collect(ctx)
	if err != nil {
		t.Fatalf("collect error: %v", err)
	}
	if len(ds.Rows) != 0 {
		t.Errorf("expected 0 rows, got %d", len(ds.Rows))
	}
}

func TestDataStream_ErrorPropagation(t *testing.T) {
	stream := NewDataStream(5)
	ctx := context.Background()

	go func() {
		stream.Send(RowBatch{Rows: makeTestRows(3)})
		stream.CloseWithError(fmt.Errorf("upstream failed"))
	}()

	_, err := stream.Collect(ctx)
	if err == nil {
		t.Fatal("expected error from collect")
	}
	if err.Error() != "upstream failed" {
		t.Errorf("expected 'upstream failed', got %q", err.Error())
	}
}

func TestDataStream_ContextCancellation(t *testing.T) {
	stream := NewDataStream(5)
	ctx, cancel := context.WithCancel(context.Background())

	// Cancel before sending anything
	cancel()

	batch, more := stream.Receive(ctx)
	if more {
		t.Error("expected no more after cancellation")
	}
	if batch.Error == nil {
		t.Error("expected context error")
	}
}

func TestFromDataSet(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"x", "y"},
		Rows:    makeTestRows(50),
	}

	stream := FromDataSet(ds, 20) // 3 batches: 20, 20, 10
	ctx := context.Background()

	result, err := stream.Collect(ctx)
	if err != nil {
		t.Fatalf("collect error: %v", err)
	}
	if len(result.Rows) != 50 {
		t.Errorf("expected 50 rows, got %d", len(result.Rows))
	}
}

func TestFromDataSet_Nil(t *testing.T) {
	stream := FromDataSet(nil, 10)
	ctx := context.Background()

	result, err := stream.Collect(ctx)
	if err != nil {
		t.Fatalf("collect error: %v", err)
	}
	if len(result.Rows) != 0 {
		t.Errorf("expected 0 rows, got %d", len(result.Rows))
	}
}

func TestStreamTransform(t *testing.T) {
	input := NewDataStream(10)
	output := NewDataStream(10)
	ctx := context.Background()

	go func() {
		input.SendRows([]string{"id", "name"}, makeTestRows(10), 5)
		input.Close()
	}()

	go func() {
		StreamTransform(ctx, input, output, func(row common.DataRow) (common.DataRow, bool) {
			// Double the ID
			id, _ := row["id"].(float64)
			row["id"] = id * 2
			return row, true
		})
	}()

	result, err := output.Collect(ctx)
	if err != nil {
		t.Fatalf("collect error: %v", err)
	}
	if len(result.Rows) != 10 {
		t.Errorf("expected 10 rows, got %d", len(result.Rows))
	}
	// First row should have id=0 (0*2)
	if result.Rows[0]["id"] != 0.0 {
		t.Errorf("expected id=0, got %v", result.Rows[0]["id"])
	}
}

func TestStreamFilter(t *testing.T) {
	input := NewDataStream(10)
	output := NewDataStream(10)
	ctx := context.Background()

	go func() {
		input.SendRows([]string{"id", "name"}, makeTestRows(20), 5)
		input.Close()
	}()

	go func() {
		StreamFilter(ctx, input, output, func(row common.DataRow) bool {
			id, _ := row["id"].(float64)
			return int(id)%2 == 0 // Keep even IDs
		})
	}()

	result, err := output.Collect(ctx)
	if err != nil {
		t.Fatalf("collect error: %v", err)
	}
	if len(result.Rows) != 10 {
		t.Errorf("expected 10 even rows, got %d", len(result.Rows))
	}
}

func TestStreamConcurrency(t *testing.T) {
	// Multiple producers, one consumer
	stream := NewDataStream(100)
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			rows := make([]common.DataRow, 100)
			for j := range rows {
				rows[j] = common.DataRow{"producer": float64(idx), "row": float64(j)}
			}
			for _, r := range rows {
				stream.Send(RowBatch{Columns: []string{"producer", "row"}, Rows: []common.DataRow{r}})
			}
		}(i)
	}

	go func() {
		wg.Wait()
		stream.Send(RowBatch{Done: true})
		stream.Close()
	}()

	result, err := stream.Collect(ctx)
	if err != nil {
		t.Fatalf("collect error: %v", err)
	}
	if len(result.Rows) != 500 {
		t.Errorf("expected 500 rows from 5 producers, got %d", len(result.Rows))
	}
}

func TestDefaultStreamConfig(t *testing.T) {
	cfg := DefaultStreamConfig()
	if cfg.BufferSize != 1000 {
		t.Errorf("expected buffer 1000, got %d", cfg.BufferSize)
	}
	if cfg.StreamThreshold != 10000 {
		t.Errorf("expected threshold 10000, got %d", cfg.StreamThreshold)
	}
	if cfg.BatchSize != 500 {
		t.Errorf("expected batch 500, got %d", cfg.BatchSize)
	}
}
