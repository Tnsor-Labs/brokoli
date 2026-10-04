package engine

import (
	"context"
	"fmt"
	"io"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// streamMigrateBulk connects a database cursor to a bulk writer with bounded
// batches. The producer copies only the batch slice: row maps are allocated by
// the scanner and never mutated after emission. A full source result is never
// materialized, which is essential for cross-database migrations.
func (r *Runner) streamMigrateBulk(sourceURI, sourceQuery string, sourceArgs []interface{}, destURI string, cfg SQLGenConfig, writer bulkBatchWriter) (int64, int, error) {
	ctx, cancel := context.WithCancel(r.ctx)
	defer cancel()

	type batchResult struct {
		batch *common.DataSet
		err   error
	}
	batches := make(chan *common.DataSet, 2)
	first := make(chan batchResult, 1)
	producerDone := make(chan error, 1)
	go func() {
		firstBatch := true
		_, _, err := streamQueryDatabase(ctx, sourceURI, sourceQuery, sourceArgs, cfg.BatchSize, func(batch *common.DataSet) error {
			// streamQueryDatabase reuses its row slice after emit returns.
			copied := &common.DataSet{Columns: batch.Columns, Rows: append([]common.DataRow(nil), batch.Rows...)}
			if firstBatch {
				firstBatch = false
				select {
				case first <- batchResult{batch: copied}:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			select {
			case batches <- copied:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		if firstBatch {
			first <- batchResult{err: err}
		}
		close(batches)
		producerDone <- err
	}()

	initial := <-first
	if initial.err != nil {
		return 0, 0, fmt.Errorf("source query: %w", initial.err)
	}
	if initial.batch == nil {
		return 0, 0, nil
	}
	chunks := 0
	pending := initial.batch
	next := func() (*common.DataSet, error) {
		if pending != nil {
			batch := pending
			pending = nil
			chunks++
			return batch, nil
		}
		select {
		case batch, ok := <-batches:
			if !ok {
				if err := <-producerDone; err != nil {
					return nil, fmt.Errorf("source query: %w", err)
				}
				return nil, io.EOF
			}
			chunks++
			return batch, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	affected, err := writer(ctx, destURI, cfg, initial.batch.Columns, next)
	if err != nil {
		return 0, chunks, err
	}
	return affected, chunks, nil
}
