package engine

import (
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/artifact"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// TestNDJSONBatchReader_EquivalentToDecodeArrowJSON is the foundational
// equivalence property of ADR-019 Milestone 1: reading a stream in
// batches must reconstruct exactly what DecodeArrowJSON reads at once —
// same rows, same order, same column handling — across batch-boundary
// row counts, including the "[]" empty sentinel.
func TestNDJSONBatchReader_EquivalentToDecodeArrowJSON(t *testing.T) {
	for _, rowCount := range []int{0, 1, 2, 3, 4, 7, 1000, 1001, 2500} {
		t.Run(fmt.Sprintf("%drows", rowCount), func(t *testing.T) {
			ds := &common.DataSet{Columns: []string{"id", "name"}}
			for i := 0; i < rowCount; i++ {
				ds.Rows = append(ds.Rows, common.DataRow{"id": float64(i), "name": fmt.Sprintf("row-%d", i)})
			}
			var buf bytes.Buffer
			if err := EncodeArrowJSON(&buf, ds); err != nil {
				t.Fatalf("encode: %v", err)
			}
			encoded := buf.Bytes()

			whole, err := DecodeArrowJSON(bytes.NewReader(encoded), ds.Columns)
			if err != nil {
				t.Fatalf("DecodeArrowJSON: %v", err)
			}

			batches := NewNDJSONBatchReader(bytes.NewReader(encoded), ds.Columns, 3)
			var streamed []common.DataRow
			for {
				batch, err := batches.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatalf("Next: %v", err)
				}
				if len(batch.Rows) == 0 {
					t.Fatal("a returned batch must never be empty")
				}
				if len(batch.Rows) > 3 {
					t.Fatalf("batch of %d rows exceeds the requested size 3", len(batch.Rows))
				}
				streamed = append(streamed, batch.Rows...)
			}

			if len(streamed) != len(whole.Rows) {
				t.Fatalf("streamed %d rows, DecodeArrowJSON read %d", len(streamed), len(whole.Rows))
			}
			for i := range streamed {
				if !reflect.DeepEqual(streamed[i], whole.Rows[i]) {
					t.Fatalf("row %d differs: streamed=%v whole=%v", i, streamed[i], whole.Rows[i])
				}
			}
		})
	}
}

func TestNDJSONBatchReader_MalformedInputErrsLoudly(t *testing.T) {
	// DecodeArrowJSON silently truncates at a bad line; the batch reader
	// must not — earlier batches have already been consumed downstream by
	// the time corruption appears.
	input := "{\"a\":1}\n{\"a\":2}\nnot json at all\n"
	batches := NewNDJSONBatchReader(strings.NewReader(input), []string{"a"}, 2)
	if _, err := batches.Next(); err != nil {
		t.Fatalf("first (well-formed) batch: %v", err)
	}
	if _, err := batches.Next(); err == nil || err == io.EOF {
		t.Fatalf("expected a loud decode error at the malformed line, got %v", err)
	}
}

func TestRowLocalTransformRules(t *testing.T) {
	rowLocal := []TransformRule{
		{Type: "add_column", Name: "x", Expression: "'v'"},
		{Type: "filter_rows", Condition: "id > 1"},
		{Type: "rename_columns", Mapping: map[string]string{"a": "b"}},
		{Type: "drop_columns", Columns: []string{"a"}},
	}
	if !rowLocalTransformRules(rowLocal) {
		t.Fatal("all-row-local rule list misclassified as blocking")
	}
	for _, blocking := range []string{"sort", "deduplicate", "aggregate"} {
		mixed := append(append([]TransformRule{}, rowLocal...), TransformRule{Type: blocking})
		if rowLocalTransformRules(mixed) {
			t.Fatalf("rule list containing %q misclassified as row-local", blocking)
		}
	}
}

// newStreamTestOutputs builds a real nodeOutputs over a temp local-disk
// blob store, with a tiny threshold so everything spills/streams.
func newStreamTestOutputs(t *testing.T) *nodeOutputs {
	t.Helper()
	blobs := artifact.NewLocalDiskStore(filepath.Join(t.TempDir(), "blobs"))
	return newNodeOutputs(blobs, "stream-test-run", 1)
}

