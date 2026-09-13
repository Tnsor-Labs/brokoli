package engine

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/artifact"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// The streaming producers wrote NDJSON unconditionally, so the largest
// outputs in the product were the only ones that never got Arrow. These
// cover the choice, both fallbacks, the drift failure, and the one thing
// that must hold whichever codec runs: the rows come back exactly.

func streamOutputs(t *testing.T) *nodeOutputs {
	t.Helper()
	o := newNodeOutputs(artifact.NewLocalDiskStore(t.TempDir()), "stream-test", 1)
	return o
}

// emitBatches turns a slice of datasets into a PutStream producer.
func emitBatches(batches ...*common.DataSet) func(func(*common.DataSet) error) error {
	return func(emit func(*common.DataSet) error) error {
		for _, b := range batches {
			if err := emit(b); err != nil {
				return err
			}
		}
		return nil
	}
}

func typedBatch(from, n int) *common.DataSet {
	ds := &common.DataSet{Columns: []string{"id", "name", "amount", "active"}}
	for i := from; i < from+n; i++ {
		ds.Rows = append(ds.Rows, common.DataRow{
			"id": int64(i), "name": fmt.Sprintf("row-%d", i),
			"amount": float64(i) + 0.5, "active": i%2 == 0,
		})
	}
	return ds
}

// readBack reads a ref through the same OpenBatches every real consumer
// uses, so the assertion covers the decoder as well as the encoder.
func readBack(t *testing.T, o *nodeOutputs, ref *artifact.DatasetRef) []common.DataRow {
	t.Helper()
	batches, closer, err := o.OpenBatches(ref)
	if err != nil {
		t.Fatalf("open batches: %v", err)
	}
	defer closer.Close()
	var rows []common.DataRow
	for {
		b, err := batches.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("next batch: %v", err)
		}
		rows = append(rows, b.Rows...)
	}
	return rows
}

func TestPutStreamChoosesArrowForTypedColumns(t *testing.T) {
	o := streamOutputs(t)
	ref, err := o.PutStream(
		emitBatches(typedBatch(0, 1500), typedBatch(1500, 1500)),
		func() []string { return []string{"id", "name", "amount", "active"} },
	)
	if err != nil {
		t.Fatalf("PutStream: %v", err)
	}
	if ref.Format != artifact.FormatArrowIPC {
		t.Fatalf("format = %q, want %q; a uniformly typed stream is exactly what arrow is for",
			ref.Format, artifact.FormatArrowIPC)
	}
	if ref.RowCount != 3000 {
		t.Errorf("RowCount = %d, want 3000", ref.RowCount)
	}

	rows := readBack(t, o, ref)
	if len(rows) != 3000 {
		t.Fatalf("read back %d rows, want 3000", len(rows))
	}
	for _, i := range []int{0, 1, 999, 1000, 1499, 1500, 2999} {
		got := rows[i]
		if got["id"] != int64(i) {
			t.Errorf("row %d id = %v (%T), want int64 %d", i, got["id"], got["id"], i)
		}
		if got["name"] != fmt.Sprintf("row-%d", i) {
			t.Errorf("row %d name = %v", i, got["name"])
		}
		if got["amount"] != float64(i)+0.5 {
			t.Errorf("row %d amount = %v", i, got["amount"])
		}
		if got["active"] != (i%2 == 0) {
			t.Errorf("row %d active = %v", i, got["active"])
		}
	}
}

// The fallback has to be silent-but-correct rather than an error: a
// dataset arrow cannot represent is ordinary data, and NDJSON is what
// every release before this one wrote for all of them.
func TestPutStreamFallsBackToNDJSONForMixedColumns(t *testing.T) {
	mixed := &common.DataSet{
		Columns: []string{"v"},
		Rows: []common.DataRow{
			{"v": "a string"},
			{"v": int64(7)}, // same column, different type: not representable
		},
	}
	o := streamOutputs(t)
	ref, err := o.PutStream(emitBatches(mixed), func() []string { return []string{"v"} })
	if err != nil {
		t.Fatalf("PutStream: %v", err)
	}
	if ref.Format != artifact.FormatNDJSON {
		t.Fatalf("format = %q, want %q", ref.Format, artifact.FormatNDJSON)
	}
	rows := readBack(t, o, ref)
	if len(rows) != 2 || rows[0]["v"] != "a string" || rows[1]["v"] != int64(7) {
		t.Errorf("rows = %v, want the two values unchanged", rows)
	}
}

