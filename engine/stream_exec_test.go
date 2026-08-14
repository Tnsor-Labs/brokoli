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
	plan, ok := planTransformRules(rules)
	if !ok {
		t.Fatal("rules should be streamable")
	}
	outRef, err := streamTransformToRef(outputs, inRef, plan)
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
	plan, _ := planTransformRules([]TransformRule{
		{Type: "filter_rows", Condition: "id > 9999"}, // drops everything
	})
	outRef, err := streamTransformToRef(outputs, inRef, plan)
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
	eng.SpillThresholdBytes = 1  // everything spills
	eng.StreamThresholdBytes = 1 // everything streams

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
	eng.StreamThresholdBytes = 1

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

// TestPipeline_LazyRows_CompatAndIdioms is Milestone 1.5's contract in
// one pipeline: four downstream code nodes each consume the same large
// ref input a different way — plain iteration (lazy), double iteration
// (re-iterability), len()+indexing (transparent materialization), and
// the new emit() idiom — and every one must produce correct results.
func TestPipeline_LazyRows_CompatAndIdioms(t *testing.T) {
	dir := t.TempDir()
	eng, s := newResumeTestEngine(t)
	eng.ArtifactStore = NewLocalDiskArtifactStore(filepath.Join(dir, "artifacts"))
	eng.SpillThresholdBytes = 1
	eng.StreamThresholdBytes = 1

	gen := models.Node{ID: "gen", Type: models.NodeTypeCode, Name: "Generate",
		Capabilities: []string{models.CapabilitySource, models.CapabilityDatasetOutput},
		Config: map[string]interface{}{"script": `
out = []
for i in range(4000):
    out.append({"id": i, "v": i * 3})
output_data = {"columns": ["id", "v"], "rows": out}
`}}

	cases := []struct {
		name   string
		script string
		check  func(t *testing.T, ds *common.DataSet)
	}{
		{"iterate-lazy", `
total = 0
n = 0
for r in rows:
    total += r["v"]
    n += 1
output_data = {"columns": ["n", "total"], "rows": [{"n": n, "total": total}]}
`, func(t *testing.T, ds *common.DataSet) {
			if len(ds.Rows) != 1 || ds.Rows[0]["n"] != float64(4000) {
				t.Fatalf("iterate-lazy: %v", ds.Rows)
			}
		}},
		{"iterate-twice", `
n1 = sum(1 for r in rows)
n2 = sum(1 for r in rows)
output_data = {"columns": ["n1", "n2"], "rows": [{"n1": n1, "n2": n2}]}
`, func(t *testing.T, ds *common.DataSet) {
			if len(ds.Rows) != 1 || ds.Rows[0]["n1"] != float64(4000) || ds.Rows[0]["n2"] != float64(4000) {
				t.Fatalf("iterate-twice (re-iterability broken): %v", ds.Rows)
			}
		}},
		{"len-and-index", `
output_data = {"columns": ["count", "first", "last"], "rows": [{"count": len(rows), "first": rows[0]["id"], "last": rows[len(rows)-1]["id"]}]}
`, func(t *testing.T, ds *common.DataSet) {
			if len(ds.Rows) != 1 || ds.Rows[0]["count"] != float64(4000) || ds.Rows[0]["first"] != float64(0) || ds.Rows[0]["last"] != float64(3999) {
				t.Fatalf("len-and-index (transparent materialization broken): %v", ds.Rows)
			}
		}},
		{"emit-idiom", `
for r in rows:
    if r["id"] < 2000:
        emit({"id": r["id"], "doubled": r["v"] * 2})
`, func(t *testing.T, ds *common.DataSet) {
			if len(ds.Rows) != 2000 {
				t.Fatalf("emit-idiom: %d rows, want 2000", len(ds.Rows))
			}
			if !reflect.DeepEqual(ds.Columns, []string{"id", "doubled"}) {
				t.Fatalf("emit-idiom columns = %v, want [id doubled] (first emitted row's key order)", ds.Columns)
			}
		}},
		{"generator-output", `
output_data = {"columns": ["id"], "rows": ({"id": r["id"]} for r in rows if r["id"] >= 3000)}
`, func(t *testing.T, ds *common.DataSet) {
			if len(ds.Rows) != 1000 {
				t.Fatalf("generator-output: %d rows, want 1000", len(ds.Rows))
			}
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pipeID := "p-lazy-" + tc.name
			pipe := &models.Pipeline{
				ID: pipeID, Name: pipeID, Enabled: true,
				Nodes: []models.Node{gen, {ID: "consume", Type: models.NodeTypeCode, Name: "Consume",
					Config: map[string]interface{}{"script": tc.script}}},
				Edges: []models.Edge{{From: "gen", To: "consume"}},
			}
			if err := s.CreatePipeline(pipe); err != nil {
				t.Fatal(err)
			}
			run, err := eng.RunPipeline(pipeID)
			if err != nil {
				t.Fatal(err)
			}
			if run.Status != models.RunStatusSuccess {
				t.Fatalf("run status = %s (error: %s)", run.Status, run.Error)
			}
			ds, err := eng.ArtifactStore.ReadArtifact(run.ID, "consume", "")
			if err != nil {
				t.Fatalf("read consume artifact: %v", err)
			}
			tc.check(t, ds)
		})
	}
}