// TestStreamTransformToRef_EquivalentToBatchTransform is the operator
// equivalence property: the streamed transform must produce exactly what
// ApplyTransforms over the whole dataset produces — including dropped
// rows, added columns, and the output column order.
func TestStreamTransformToRef_EquivalentToBatchTransform(t *testing.T) {
	outputs := newStreamTestOutputs(t)

	in := &common.DataSet{Columns: []string{"id", "region", "amount"}}
	for i := 0; i < 2500; i++ {
		in.Rows = append(in.Rows, common.DataRow{
			"id": float64(i), "region": fmt.Sprintf("r%d", i%7), "amount": float64(i) * 1.5,
		})
	}
	rules := []TransformRule{
		{Type: "filter_rows", Condition: "id >= 100"},
		{Type: "add_column", Name: "tag", Expression: "'streamed'"},
		{Type: "drop_columns", Columns: []string{"region"}},
	}

	// Batch reference result.
	batchDS := &common.DataSet{Columns: append([]string{}, in.Columns...)}
	for _, row := range in.Rows {
		nr := make(common.DataRow, len(row))
		for k, v := range row {
			nr[k] = v
		}
		batchDS.Rows = append(batchDS.Rows, nr)
	}
	if err := ApplyTransforms(rules, batchDS); err != nil {
		t.Fatalf("batch ApplyTransforms: %v", err)
	}

	// Streamed result: put the input in as a ref first (Put spills — the
	// threshold is 1 byte), then stream it.
	if err := outputs.Put("up", in); err != nil {
		t.Fatalf("Put: %v", err)
	}
	inRef, ok := outputs.GetRef("up")
	if !ok {
		t.Fatal("input did not spill to a ref despite a 1-byte threshold")
	}
	outRef, err := streamTransformToRef(outputs, inRef, rules)
	if err != nil {
		t.Fatalf("streamTransformToRef: %v", err)
	}
	if outRef.RowCount != int64(len(batchDS.Rows)) {
		t.Fatalf("ref.RowCount = %d, want %d", outRef.RowCount, len(batchDS.Rows))
	}

	outputs.PutRef("down", outRef)
	streamedDS, ok, err := outputs.Get("down")
	if err != nil || !ok {
		t.Fatalf("materialize streamed output: ok=%v err=%v", ok, err)
	}
	if !reflect.DeepEqual(streamedDS.Columns, batchDS.Columns) {
		t.Fatalf("columns differ: streamed=%v batch=%v", streamedDS.Columns, batchDS.Columns)
	}
	if len(streamedDS.Rows) != len(batchDS.Rows) {
		t.Fatalf("row counts differ: streamed=%d batch=%d", len(streamedDS.Rows), len(batchDS.Rows))
	}
	for i := range streamedDS.Rows {
		if !reflect.DeepEqual(streamedDS.Rows[i], batchDS.Rows[i]) {
			t.Fatalf("row %d differs: streamed=%v batch=%v", i, streamedDS.Rows[i], batchDS.Rows[i])
		}
	}
}

func TestStreamTransformToRef_EmptyResultUsesSentinel(t *testing.T) {
	outputs := newStreamTestOutputs(t)
	in := &common.DataSet{Columns: []string{"id"}}
	for i := 0; i < 50; i++ {
		in.Rows = append(in.Rows, common.DataRow{"id": float64(i)})
	}
	if err := outputs.Put("up", in); err != nil {
		t.Fatalf("Put: %v", err)
	}
	inRef, _ := outputs.GetRef("up")
	outRef, err := streamTransformToRef(outputs, inRef, []TransformRule{
		{Type: "filter_rows", Condition: "id > 9999"}, // drops everything
	})
	if err != nil {
		t.Fatalf("streamTransformToRef: %v", err)
	}
	if outRef.RowCount != 0 {
		t.Fatalf("RowCount = %d, want 0", outRef.RowCount)
	}
	outputs.PutRef("down", outRef)
	ds, ok, err := outputs.Get("down")
	if err != nil || !ok {
		t.Fatalf("materialize empty streamed output: ok=%v err=%v", ok, err)
	}
	if len(ds.Rows) != 0 {
		t.Fatalf("rows = %d, want 0", len(ds.Rows))
	}
}

