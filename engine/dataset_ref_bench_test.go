package engine

import (
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/artifact"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// The DatasetRef streaming path had no benchmark. Every Arrow-vs-NDJSON
// figure in the tree ("6-8x", "6.8x decode") is prose in CHANGELOG and
// code comments, measured on the task-dataset codecs, which are capped
// well below the sizes where a columnar transport matters. This measures
// the path a spilled node output actually takes: spill, then read it
// back batch at a time through OpenBatches, which is what every
// streaming consumer does.

func benchRefDataset(rows int) *common.DataSet {
	ds := &common.DataSet{Columns: []string{"id", "name", "amount", "active", "category", "note"}}
	for i := 0; i < rows; i++ {
		ds.Rows = append(ds.Rows, common.DataRow{
			"id":       int64(i),
			"name":     fmt.Sprintf("customer-%d", i),
			"amount":   float64(i%997) + 0.5,
			"active":   i%3 == 0,
			"category": []string{"alpha", "beta", "gamma"}[i%3],
			"note":     "a short note that is representative of a text column",
		})
	}
	return ds
}

// spillAs forces a format so the two codecs can be compared over the
// same rows. nodeOutputs.spill picks Arrow whenever it can, so the
// NDJSON arm has to be constructed rather than coaxed.
func spillAs(b *testing.B, outputs *nodeOutputs, ds *common.DataSet, format string) *artifact.DatasetRef {
	b.Helper()
	encode := func(w io.Writer) error { return EncodeNDJSON(w, ds) }
	mediaType := artifact.MediaTypeNDJSON
	if format == artifact.FormatArrowIPC {
		encode = func(w io.Writer) error { return EncodeArrowIPC(w, ds) }
		mediaType = artifact.MediaTypeArrowIPC
	}
	pr, pw := io.Pipe()
	go func() { _ = pw.CloseWithError(encode(pw)) }()
	ref, err := outputs.blobs.Put(context.Background(), outputs.namespace, pr, artifact.PutOptions{MediaType: mediaType})
	if err != nil {
		b.Fatalf("put %s: %v", format, err)
	}
	return &artifact.DatasetRef{
		ArtifactRef: *ref, Format: format,
		Columns: ds.Columns, RowCount: int64(len(ds.Rows)),
	}
}

func BenchmarkDatasetRefRead(b *testing.B) {
	for _, rows := range []int{10_000, 100_000} {
		ds := benchRefDataset(rows)
		for _, format := range []string{artifact.FormatNDJSON, artifact.FormatArrowIPC} {
			b.Run(fmt.Sprintf("%s/%d", format, rows), func(b *testing.B) {
				outputs := newNodeOutputs(artifact.NewLocalDiskStore(b.TempDir()), "bench", 1)
				ref := spillAs(b, outputs, ds, format)

				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					batches, closer, err := outputs.OpenBatches(ref)
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
}

// The size on the wire, which is what the blob store and every transfer
// of it actually pays.
func TestDatasetRefEncodedSizes(t *testing.T) {
	for _, rows := range []int{10_000, 100_000} {
		ds := benchRefDataset(rows)
		outputs := newNodeOutputs(artifact.NewLocalDiskStore(t.TempDir()), "sizes", 1)
		sizes := map[string]int64{}
		for _, format := range []string{artifact.FormatNDJSON, artifact.FormatArrowIPC} {
			encode := func(w io.Writer) error { return EncodeNDJSON(w, ds) }
			if format == artifact.FormatArrowIPC {
				encode = func(w io.Writer) error { return EncodeArrowIPC(w, ds) }
			}
			pr, pw := io.Pipe()
			go func() { _ = pw.CloseWithError(encode(pw)) }()
			ref, err := outputs.blobs.Put(context.Background(), outputs.namespace, pr, artifact.PutOptions{})
			if err != nil {
				t.Fatalf("put %s: %v", format, err)
			}
			sizes[format] = ref.SizeBytes
		}
		t.Logf("%d rows: ndjson %d bytes, arrow %d bytes (%.2fx smaller)",
			rows, sizes[artifact.FormatNDJSON], sizes[artifact.FormatArrowIPC],
			float64(sizes[artifact.FormatNDJSON])/float64(sizes[artifact.FormatArrowIPC]))
	}
}
