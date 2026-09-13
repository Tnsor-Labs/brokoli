package engine

import (
	"context"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/artifact"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// The cap exists to stop one artifact becoming an unbounded column value.
// WriteArtifactRef checks ref.SizeBytes, which is the BLOB's size, and
// for a compact format that is not the number that lands in the column:
// the blob is Arrow, the column is NDJSON, and the gap grows with how
// compressible the data is. Measured on a fleet, 300,000 rows were a
// 13 MB Arrow blob and a 27 MB row.
//
// So a ref comfortably under the cap could write a row well over it, and
// nothing would notice.

// wideDataset builds rows whose NDJSON is far larger than their Arrow,
// which is what a real table with repeated low-cardinality strings looks
// like: Arrow dictionary-free but columnar and typed, NDJSON repeating
// every key name on every row.
func wideDataset(rows int) *common.DataSet {
	cols := []string{
		"customer_identifier", "transaction_category", "settlement_status",
		"originating_region", "counterparty_name",
	}
	ds := &common.DataSet{Columns: cols}
	for i := 0; i < rows; i++ {
		ds.Rows = append(ds.Rows, common.DataRow{
			"customer_identifier":  int64(i),
			"transaction_category": "category-alpha",
			"settlement_status":    "settled",
			"originating_region":   "region-north",
			"counterparty_name":    "counterparty-with-a-long-name",
		})
	}
	return ds
}

func TestSQLArtifactCapMeasuresTheBytesActuallyStored(t *testing.T) {
	s := newSQLArtifactTestStore(t)
	ds := wideDataset(20000)

	outputs := newNodeOutputs(s.Blobs(), "run-cap", 1)
	ref, err := outputs.spill(ds)
	if err != nil {
		t.Fatalf("spill: %v", err)
	}
	if ref.Format != artifact.FormatArrowIPC {
		t.Fatalf("precondition: spill chose %q, wanted arrow", ref.Format)
	}

	// Encode it as NDJSON to learn what the column would actually hold,
	// then set the cap between the two. This is the window the old check
	// could not see: the blob passes, the row does not.
	var stored int64
	if b, err := ndjsonBytesForTextColumn(mustOpen(t, s, ref), ref, "n1"); err == nil {
		stored = int64(len(b))
	} else {
		t.Fatalf("convert: %v", err)
	}
	if stored <= ref.SizeBytes {
		t.Skipf("this dataset did not expand (blob %d, stored %d); nothing to guard here",
			ref.SizeBytes, stored)
	}
	t.Logf("blob %d bytes, column would hold %d bytes (%.1fx)",
		ref.SizeBytes, stored, float64(stored)/float64(ref.SizeBytes))

	// A cap above the blob but below the stored bytes.
	between := (ref.SizeBytes + stored) / 2
	t.Setenv("BROKOLI_SQL_ARTIFACT_MAX_BYTES", itoa(between))

	err = s.WriteArtifactRef("run-cap", "n1", "", ref)
	if err == nil {
		t.Fatalf("a ref whose column value is %d bytes was written under a %d-byte cap",
			stored, between)
	}
	if !strings.Contains(err.Error(), "n1") {
		t.Errorf("error %q does not name the node", err.Error())
	}
}

// And the cap must not reject what genuinely fits, or every ordinary
// artifact stops being resumable.
func TestSQLArtifactCapAcceptsWhatFits(t *testing.T) {
	s := newSQLArtifactTestStore(t)
	ds := wideDataset(2000)
	outputs := newNodeOutputs(s.Blobs(), "run-fits", 1)
	ref, err := outputs.spill(ds)
	if err != nil {
		t.Fatalf("spill: %v", err)
	}
	// Deliberately generous: the point is that a normal artifact passes.
	t.Setenv("BROKOLI_SQL_ARTIFACT_MAX_BYTES", itoa(256<<20))
	if err := s.WriteArtifactRef("run-fits", "n1", "", ref); err != nil {
		t.Fatalf("an artifact well under the cap was refused: %v", err)
	}
	got, err := s.ReadArtifact("run-fits", "n1", "")
	if err != nil {
		t.Fatalf("ReadArtifact: %v", err)
	}
	if len(got.Rows) != 2000 {
		t.Errorf("read back %d rows, want 2000", len(got.Rows))
	}
}

func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}

func mustOpen(t *testing.T, s *SQLArtifactStore, ref *artifact.DatasetRef) io.ReadCloser {
	t.Helper()
	rc, err := s.Blobs().Open(context.Background(), &ref.ArtifactRef)
	if err != nil {
		t.Fatalf("open blob: %v", err)
	}
	t.Cleanup(func() { _ = rc.Close() })
	return rc
}