// TestPipeline_EmitZeroRows preserves the empty-output contract under the
// emit idiom: a script that calls emit() zero times must behave like a
// script producing no rows, not leave a half-open file behind.
func TestPipeline_EmitZeroRows(t *testing.T) {
	dir := t.TempDir()
	eng, s := newResumeTestEngine(t)
	eng.ArtifactStore = NewLocalDiskArtifactStore(filepath.Join(dir, "artifacts"))
	eng.SpillThresholdBytes = 1
	eng.StreamThresholdBytes = 1

	pipe := &models.Pipeline{
		ID: "p-emit-zero", Name: "Emit Zero", Enabled: true,
		Nodes: []models.Node{
			{ID: "gen", Type: models.NodeTypeCode, Name: "Gen",
				Capabilities: []string{models.CapabilitySource, models.CapabilityDatasetOutput},
				Config: map[string]interface{}{"script": `
out = [{"id": i} for i in range(2000)]
output_data = {"columns": ["id"], "rows": out}
`}},
			{ID: "consume", Type: models.NodeTypeCode, Name: "Consume",
				Config: map[string]interface{}{"script": `
begin_emit(["id"])
for r in rows:
    if r["id"] > 99999:
        emit(r)
`}},
		},
		Edges: []models.Edge{{From: "gen", To: "consume"}},
	}
	if err := s.CreatePipeline(pipe); err != nil {
		t.Fatal(err)
	}
	run, err := eng.RunPipeline("p-emit-zero")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %s (error: %s)", run.Status, run.Error)
	}
	nodeRuns, err := s.ListNodeRunsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, nr := range nodeRuns {
		if nr.NodeID == "consume" && nr.RowCount != 0 {
			t.Fatalf("consume row_count = %d, want 0", nr.RowCount)
		}
	}
}

