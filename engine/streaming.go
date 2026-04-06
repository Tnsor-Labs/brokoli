package engine

import (
	"context"
	"sync"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// StreamConfig controls streaming behavior for the pipeline engine.
type StreamConfig struct {
	// BufferSize is the channel buffer for streaming rows between nodes.
	// Higher values use more memory but reduce back-pressure stalls.
	// Default: 1000 rows.
	BufferSize int

	// StreamThreshold is the minimum row count to enable streaming.
	// Datasets smaller than this are materialized in memory (faster for small data).
	// Default: 10000 rows.
	StreamThreshold int

	// BatchSize is the number of rows sent per batch in streaming mode.
	// Larger batches reduce channel overhead but increase latency.
	// Default: 500 rows.
	BatchSize int
}

// DefaultStreamConfig returns sensible streaming defaults.
func DefaultStreamConfig() StreamConfig {
	return StreamConfig{
		BufferSize:      1000,
		StreamThreshold: 10000,
		BatchSize:       500,
	}
}

// RowBatch is a chunk of rows sent through a streaming channel.
type RowBatch struct {
	Columns []string
	Rows    []common.DataRow
	Done    bool  // true when this is the last batch
	Error   error // non-nil if upstream failed
}

// DataStream represents a streaming data channel between nodes.
type DataStream struct {
	ch      chan RowBatch
	columns []string
	mu      sync.RWMutex
	closed  bool
}

// NewDataStream creates a buffered streaming channel.
func NewDataStream(bufferSize int) *DataStream {
	if bufferSize <= 0 {
		bufferSize = 1000
	}
	return &DataStream{
		ch: make(chan RowBatch, bufferSize),
	}
}

// Send sends a batch of rows to the stream.
func (s *DataStream) Send(batch RowBatch) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	if len(batch.Columns) > 0 && len(s.columns) == 0 {
		s.columns = batch.Columns
	}
	s.mu.Unlock()
	s.ch <- batch
}

// SendRows sends rows in batches of the specified size.
func (s *DataStream) SendRows(columns []string, rows []common.DataRow, batchSize int) {
	if batchSize <= 0 {
		batchSize = 500
	}
	for i := 0; i < len(rows); i += batchSize {
		end := i + batchSize
		if end > len(rows) {
			end = len(rows)
		}
		s.Send(RowBatch{
			Columns: columns,
			Rows:    rows[i:end],
			Done:    end >= len(rows),
		})
	}
	if len(rows) == 0 {
		s.Send(RowBatch{Columns: columns, Done: true})
	}
}

// Close marks the stream as done.
func (s *DataStream) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.ch)
	}
}

// CloseWithError sends an error and closes the stream.
func (s *DataStream) CloseWithError(err error) {
	s.Send(RowBatch{Error: err, Done: true})
	s.Close()
}

// Receive reads batches from the stream. Returns false when stream is done.
func (s *DataStream) Receive(ctx context.Context) (RowBatch, bool) {
	select {
	case batch, ok := <-s.ch:
		if !ok {
			return RowBatch{Done: true}, false
		}
		return batch, !batch.Done
	case <-ctx.Done():
		return RowBatch{Error: ctx.Err(), Done: true}, false
	}
}

// Collect materializes the entire stream into a DataSet.
// Use this when a node needs all data at once (join, sort, quality check).
func (s *DataStream) Collect(ctx context.Context) (*common.DataSet, error) {
	var allRows []common.DataRow
	var columns []string

	for {
		batch, more := s.Receive(ctx)
		if batch.Error != nil {
			return nil, batch.Error
		}
		if len(batch.Columns) > 0 && len(columns) == 0 {
			columns = batch.Columns
		}
		allRows = append(allRows, batch.Rows...)
		if !more {
			break
		}
	}

	return &common.DataSet{
		Columns: columns,
		Rows:    allRows,
	}, nil
}

// FromDataSet creates a stream from a materialized dataset.
// This bridges the gap between streaming and non-streaming nodes.
func FromDataSet(ds *common.DataSet, batchSize int) *DataStream {
	stream := NewDataStream(10) // Small buffer since we're already materialized
	go func() {
		if ds == nil || len(ds.Rows) == 0 {
			stream.Send(RowBatch{
				Columns: func() []string {
					if ds != nil {
						return ds.Columns
					}
					return []string{}
				}(),
				Done: true,
			})
		} else {
			stream.SendRows(ds.Columns, ds.Rows, batchSize)
		}
		stream.Close()
	}()
	return stream
}

// StreamProcessor is the interface for nodes that can process data in streaming mode.
type StreamProcessor interface {
	// ProcessStream reads from input, transforms, and writes to output.
	ProcessStream(ctx context.Context, input *DataStream, output *DataStream) error
}

// StreamTransform applies a row-level transformation function to a stream.
func StreamTransform(ctx context.Context, input *DataStream, output *DataStream, transformFn func(common.DataRow) (common.DataRow, bool)) error {
	defer output.Close()

	for {
		batch, more := input.Receive(ctx)
		if batch.Error != nil {
			output.CloseWithError(batch.Error)
			return batch.Error
		}

		var outRows []common.DataRow
		for _, row := range batch.Rows {
			if outRow, keep := transformFn(row); keep {
				outRows = append(outRows, outRow)
			}
		}

		if len(outRows) > 0 || batch.Done {
			output.Send(RowBatch{
				Columns: batch.Columns,
				Rows:    outRows,
				Done:    batch.Done,
			})
		}

		if !more {
			break
		}
	}
	return nil
}

// StreamFilter filters rows based on a predicate function.
func StreamFilter(ctx context.Context, input *DataStream, output *DataStream, predicate func(common.DataRow) bool) error {
	return StreamTransform(ctx, input, output, func(row common.DataRow) (common.DataRow, bool) {
		return row, predicate(row)
	})
}
