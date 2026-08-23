package engine

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// Writing a file sink without holding the file in memory.
//
// runSinkFile marshals the whole dataset into one []byte and hands it to
// os.WriteFile, so writing a 200k-row extract costs the dataset plus a
// second full copy of it encoded. That is what OOM-killed the worker on
// the pipeline #304 was filed for: the source had already been taught to
// stream, and the file sink pulled the entire thing back into memory to
// serialise it.
//
// The output here is byte-identical to marshalCSV and to
// json.MarshalIndent(rows, "", "  ") — asserted by test against the
// existing implementations, because a sink whose format shifts when the
// planner picks a different path is worse than a slow one.

// csvRecordFor renders one row in marshalCSV's exact terms. Shared with
// marshalCSV so the streamed and buffered encoders cannot drift.
func csvRecordFor(row common.DataRow, columns []string) []string {
	record := make([]string, len(columns))
	for i, col := range columns {
		if v, ok := row[col]; ok && v != nil {
			switch val := v.(type) {
			case float64:
				// Remove floating point noise
				if val == float64(int64(val)) {
					record[i] = fmt.Sprintf("%d", int64(val))
				} else {
					record[i] = strconv.FormatFloat(val, 'f', -1, 64)
				}
			default:
				record[i] = fmt.Sprintf("%v", v)
			}
		}
	}
	return record
}

// sinkFileFormatStreams reports whether a format can be written
// incrementally. "sql" cannot: it reads a single sql_output cell from the
// first row, which is by definition a tiny input that never arrives as a
// reference anyway.
func sinkFileFormatStreams(format string) bool {
	return format == "csv" || format == "json"
}

// writeSinkFileStreamed writes rows pulled from next to path, holding one
// batch at a time. Returns the rows and bytes written.
func writeSinkFileStreamed(path, format string, columns []string, next func() (*common.DataSet, error)) (int64, int64, error) {
	// #nosec G304,G302 -- path is the node's configured output and has
	// already been through validateFilePath; 0644 is what runSinkFile's
	// buffered write produces, and a streamed write landing with different
	// permissions than a buffered one would be a worse defect than the
	// permissive mode.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, 0, fmt.Errorf("write %s: %w", path, err)
	}
	// Closed explicitly on the success path so a failed flush is reported;
	// this covers the error paths.
	defer f.Close() //nolint:errcheck

	counted := &countingWriter{w: bufio.NewWriterSize(f, encodeBufferSize)}

	var rows int64
	switch format {
	case "csv":
		rows, err = streamCSV(counted, columns, next)
	case "json":
		rows, err = streamJSONArray(counted, next)
	default:
		return 0, 0, fmt.Errorf("format %q cannot be streamed", format)
	}
	if err != nil {
		return 0, 0, err
	}
	if err := counted.w.Flush(); err != nil {
		return 0, 0, fmt.Errorf("write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return 0, 0, fmt.Errorf("write %s: %w", path, err)
	}
	return rows, counted.n, nil
}

func streamCSV(w io.Writer, columns []string, next func() (*common.DataSet, error)) (int64, error) {
	cw := csv.NewWriter(w)
	if err := cw.Write(columns); err != nil {
		return 0, err
	}
	rows := int64(0)
	for {
		batch, err := next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, err
		}
		for _, row := range batch.Rows {
			if err := cw.Write(csvRecordFor(row, columns)); err != nil {
				return 0, err
			}
			rows++
		}
		// Per batch rather than per row: the underlying writer is already
		// buffered, and flushing per row would defeat it.
		cw.Flush()
		if err := cw.Error(); err != nil {
			return 0, err
		}
	}
	cw.Flush()
	return rows, cw.Error()
}

// streamJSONArray reproduces json.MarshalIndent(rows, "", "  ") exactly:
// each element is marshalled with a two-space prefix so its inner fields
// land four spaces in and its closing brace two, which is what the
// whole-slice call produces.
func streamJSONArray(w io.Writer, next func() (*common.DataSet, error)) (int64, error) {
	rows := int64(0)
	for {
		batch, err := next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, err
		}
		for _, row := range batch.Rows {
			encoded, err := json.MarshalIndent(row, "  ", "  ")
			if err != nil {
				return 0, err
			}
			if rows == 0 {
				if _, err := io.WriteString(w, "[\n  "); err != nil {
					return 0, err
				}
			} else if _, err := io.WriteString(w, ",\n  "); err != nil {
				return 0, err
			}
			if _, err := w.Write(encoded); err != nil {
				return 0, err
			}
			rows++
		}
	}
	if rows == 0 {
		// json.MarshalIndent of an empty slice, matching the buffered path.
		_, err := io.WriteString(w, "[]")
		return 0, err
	}
	_, err := io.WriteString(w, "\n]")
	return rows, err
}

// countingWriter tracks bytes written so the sink can report a size
// without having the content in hand.
type countingWriter struct {
	w *bufio.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}
