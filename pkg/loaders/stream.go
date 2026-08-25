package loaders

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// defaultStreamBatchRows is the number of rows gathered before a batch is
// handed to the consumer. Large enough that the per-batch overhead is not the
// cost that matters, small enough that a batch of wide rows is still a
// rounding error against a worker's memory budget.
const defaultStreamBatchRows = 5000

// SupportsStreaming reports whether a file can be read incrementally.
//
// CSV can: its header row names the columns before any data is read, so the
// shape of the result is known up front. JSON cannot, and the reason is
// structural rather than unfinished work — ConvertToDataSet derives the
// column set from the union of keys across every object in the file, so the
// columns are not known until the last object has been parsed. A single-pass
// JSON reader would have to either guess the columns from the first batch,
// which changes the result for heterogeneous objects, or read the whole file
// to find them, which is what streaming exists to avoid.
func SupportsStreaming(filePath string) bool {
	return strings.EqualFold(filepath.Ext(filePath), ".csv")
}

// StreamBatches reads filePath in batches, calling emit for each one, and
// returns the columns and the total row count. The whole file is never held
// in memory: a batch is handed over and dropped before the next is read.
//
// The result is the same DataSet the matching Loader would have produced,
// batch boundaries aside. That equivalence is the point — a caller choosing
// between this and Load must be choosing on memory, not on semantics — and it
// is asserted by test rather than assumed.
//
// emit must not retain the batch it is given; the caller reuses nothing, but
// the batch is dropped as soon as emit returns so retaining it defeats the
// bounded-memory property.
func StreamBatches(ctx context.Context, filePath string, batchSize int, emit func(*common.DataSet) error) ([]string, int64, error) {
	if !SupportsStreaming(filePath) {
		return nil, 0, fmt.Errorf("streaming is not supported for %q files", filepath.Ext(filePath))
	}
	if batchSize <= 0 {
		batchSize = defaultStreamBatchRows
	}

	file, err := common.SafeOpenFile(filePath)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to open CSV file: %w", err)
	}
	defer func() {
		if cerr := file.Close(); cerr != nil {
			common.DefaultLogger.Warning("Failed to close CSV file: %v", cerr)
		}
	}()

	reader := csv.NewReader(file)

	headers, err := reader.Read()
	if err != nil {
		return nil, 0, fmt.Errorf("failed to read CSV headers: %w", err)
	}
	for i, header := range headers {
		headers[i] = strings.TrimSpace(header)
	}

	var total int64
	batch := make([]common.DataRow, 0, batchSize)

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := emit(&common.DataSet{Columns: headers, Rows: batch}); err != nil {
			return err
		}
		batch = make([]common.DataRow, 0, batchSize)
		return nil
	}

	for {
		// Checked per row rather than per batch: a cancelled run should stop
		// reading a large file promptly, not at the next batch boundary.
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}

		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			// A malformed or truncated row fails the read. The channel-based
			// loader this replaces signalled errors by sending a nil row and
			// discarding the error, so a truncated file looked exactly like a
			// complete one and the run wrote a partial result and reported
			// success.
			return nil, 0, fmt.Errorf("failed to read CSV data: %w", err)
		}

		batch = append(batch, csvRowFrom(record, headers))
		total++

		if len(batch) >= batchSize {
			if err := flush(); err != nil {
				return nil, 0, err
			}
		}
	}

	if err := flush(); err != nil {
		return nil, 0, err
	}
	return headers, total, nil
}

// csvRowFrom builds one row, matching CSVLoader.Load exactly. The empty-field
// rule in particular is a decision that belongs here and not at the output
// boundary: an empty CSV field carries no type information, so it becomes
// NULL where the ambiguity lives.
func csvRowFrom(record []string, headers []string) common.DataRow {
	row := make(common.DataRow, len(headers))
	for i, value := range record {
		if i >= len(headers) {
			break
		}
		if value == "" {
			row[headers[i]] = nil
			continue
		}
		row[headers[i]] = value
	}
	return row
}