// A stream whose first batch is representable and whose later rows are
// not cannot be recovered: the schema is already on the wire. It must
// fail by name rather than write something that reads back wrong.
func TestPutStreamRefusesTypeDriftAfterTheSchemaIsCommitted(t *testing.T) {
	first := &common.DataSet{Columns: []string{"v"}, Rows: []common.DataRow{{"v": int64(1)}}}
	drifted := &common.DataSet{Columns: []string{"v"}, Rows: []common.DataRow{{"v": "now a string"}}}

	o := streamOutputs(t)
	_, err := o.PutStream(emitBatches(first, drifted), func() []string { return []string{"v"} })
	if err == nil {
		t.Fatal("a column that changed type mid-stream was accepted")
	}
	for _, want := range []string{`"v"`, "int64", "string", streamCodecEnv} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
}

// Adding or dropping a column mid-stream is the same problem: a value
// with no field to go into would otherwise vanish.
func TestPutStreamRefusesAColumnSetChange(t *testing.T) {
	first := &common.DataSet{Columns: []string{"a"}, Rows: []common.DataRow{{"a": int64(1)}}}
	wider := &common.DataSet{
		Columns: []string{"a", "b"},
		Rows:    []common.DataRow{{"a": int64(2), "b": int64(3)}},
	}
	o := streamOutputs(t)
	_, err := o.PutStream(emitBatches(first, wider), func() []string { return []string{"a"} })
	if err == nil {
		t.Fatal("a batch that added a column was accepted")
	}
	if !strings.Contains(err.Error(), "column set") || !strings.Contains(err.Error(), "b") {
		t.Errorf("error %q does not name the added column", err.Error())
	}
}

// An empty stream keeps NDJSON's sentinel, so it decodes identically to
// an empty batch-written output.
func TestPutStreamEmptyStaysNDJSON(t *testing.T) {
	o := streamOutputs(t)
	ref, err := o.PutStream(emitBatches(), func() []string { return []string{"a"} })
	if err != nil {
		t.Fatalf("PutStream: %v", err)
	}
	if ref.Format != artifact.FormatNDJSON {
		t.Fatalf("format = %q, want %q", ref.Format, artifact.FormatNDJSON)
	}
	if ref.RowCount != 0 {
		t.Errorf("RowCount = %d, want 0", ref.RowCount)
	}
	if rows := readBack(t, o, ref); len(rows) != 0 {
		t.Errorf("read back %d rows from an empty stream", len(rows))
	}

	raw, err := o.blobs.Open(context.Background(), &ref.ArtifactRef)
	if err != nil {
		t.Fatalf("open blob: %v", err)
	}
	defer raw.Close()
	b, _ := io.ReadAll(raw)
	if !bytes.Equal(b, []byte("[]")) {
		t.Errorf("empty blob = %q, want the [] sentinel EncodeNDJSON writes", b)
	}
}

// A producer error must survive the codec machinery rather than being
// replaced by a pipe error from the consumer that died with it.
func TestPutStreamReportsTheProducersError(t *testing.T) {
	boom := fmt.Errorf("the query failed")
	o := streamOutputs(t)
	_, err := o.PutStream(
		func(emit func(*common.DataSet) error) error {
			if err := emit(typedBatch(0, 10)); err != nil {
				return err
			}
			return boom
		},
		func() []string { return nil },
	)
	if err == nil || !strings.Contains(err.Error(), "the query failed") {
		t.Fatalf("error = %v, want the producer's own error", err)
	}
}

