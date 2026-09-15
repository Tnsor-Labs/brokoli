package engine

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// A node records the digest of the output it actually stored.
//
// The first version of the recorder ran before the engine stored a node's
// output. A node whose handler returns rows in memory -- a quality check,
// a transform, most processing nodes -- is spilled to the artifact store
// only afterwards, so its own record carried no digest while the node
// consuming it recorded one for the very same bytes. The earlier test
// compared a source node only, which streams straight into a stored ref
// and so never showed it.
//
// Two properties, checked at every edge rather than at one node type:
//
//   - what a node records for its output is what its consumer records for
//     the same dataset;
//   - a node that returns its input unchanged records the same digest on
//     both sides. That is ADR-039's first attested claim: output bytes
//     equal to input bytes provably added, dropped and rewrote nothing.
func TestANodeRecordsTheDigestOfWhatItStored(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BROKOLI_ARTIFACT_DIR", filepath.Join(dir, "artifacts"))

	s, err := store.NewSQLiteStore(filepath.Join(dir, "digest.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	in := filepath.Join(dir, "in.csv")
	if err := os.WriteFile(in, []byte("id,amount\n1,10\n2,20\n3,30\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pipe := &models.Pipeline{
		ID: "p-digest", Name: "p-digest", Enabled: true,
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceFile,
				Config: map[string]interface{}{"path": in, "format": "csv"}},
			{ID: "qc", Type: models.NodeTypeQualityCheck,
				Config: map[string]interface{}{"rules": []interface{}{
					map[string]interface{}{"column": "id", "rule": "not_null", "on_failure": "block"},
				}}},
			{ID: "sorted", Type: models.NodeTypeTransform,
				Config: map[string]interface{}{"rules": []interface{}{
					map[string]interface{}{"type": "sort", "columns": []interface{}{"id"}, "ascending": true},
				}}},
			{ID: "dst", Type: models.NodeTypeSinkFile,
				Config: map[string]interface{}{"path": filepath.Join(dir, "out.csv"), "format": "csv"}},
		},
		Edges: []models.Edge{
			{From: "src", To: "qc"}, {From: "qc", To: "sorted"}, {From: "sorted", To: "dst"},
		},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := s.CreatePipeline(pipe); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	eng := drainEngineOnCleanup(t, NewEngine(s))
	eng.SpillThresholdBytes = 1 // every output is stored, so every one has a digest

	run, err := eng.RunPipeline(pipe.ID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if finished := waitForRun(t, s, run.ID); finished.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %s, want success", finished.Status)
	}

	records, err := s.GetRunProvenance(run.ID)
	if err != nil {
		t.Fatalf("get provenance: %v", err)
	}
	byNode := map[string]models.NodeProvenance{}
	for _, p := range records {
		byNode[p.NodeID] = p
	}

	for _, id := range []string{"src", "qc", "sorted", "dst"} {
		p := byNode[id]
		for _, in := range p.Inputs {
			t.Logf("%-6s in  from %-6s digest=%s rows=%d", id, in.Node, in.Digest, in.RowCount)
		}
		if p.Output != nil {
			t.Logf("%-6s out             digest=%s rows=%d", id, p.Output.Digest, p.Output.RowCount)
		}
	}

	// Every edge: the producer's output fact and the consumer's input fact
	// describe the same stored bytes.
	for _, edge := range pipe.Edges {
		producer, consumer := byNode[edge.From], byNode[edge.To]
		if producer.Output == nil {
			t.Errorf("%s recorded no output", edge.From)
			continue
		}
		var consumed string
		for _, in := range consumer.Inputs {
			if in.Node == edge.From {
				consumed = in.Digest
			}
		}
		if consumed == "" {
			t.Errorf("%s recorded no digest for its input from %s, with every output stored", edge.To, edge.From)
			continue
		}
		if producer.Output.Digest != consumed {
			t.Errorf("%s recorded its output as %q, but %s consumed %q from it: the producer's record "+
				"does not describe what it stored", edge.From, producer.Output.Digest, edge.To, consumed)
		}
	}

	// The pass-through node: same bytes out as in.
	qc := byNode["qc"]
	if len(qc.Inputs) != 1 || qc.Output == nil || qc.Inputs[0].Digest == "" {
		t.Fatalf("quality_check provenance = %+v, want one stored input and an output", qc)
	}
	if qc.Output.Digest != qc.Inputs[0].Digest {
		t.Errorf("quality_check returned its input unchanged but recorded %q out and %q in; "+
			"the attested pass-through claim could never fire", qc.Output.Digest, qc.Inputs[0].Digest)
	}
}