// TestStreamAggregation_EquivalentToBatch holds the incremental
// aggregation to byte-identical results against transform.go's batch
// aggregate across its edge semantics: count counting all rows regardless
// of column values, avg over only-accepted values, min/max returning 0.0
// when nothing was accepted, alias defaults (fn_col), the
// agg_fields/aggregations compat alias, first-seen group ordering, and
// suffix rules (including sort) applied after grouping.
func TestStreamAggregation_EquivalentToBatch(t *testing.T) {
	cases := []struct {
		name  string
		rules []TransformRule
	}{
		{"basic-sum-count", []TransformRule{
			{Type: "aggregate", GroupBy: []string{"region"}, AggFields: []AggField{
				{Column: "amount", Function: "sum"},
				{Column: "amount", Function: "count", Alias: "n"},
			}},
		}},
		{"all-functions", []TransformRule{
			{Type: "aggregate", GroupBy: []string{"region", "kind"}, AggFields: []AggField{
				{Column: "amount", Function: "sum"},
				{Column: "amount", Function: "avg"},
				{Column: "amount", Function: "min"},
				{Column: "amount", Function: "max"},
				{Column: "mixed", Function: "avg", Alias: "mixed_avg"},
				{Column: "mixed", Function: "min", Alias: "mixed_min"},
			}},
		}},
		{"aggregations-compat-alias", []TransformRule{
			{Type: "aggregate", GroupBy: []string{"region"}, Aggregations: []AggField{
				{Column: "amount", Function: "sum"},
			}},
		}},
		{"prefix-then-agg", []TransformRule{
			{Type: "filter_rows", Condition: "id >= 500"},
			{Type: "add_column", Name: "half", Expression: "amount / 2"},
			{Type: "aggregate", GroupBy: []string{"region"}, AggFields: []AggField{
				{Column: "half", Function: "sum"},
			}},
		}},
		{"agg-then-sort-suffix", []TransformRule{
			{Type: "aggregate", GroupBy: []string{"region"}, AggFields: []AggField{
				{Column: "amount", Function: "sum", Alias: "total"},
			}},
			{Type: "sort", Columns: []string{"total"}, Ascending: false},
			{Type: "filter_rows", Condition: "total > 0"},
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outputs := newStreamTestOutputs(t)
			in := &common.DataSet{Columns: []string{"id", "region", "kind", "amount", "mixed"}}
			for i := 0; i < 3200; i++ {
				var mixed interface{} = float64(i)
				if i%3 == 0 {
					mixed = "not-a-number" // exercised the toAggFloat-rejected path
				}
				in.Rows = append(in.Rows, common.DataRow{
					"id": float64(i), "region": fmt.Sprintf("r%d", i%5),
					"kind": fmt.Sprintf("k%d", i%2), "amount": float64(i) * 1.25, "mixed": mixed,
				})
			}

			// Batch ground truth.
			batchDS := &common.DataSet{Columns: append([]string{}, in.Columns...)}
			for _, row := range in.Rows {
				nr := make(common.DataRow, len(row))
				for k, v := range row {
					nr[k] = v
				}
				batchDS.Rows = append(batchDS.Rows, nr)
			}
			if err := ApplyTransforms(tc.rules, batchDS); err != nil {
				t.Fatalf("batch: %v", err)
			}

			// Streamed.
			if err := outputs.Put("up", in); err != nil {
				t.Fatal(err)
			}
			inRef, ok := outputs.GetRef("up")
			if !ok {
				t.Fatal("input did not spill")
			}
			plan, ok := planTransformRules(tc.rules)
			if !ok {
				t.Fatal("rules should plan as streamable")
			}
			outRef, err := streamTransformToRef(outputs, inRef, plan)
			if err != nil {
				t.Fatalf("streamed: %v", err)
			}
			outputs.PutRef("down", outRef)
			streamedDS, ok, err := outputs.Get("down")
			if err != nil || !ok {
				t.Fatalf("materialize: ok=%v err=%v", ok, err)
			}

			if !reflect.DeepEqual(streamedDS.Columns, batchDS.Columns) {
				t.Fatalf("columns: streamed=%v batch=%v", streamedDS.Columns, batchDS.Columns)
			}
			if len(streamedDS.Rows) != len(batchDS.Rows) {
				t.Fatalf("rows: streamed=%d batch=%d", len(streamedDS.Rows), len(batchDS.Rows))
			}
			for i := range streamedDS.Rows {
				for _, col := range batchDS.Columns {
					sv, bv := streamedDS.Rows[i][col], batchDS.Rows[i][col]
					// count is int in batch; the NDJSON round-trip makes it
					// float64 — compare numerically.
					sf, sok := toAggFloat(sv)
					bf, bok := toAggFloat(bv)
					if sok && bok {
						if sf != bf {
							t.Fatalf("row %d col %s: streamed=%v batch=%v", i, col, sv, bv)
						}
					} else if !reflect.DeepEqual(sv, bv) {
						t.Fatalf("row %d col %s: streamed=%v (%T) batch=%v (%T)", i, col, sv, sv, bv, bv)
					}
				}
			}
		})
	}
}