// A producer that fails before emitting anything must not hang: nothing
// has chosen a codec, and the receive that waits for that choice is on
// the caller's goroutine.
func TestPutStreamProducerFailingBeforeTheFirstBatchDoesNotHang(t *testing.T) {
	boom := fmt.Errorf("connection refused")
	o := streamOutputs(t)
	done := make(chan error, 1)
	go func() {
		_, err := o.PutStream(
			func(emit func(*common.DataSet) error) error { return boom },
			func() []string { return nil },
		)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "connection refused") {
			t.Fatalf("error = %v, want the producer's own error", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("PutStream hung when the producer failed before its first batch")
	}
}

func TestStreamCodecEnvSelection(t *testing.T) {
	for _, tc := range []struct {
		set  string
		want streamCodec
	}{
		{"", streamCodecAuto},
		{"ndjson", streamCodecNDJSON},
		{"NDJSON", streamCodecNDJSON},
		{"arrow", streamCodecArrow},
		{"arrow_ipc", streamCodecArrow},
		{" arrow ", streamCodecArrow},
		{"parquet", streamCodecAuto}, // unrecognised selects nothing on the operator's behalf
	} {
		t.Run(tc.set, func(t *testing.T) {
			t.Setenv(streamCodecEnv, tc.set)
			if got := streamCodecFromEnv(); got != tc.want {
				t.Errorf("%s=%q gave codec %d, want %d", streamCodecEnv, tc.set, got, tc.want)
			}
		})
	}
}

// ndjson is the escape hatch the drift error points at, so it has to
// actually suppress arrow for a stream arrow would otherwise take.
func TestStreamCodecNDJSONForcesTheFallback(t *testing.T) {
	t.Setenv(streamCodecEnv, "ndjson")
	o := streamOutputs(t)
	ref, err := o.PutStream(emitBatches(typedBatch(0, 100)), func() []string { return nil })
	if err != nil {
		t.Fatalf("PutStream: %v", err)
	}
	if ref.Format != artifact.FormatNDJSON {
		t.Fatalf("format = %q, want %q with the codec forced", ref.Format, artifact.FormatNDJSON)
	}
	if rows := readBack(t, o, ref); len(rows) != 100 || rows[0]["id"] != int64(0) {
		t.Errorf("forced ndjson did not round-trip: %d rows, first id %v", len(rows), rows[0]["id"])
	}
}

// arrow makes the choice observable. Without it a measurement cannot
// prove which codec ran, and a benchmark that cannot tell them apart is
// not a benchmark.
func TestStreamCodecArrowRefusesAnUnrepresentableStream(t *testing.T) {
	t.Setenv(streamCodecEnv, "arrow")
	mixed := &common.DataSet{
		Columns: []string{"v"},
		Rows:    []common.DataRow{{"v": "s"}, {"v": int64(1)}},
	}
	o := streamOutputs(t)
	_, err := o.PutStream(emitBatches(mixed), func() []string { return nil })
	if err == nil {
		t.Fatal("codec=arrow accepted a stream that cannot be represented as arrow")
	}
	if !strings.Contains(err.Error(), streamCodecEnv) {
		t.Errorf("error %q does not name the setting that caused it", err.Error())
	}
}

// streamTransformToRef is the other streaming producer, and it had its
// own hand-rolled copy of the NDJSON loop. The equivalence property
// (TestStreamTransformToRef_EquivalentToBatchTransform) covers what the
// transform computes; these cover what it writes it as.

func TestStreamTransformChoosesArrowForTypedColumns(t *testing.T) {
	outputs := newStreamTestOutputs(t)

	in := &common.DataSet{Columns: []string{"id", "region", "amount"}}
	for i := 0; i < 2500; i++ {
		in.Rows = append(in.Rows, common.DataRow{
			"id": int64(i), "region": fmt.Sprintf("r%d", i%7), "amount": float64(i) * 1.5,
		})
	}
	inputRef, err := outputs.spill(in)
	if err != nil {
		t.Fatalf("spill input: %v", err)
	}

	plan := transformStreamPlan{prefix: []TransformRule{
		{Type: "filter_rows", Condition: "id >= 100"},
	}}
	ref, err := streamTransformToRef(outputs, inputRef, plan)
	if err != nil {
		t.Fatalf("streamTransformToRef: %v", err)
	}
	if ref.Format != artifact.FormatArrowIPC {
		t.Fatalf("format = %q, want %q", ref.Format, artifact.FormatArrowIPC)
	}
	if ref.RowCount != 2400 {
		t.Fatalf("RowCount = %d, want 2400", ref.RowCount)
	}
	rows := readBack(t, outputs, ref)
	if len(rows) != 2400 {
		t.Fatalf("read back %d rows, want 2400", len(rows))
	}
	if rows[0]["id"] != int64(100) || rows[2399]["id"] != int64(2499) {
		t.Errorf("boundaries wrong: first=%v last=%v", rows[0]["id"], rows[2399]["id"])
	}
}

// A transform that filters everything away still has to write the
// sentinel, and nothing has chosen a codec by then.
func TestStreamTransformEmptyResultStaysNDJSON(t *testing.T) {
	outputs := newStreamTestOutputs(t)
	in := &common.DataSet{
		Columns: []string{"id"},
		Rows:    []common.DataRow{{"id": int64(1)}, {"id": int64(2)}},
	}
	inputRef, err := outputs.spill(in)
	if err != nil {
		t.Fatalf("spill input: %v", err)
	}
	plan := transformStreamPlan{prefix: []TransformRule{
		{Type: "filter_rows", Condition: "id > 1000"},
	}}
	ref, err := streamTransformToRef(outputs, inputRef, plan)
	if err != nil {
		t.Fatalf("streamTransformToRef: %v", err)
	}
	if ref.Format != artifact.FormatNDJSON {
		t.Errorf("format = %q, want %q for an empty result", ref.Format, artifact.FormatNDJSON)
	}
	if ref.RowCount != 0 {
		t.Errorf("RowCount = %d, want 0", ref.RowCount)
	}
	if rows := readBack(t, outputs, ref); len(rows) != 0 {
		t.Errorf("read back %d rows from an empty transform", len(rows))
	}
}

// The aggregation path emits one dataset after EOF rather than batch by
// batch, so it reaches the writer through a different branch and has to
// choose a codec there too.
func TestStreamTransformAggregatedOutputChoosesACodec(t *testing.T) {
	outputs := newStreamTestOutputs(t)
	in := &common.DataSet{Columns: []string{"region", "amount"}}
	for i := 0; i < 3000; i++ {
		in.Rows = append(in.Rows, common.DataRow{
			"region": fmt.Sprintf("r%d", i%3), "amount": float64(i),
		})
	}
	inputRef, err := outputs.spill(in)
	if err != nil {
		t.Fatalf("spill input: %v", err)
	}
	agg := TransformRule{
		Type: "aggregate", GroupBy: []string{"region"},
		Aggregations: []AggField{{Column: "amount", Function: "sum"}},
	}
	plan := transformStreamPlan{agg: &agg}
	ref, err := streamTransformToRef(outputs, inputRef, plan)
	if err != nil {
		t.Fatalf("streamTransformToRef: %v", err)
	}
	if ref.RowCount != 3 {
		t.Fatalf("RowCount = %d, want 3 groups", ref.RowCount)
	}
	rows := readBack(t, outputs, ref)
	if len(rows) != 3 {
		t.Fatalf("read back %d rows, want 3", len(rows))
	}
	// Whichever codec it chose, the grouped values must be exact. Both
	// numeric Go types are correct here: numericValue returns int64 for
	// a whole number and float64 otherwise, identically for both codecs,
	// and these sums happen to be whole.
	total := 0.0
	for _, r := range rows {
		switch v := r["sum_amount"].(type) {
		case int64:
			total += float64(v)
		case float64:
			total += v
		default:
			t.Fatalf("sum_amount is %T, want a number", r["sum_amount"])
		}
	}
	if want := float64(2999*3000) / 2; total != want {
		t.Errorf("sum of groups = %v, want %v", total, want)
	}
}
