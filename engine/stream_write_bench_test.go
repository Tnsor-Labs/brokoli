package engine

import (
	"fmt"
	"io"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/artifact"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// The streaming WRITE path had no benchmark, and until this change it
// had only one codec, so there was nothing to compare. Every Arrow
// figure in the tree measures the read side.
//
// BROKOLI_STREAM_CODEC is what makes this measurable at all: without a
// way to pin the codec, a benchmark cannot prove which one ran.

func benchStreamBatches(rows, batchSize int) func(func(*common.DataSet) error) error {
	return func(emit func(*common.DataSet) error) error {
		for start := 0; start < rows; start += batchSize {
			n := batchSize
			if start+n > rows {
				n = rows - start
			}
			ds := &common.DataSet{Columns: []string{"id", "name", "amount", "active", "category"}}
			for i := start; i < start+n; i++ {
				ds.Rows = append(ds.Rows, common.DataRow{
					"id":       int64(i),
					"name":     fmt.Sprintf("customer-%d", i),
					"amount":   float64(i%997) + 0.5,
					"active":   i%3 == 0,
					"category": []string{"alpha", "beta", "gamma"}[i%3],
				})
			}
			if err := emit(ds); err != nil {
				return err
			}
		}
		return nil
	}
}

// BenchmarkPutStreamWrite measures producing a spilled output batch by
// batch, which is what a source_file or source_db node does for any
// dataset over the streaming threshold.
func BenchmarkPutStreamWrite(b *testing.B) {
	for _, rows := range []int{10_000, 100_000} {
		for _, codec := range []string{"ndjson", "arrow"} {
			b.Run(fmt.Sprintf("%s/%d", codec, rows), func(b *testing.B) {
				b.Setenv(streamCodecEnv, codec)
				want := artifact.FormatNDJSON
				if codec == "arrow" {
					want = artifact.FormatArrowIPC
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					o := newNodeOutputs(artifact.NewLocalDiskStore(b.TempDir()), "bench", 1)
					ref, err := o.PutStream(
						benchStreamBatches(rows, streamBatchRows),
						func() []string { return nil },
					)
					if err != nil {
						b.Fatal(err)
					}
					// The codec is asserted, not assumed: a silent
					// fallback would make both arms measure ndjson.
					if ref.Format != want {
						b.Fatalf("wrote %q, wanted %q", ref.Format, want)
					}
					if ref.RowCount != int64(rows) {
						b.Fatalf("wrote %d rows, want %d", ref.RowCount, rows)
					}
				}
			})
		}
	}
}

// BenchmarkStreamWriteThenRead is the figure that actually matters: a
// spilled output is written once and read at least once, so the codec
// choice is only worth making if the pair is faster.
func BenchmarkStreamWriteThenRead(b *testing.B) {
	const rows = 100_000
	for _, codec := range []string{"ndjson", "arrow"} {
		b.Run(codec, func(b *testing.B) {
			b.Setenv(streamCodecEnv, codec)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				o := newNodeOutputs(artifact.NewLocalDiskStore(b.TempDir()), "bench", 1)
				ref, err := o.PutStream(
					benchStreamBatches(rows, streamBatchRows),
					func() []string { return nil },
				)
				if err != nil {
					b.Fatal(err)
				}
				batches, closer, err := o.OpenBatches(ref)
				if err != nil {
					b.Fatal(err)
				}
				got := 0
				for {
					batch, err := batches.Next()
					if err == io.EOF {
						break
					}
					if err != nil {
						b.Fatal(err)
					}
					got += len(batch.Rows)
				}
				_ = closer.Close()
				if got != rows {
					b.Fatalf("read %d rows, want %d", got, rows)
				}
			}
		})
	}
}

// TestStreamWriteEncodedSizes reports what each codec costs on the wire,
// which the blob store and every transfer of it pays.
func TestStreamWriteEncodedSizes(t *testing.T) {
	const rows = 100_000
	sizes := map[string]int64{}
	for _, codec := range []string{"ndjson", "arrow"} {
		t.Setenv(streamCodecEnv, codec)
		o := newNodeOutputs(artifact.NewLocalDiskStore(t.TempDir()), "sizes", 1)
		ref, err := o.PutStream(benchStreamBatches(rows, streamBatchRows), func() []string { return nil })
		if err != nil {
			t.Fatalf("%s: %v", codec, err)
		}
		sizes[codec] = ref.SizeBytes
	}
	t.Logf("%d rows streamed: ndjson %d bytes, arrow %d bytes (%.2fx smaller)",
		rows, sizes["ndjson"], sizes["arrow"],
		float64(sizes["ndjson"])/float64(sizes["arrow"]))
}