func TestPlanTransformRules(t *testing.T) {
	// Blocking before aggregate: not streamable.
	if _, ok := planTransformRules([]TransformRule{{Type: "sort", Column: "a"}, {Type: "aggregate", GroupBy: []string{"a"}, AggFields: []AggField{{Column: "a", Function: "sum"}}}}); ok {
		t.Fatal("sort before aggregate must not stream")
	}
	// Aggregate then anything: streamable.
	plan, ok := planTransformRules([]TransformRule{
		{Type: "filter_rows", Condition: "x > 1"},
		{Type: "aggregate", GroupBy: []string{"a"}, AggFields: []AggField{{Column: "a", Function: "sum"}}},
		{Type: "sort", Columns: []string{"sum_a"}},
		{Type: "deduplicate"},
	})
	if !ok || plan.agg == nil || len(plan.prefix) != 1 || len(plan.suffix) != 2 {
		t.Fatalf("plan = %+v, ok=%v", plan, ok)
	}
}

// TestPipeline_FanOutReplayFromRef proves ADR-019 Milestone 2's fan-out
// property: one node's ref output consumed by TWO downstream streamable
// consumers replays independently and correctly for each — no shared
// cursor, no interference — because each consumer opens the blob afresh.
func TestPipeline_FanOutReplayFromRef(t *testing.T) {
	dir := t.TempDir()
	eng, s := newResumeTestEngine(t)
	eng.ArtifactStore = NewLocalDiskArtifactStore(filepath.Join(dir, "artifacts"))
	eng.SpillThresholdBytes = 1
	eng.StreamThresholdBytes = 1

	pipe := &models.Pipeline{
		ID: "p-fanout-replay", Name: "Fan-out replay", Enabled: true,
		Nodes: []models.Node{
			{ID: "gen", Type: models.NodeTypeCode, Name: "Gen",
				Capabilities: []string{models.CapabilitySource, models.CapabilityDatasetOutput},
				Config: map[string]interface{}{"script": `
begin_emit(["id", "region", "amount"])
for i in range(3000):
    emit({"id": i, "region": "r" + str(i % 4), "amount": float(i)})
`}},
			{ID: "sum_by_region", Type: models.NodeTypeTransform, Name: "Sum",
				Config: map[string]interface{}{"rules": []interface{}{
					map[string]interface{}{"type": "aggregate", "group_by": []interface{}{"region"},
						"agg_fields": []interface{}{map[string]interface{}{"column": "amount", "function": "sum", "alias": "total"}}},
				}}},
			{ID: "big_only", Type: models.NodeTypeTransform, Name: "Big",
				Config: map[string]interface{}{"rules": []interface{}{
					map[string]interface{}{"type": "filter_rows", "condition": "id >= 2900"},
				}}},
		},
		Edges: []models.Edge{{From: "gen", To: "sum_by_region"}, {From: "gen", To: "big_only"}},
	}
	if err := s.CreatePipeline(pipe); err != nil {
		t.Fatal(err)
	}
	run, err := eng.RunPipeline("p-fanout-replay")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %s (error: %s)", run.Status, run.Error)
	}
	byNode := map[string]int{}
	nodeRuns, _ := s.ListNodeRunsByRun(run.ID)
	for _, nr := range nodeRuns {
		byNode[nr.NodeID] = nr.RowCount
	}
	if byNode["gen"] != 3000 || byNode["sum_by_region"] != 4 || byNode["big_only"] != 100 {
		t.Fatalf("row counts = %v, want gen=3000 sum=4 big=100", byNode)
	}
	// Both consumers' artifacts hold correct, independent results.
	sums, err := eng.ArtifactStore.ReadArtifact(run.ID, "sum_by_region", "")
	if err != nil || len(sums.Rows) != 4 {
		t.Fatalf("sum artifact: err=%v rows=%v", err, sums)
	}
	big, err := eng.ArtifactStore.ReadArtifact(run.ID, "big_only", "")
	if err != nil || len(big.Rows) != 100 {
		t.Fatalf("big artifact: err=%v rows=%d", err, len(big.Rows))
	}
}
