package engine

// Does arrow-ipc/v1 actually beat ndjson/v1 on this workload?
//
// ADR-033 section 8 prefers Arrow "because it is columnar and
// cross-language", and Arrow is being integrated here specifically to
// remove NDJSON's decode bottleneck. That is a claim about THIS engine's
// path, so it gets measured on this engine's path rather than inherited
// from the format's general reputation.
//
// Fairness rules this file is built to obey, because getting them wrong
// makes the answer worthless:
//
//   - Typed Arrow columns, never all-strings. arrowIPCFromRows (in
//     task_dataset_arrow.go) encodes everything as strings because it
//     exists to round-trip the decoder test; benchmarking against it
//     would understate Arrow badly, since the columnar win comes
//     precisely from typed columns.
//   - A realistic row: six columns of mixed types. One int64 column
//     flatters Arrow; one free-text column flatters NDJSON.
//   - The same logical data through both codecs, so the comparison is
//     transport only.
//   - The checksum pass included, because readTaskDatasetOutput hashes
//     the whole file on both paths in production, and excluding it would
//     overstate the gap between codecs.
//
// Run: go test ./engine/ -run xxx -bench 'BenchmarkTaskDatasetCodec' -benchmem

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/arrow/memory"
)

// benchRow is the shape being measured: an analytics row, mixed types,
// six columns -- what a task actually emits, not a microbenchmark
// tailored to either format.
type benchRow struct {
	ID       int64
	Name     string
	Amount   float64
	Active   bool
	Category string
	Note     string
}

func benchRows(n int) []benchRow {
	cats := []string{"alpha", "beta", "gamma", "delta"}
	out := make([]benchRow, n)
	for i := range out {
		out[i] = benchRow{
			ID:       int64(i),
			Name:     fmt.Sprintf("customer-%d", i),
			Amount:   float64(i) * 1.5,
			Active:   i%3 != 0,
			Category: cats[i%len(cats)],
			Note:     "a short free-text note that a real row usually carries",
		}
	}
	return out
}

// benchNDJSON encodes exactly what a harness's NDJSON writer produces.
func benchNDJSON(t testing.TB, rows []benchRow) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, r := range rows {
		if err := enc.Encode(map[string]interface{}{
			"id": r.ID, "name": r.Name, "amount": r.Amount,
			"active": r.Active, "category": r.Category, "note": r.Note,
		}); err != nil {
			t.Fatal(err)
		}
	}
	return buf.Bytes()
}