func TestNDJSONRowCounter(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"", 0},
		{"{\"a\":1}\n", 1},
		{"{\"a\":1}\n{\"a\":2}\n", 2},
		{"{\"a\":1}\n{\"a\":2}", 2}, // no trailing newline: still two rows
	}
	for _, tc := range cases {
		c := &ndjsonRowCounter{}
		if _, err := c.Write([]byte(tc.in)); err != nil {
			t.Fatalf("write: %v", err)
		}
		if got := c.finalCount(); got != tc.want {
			t.Fatalf("finalCount(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestPipeline_StreamedCodeChain_RefsEndToEnd is the engagement proof for
// the whole Milestone 1 path, shaped exactly like the live benchmark
// pipeline (code → code chain): with a tiny spill threshold, every
// intermediate must be held BY REFERENCE (never materialized inline), the
// results must be correct, and the resume artifact for each node must be
// readable back — the full ADR-010 contract surviving streaming.
func TestPipeline_StreamedCodeChain_RefsEndToEnd(t *testing.T) {
	dir := t.TempDir()
	eng, s := newResumeTestEngine(t)
	eng.ArtifactStore = NewLocalDiskArtifactStore(filepath.Join(dir, "artifacts"))
	eng.SpillThresholdBytes = 1 // everything streams

	pipeline := &models.Pipeline{
		ID: "p-streamed-code-chain", Name: "Streamed Code Chain", Enabled: true,
		Nodes: []models.Node{
			{ID: "gen", Type: models.NodeTypeCode, Name: "Generate",
				Capabilities: []string{models.CapabilitySource, models.CapabilityDatasetOutput},
				Config: map[string]interface{}{"script": `
out = []
for i in range(5000):
    out.append({"id": i, "value": i * 2})
output_data = {"columns": ["id", "value"], "rows": out}
`}},
			{ID: "double", Type: models.NodeTypeCode, Name: "Double",
				Config: map[string]interface{}{"script": `
out = []
for r in rows:
    out.append({"id": r["id"], "value": r["value"] * 2})
output_data = {"columns": ["id", "value"], "rows": out}
`}},
		},
		Edges: []models.Edge{{From: "gen", To: "double"}},
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}

	run, err := eng.RunPipeline(pipeline.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %s, want success (error: %s)", run.Status, run.Error)
	}

	// Row counts recorded from refs must match the real data.
	nodeRuns, err := s.ListNodeRunsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, nr := range nodeRuns {
		if nr.RowCount != 5000 {
			t.Errorf("node %s row_count = %d, want 5000", nr.NodeID, nr.RowCount)
		}
	}

	// The resume artifact contract must survive streaming: both nodes'
	// outputs readable back, with correct content.
	genDS, err := eng.ArtifactStore.ReadArtifact(run.ID, "gen", "")
	if err != nil {
		t.Fatalf("read gen artifact: %v", err)
	}
	if len(genDS.Rows) != 5000 {
		t.Fatalf("gen artifact rows = %d, want 5000", len(genDS.Rows))
	}
	doubleDS, err := eng.ArtifactStore.ReadArtifact(run.ID, "double", "")
	if err != nil {
		t.Fatalf("read double artifact: %v", err)
	}
	if len(doubleDS.Rows) != 5000 {
		t.Fatalf("double artifact rows = %d, want 5000", len(doubleDS.Rows))
	}
	// Spot-check the arithmetic actually flowed through both scripts.
	for _, row := range doubleDS.Rows {
		id, _ := row["id"].(float64)
		if v, _ := row["value"].(float64); v != id*4 {
			t.Fatalf("row id=%v value=%v, want value=id*4 — data corrupted in the streamed chain", id, v)
		}
	}

	// The preview kept only its 50 rows.
	previewCols, previewRows, err := s.GetNodePreview(run.ID, "double")
	if err != nil {
		t.Fatalf("get preview: %v", err)
	}
	if len(previewRows) == 0 || len(previewRows) > 50 {
		t.Fatalf("preview rows = %d, want 1..50", len(previewRows))
	}
	if len(previewCols) == 0 {
		t.Fatal("preview lost its columns")
	}
}

// TestPipeline_StreamedTransformAfterCode covers the mixed chain: a code
// node's ref output flowing into a row-local transform that streams it,
// and a blocking (aggregate) transform after it correctly falling back to
// the batch path via materialization — the barrier behaving as ADR-019
// specifies.
func TestPipeline_StreamedTransformAfterCode(t *testing.T) {
	dir := t.TempDir()
	eng, s := newResumeTestEngine(t)
	eng.ArtifactStore = NewLocalDiskArtifactStore(filepath.Join(dir, "artifacts"))
	eng.SpillThresholdBytes = 1

	pipeline := &models.Pipeline{
		ID: "p-streamed-mixed", Name: "Streamed Mixed Chain", Enabled: true,
		Nodes: []models.Node{
			{ID: "gen", Type: models.NodeTypeCode, Name: "Generate",
				Capabilities: []string{models.CapabilitySource, models.CapabilityDatasetOutput},
				Config: map[string]interface{}{"script": `
out = []
for i in range(3000):
    out.append({"id": i, "region": "r" + str(i % 3), "amount": float(i)})
output_data = {"columns": ["id", "region", "amount"], "rows": out}
`}},
			{ID: "tag", Type: models.NodeTypeTransform, Name: "Tag",
				Config: map[string]interface{}{"rules": []interface{}{
					map[string]interface{}{"type": "filter_rows", "condition": "id >= 1000"},
					map[string]interface{}{"type": "add_column", "name": "tag", "expression": "'kept'"},
				}}},
			{ID: "agg", Type: models.NodeTypeTransform, Name: "Aggregate",
				Config: map[string]interface{}{"rules": []interface{}{
					map[string]interface{}{"type": "aggregate", "group_by": []interface{}{"region"},
						"agg_fields": []interface{}{map[string]interface{}{"column": "amount", "function": "sum"}}},
				}}},
		},
		Edges: []models.Edge{{From: "gen", To: "tag"}, {From: "tag", To: "agg"}},
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}

	run, err := eng.RunPipeline(pipeline.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %s, want success (error: %s)", run.Status, run.Error)
	}

	nodeRuns, err := s.ListNodeRunsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	byNode := map[string]int{}
	for _, nr := range nodeRuns {
		byNode[nr.NodeID] = nr.RowCount
	}
	if byNode["gen"] != 3000 {
		t.Errorf("gen row_count = %d, want 3000", byNode["gen"])
	}
	if byNode["tag"] != 2000 {
		t.Errorf("tag row_count = %d, want 2000 (filter dropped ids < 1000)", byNode["tag"])
	}
	if byNode["agg"] != 3 {
		t.Errorf("agg row_count = %d, want 3 regions", byNode["agg"])
	}
}