// benchArrowTyped encodes the same rows with REAL typed columns --
// int64, float64, bool and string arrays -- which is what a competent
// Arrow producer emits and what the format's advantage depends on.
func benchArrowTyped(t testing.TB, rows []benchRow) []byte {
	t.Helper()
	schema := arrow.NewSchema([]arrow.Field{
		{Name: "id", Type: arrow.PrimitiveTypes.Int64},
		{Name: "name", Type: arrow.BinaryTypes.String},
		{Name: "amount", Type: arrow.PrimitiveTypes.Float64},
		{Name: "active", Type: arrow.FixedWidthTypes.Boolean},
		{Name: "category", Type: arrow.BinaryTypes.String},
		{Name: "note", Type: arrow.BinaryTypes.String},
	}, nil)

	b := array.NewRecordBuilder(memory.DefaultAllocator, schema)
	defer b.Release()
	for _, r := range rows {
		b.Field(0).(*array.Int64Builder).Append(r.ID)
		b.Field(1).(*array.StringBuilder).Append(r.Name)
		b.Field(2).(*array.Float64Builder).Append(r.Amount)
		b.Field(3).(*array.BooleanBuilder).Append(r.Active)
		b.Field(4).(*array.StringBuilder).Append(r.Category)
		b.Field(5).(*array.StringBuilder).Append(r.Note)
	}
	rec := b.NewRecord()
	defer rec.Release()

	var buf bytes.Buffer
	w := ipc.NewWriter(&buf, ipc.WithSchema(schema))
	if err := w.Write(rec); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// stageForBench writes content and returns what a truthful manifest
// would declare, so the benchmark drives the real readTaskDatasetOutput
// path including its integrity pass.
func stageForBench(t testing.TB, dir, name string, content []byte) (int64, string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	return int64(len(content)), "sha256:" + hex.EncodeToString(sum[:])
}

var benchSizes = []int{1000, 10000, 100000}

// BenchmarkTaskDatasetCodec measures the whole production read: open,
// verify size, hash every byte, decode into a DataSet.
func BenchmarkTaskDatasetCodec(b *testing.B) {
	for _, n := range benchSizes {
		rows := benchRows(n)
		nd := benchNDJSON(b, rows)
		ar := benchArrowTyped(b, rows)

		b.Run(fmt.Sprintf("ndjson/%d", n), func(b *testing.B) {
			dir := b.TempDir()
			size, sum := stageForBench(b, dir, "d.ndjson", nd)
			b.SetBytes(int64(len(nd)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				ds, err := readTaskDatasetOutput(dir, "d.ndjson", CodecNDJSON, size, sum)
				if err != nil || len(ds.Rows) != n {
					b.Fatalf("read: %v", err)
				}
			}
		})

		b.Run(fmt.Sprintf("arrow/%d", n), func(b *testing.B) {
			dir := b.TempDir()
			size, sum := stageForBench(b, dir, "d.arrow", ar)
			b.SetBytes(int64(len(ar)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				ds, err := readTaskDatasetOutput(dir, "d.arrow", CodecArrowIPC, size, sum)
				if err != nil || len(ds.Rows) != n {
					b.Fatalf("read: %v", err)
				}
			}
		})
	}
}

// BenchmarkTaskDatasetDecodeOnly isolates decoding from the hashing and
// file I/O both codecs share, so a difference can be attributed to the
// decoder rather than to bytes moved.
func BenchmarkTaskDatasetDecodeOnly(b *testing.B) {
	for _, n := range benchSizes {
		rows := benchRows(n)
		nd := benchNDJSON(b, rows)
		ar := benchArrowTyped(b, rows)

		b.Run(fmt.Sprintf("ndjson/%d", n), func(b *testing.B) {
			b.SetBytes(int64(len(nd)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := decodeNDJSONRows(bytes.NewReader(nd)); err != nil {
					b.Fatal(err)
				}
			}
		})

		b.Run(fmt.Sprintf("arrow/%d", n), func(b *testing.B) {
			b.SetBytes(int64(len(ar)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, _, err := decodeArrowIPCRows(bytes.NewReader(ar)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// TestCodecEncodedSizes reports bytes on the wire -- staging I/O and
// checksum cost both scale with it, so it is part of the decision even
// though it is not a timing.
func TestCodecEncodedSizes(t *testing.T) {
	for _, n := range benchSizes {
		rows := benchRows(n)
		nd := len(benchNDJSON(t, rows))
		ar := len(benchArrowTyped(t, rows))
		t.Logf("%7d rows: ndjson=%9d B  arrow=%9d B  arrow/ndjson=%.2fx", n, nd, ar, float64(ar)/float64(nd))
	}
}

// The benchmark is only meaningful if both codecs really did carry the
// same data -- a decoder that silently dropped rows or columns would
// look fast for the wrong reason.
func TestBenchFixturesAgreeAcrossCodecs(t *testing.T) {
	rows := benchRows(500)
	dir := t.TempDir()
	ndSize, ndSum := stageForBench(t, dir, "d.ndjson", benchNDJSON(t, rows))
	arSize, arSum := stageForBench(t, dir, "d.arrow", benchArrowTyped(t, rows))

	viaND, err := readTaskDatasetOutput(dir, "d.ndjson", CodecNDJSON, ndSize, ndSum)
	if err != nil {
		t.Fatal(err)
	}
	viaAR, err := readTaskDatasetOutput(dir, "d.arrow", CodecArrowIPC, arSize, arSum)
	if err != nil {
		t.Fatal(err)
	}
	if len(viaND.Rows) != len(viaAR.Rows) {
		t.Fatalf("row counts differ: ndjson=%d arrow=%d", len(viaND.Rows), len(viaAR.Rows))
	}
	// Compared with != rather than by rendering: an earlier version of
	// this used fmt.Sprintf("%v") and was blind to the difference that
	// mattered, since int64(2) and float64(2) both print as "2". The
	// codecs really did disagree on numeric types until that was found.
	for _, c := range []string{"id", "name", "amount", "active", "category", "note"} {
		a, n := viaAR.Rows[499][c], viaND.Rows[499][c]
		if a != n {
			t.Errorf("column %q differs by codec: arrow=%#v (%T) ndjson=%#v (%T)", c, a, a, n, n)
		}
	}
}

// benchLargeSizes reaches past what readTaskDatasetOutput will accept:
// at 1M rows this fixture is ~150 MB of NDJSON and ~101 MB of Arrow,
// both over maxTaskDatasetBytes (64 MiB), and 10M rows is over
// maxTaskDatasetRows as well. So these run against the decoders
// directly.
//
// That gap is the point rather than an inconvenience: the caps make the
// scale where a columnar transport matters most unreachable through
// this path, which is a design question about where big datasets belong
// (artifact.DatasetRef streaming) rather than a number to tune upward
// without thinking.
var benchLargeSizes = []int{1000000, 10000000}

func BenchmarkTaskDatasetDecodeLarge(b *testing.B) {
	// Raised only to see the shape of the curve past the cap. Whether
	// the cap SHOULD move is a separate question, and the memory
	// numbers below are most of the answer.
	restore := maxTaskDatasetRows
	maxTaskDatasetRows = 20_000_000
	defer func() { maxTaskDatasetRows = restore }()

	for _, n := range benchLargeSizes {
		rows := benchRows(n)
		nd := benchNDJSON(b, rows)
		ar := benchArrowTyped(b, rows)
		b.Logf("%d rows: ndjson=%d B, arrow=%d B", n, len(nd), len(ar))

		b.Run(fmt.Sprintf("ndjson/%d", n), func(b *testing.B) {
			b.SetBytes(int64(len(nd)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := decodeNDJSONRows(bytes.NewReader(nd)); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("arrow/%d", n), func(b *testing.B) {
			b.SetBytes(int64(len(ar)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, _, err := decodeArrowIPCRows(bytes.NewReader(ar)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
