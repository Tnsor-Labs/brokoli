package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/artifact"
	"github.com/Tnsor-Labs/brokoli/store"
)

// A node's output is stored in its single stored input's format (#641).
//
// Attested lineage rests on a node that changed nothing storing the same
// bytes it was given. Those two blobs used to be written by encoders that
// chose their format independently: the stream writer from
// BROKOLI_STREAM_CODEC or its first batch, the spill from the whole
// dataset. With the stream codec on NDJSON, a dedup that removed nothing
// stored Arrow for NDJSON input -- the same rows, a different digest -- and
// no edge through it could ever be attested.

func runStoredPipeline(t *testing.T, codec string, nodes []models.Node, edges []models.Edge) map[string]models.NodeProvenance {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("BROKOLI_ARTIFACT_DIR", filepath.Join(dir, "artifacts"))
	t.Setenv("BROKOLI_STREAM_CODEC", codec)
	s, err := store.NewSQLiteStore(filepath.Join(dir, "fmt.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	// Mixed column types, and enough rows to span several batches.
	var b strings.Builder
	b.WriteString("id,amount,label,ratio\n")
	for i := 1; i <= 5000; i++ {
		fmt.Fprintf(&b, "%d,%d,name-%d,%d.5\n", i, i*10, i, i)
	}
	for i := range nodes {
		if nodes[i].Type == models.NodeTypeSourceFile {
			path := filepath.Join(dir, nodes[i].ID+".csv")
			if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
				t.Fatal(err)
			}
			nodes[i].Config = map[string]interface{}{"path": path, "format": "csv"}
		}
		if nodes[i].Type == models.NodeTypeSinkFile {
			nodes[i].Config = map[string]interface{}{"path": filepath.Join(dir, nodes[i].ID+".out.csv"), "format": "csv"}
		}
	}
	pipe := &models.Pipeline{ID: "p-fmt", Name: "p-fmt", Enabled: true, Nodes: nodes, Edges: edges,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := s.CreatePipeline(pipe); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
	eng := drainEngineOnCleanup(t, NewEngine(s))
	eng.SpillThresholdBytes = 1 // every output stored
	run, err := eng.RunPipeline(pipe.ID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if f := waitForRun(t, s, run.ID); f.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %s, want success", f.Status)
	}
	recs, err := s.GetRunProvenance(run.ID)
	if err != nil {
		t.Fatalf("get provenance: %v", err)
	}
	by := map[string]models.NodeProvenance{}
	for _, r := range recs {
		by[r.NodeID] = r
	}
	return by
}

func TestANoOpNodeKeepsItsInputsFormatAndDigest(t *testing.T) {
	for _, codec := range []string{"ndjson", ""} {
		t.Run("codec="+codec, func(t *testing.T) {
			by := runStoredPipeline(t, codec,
				[]models.Node{
					{ID: "src", Type: models.NodeTypeSourceFile},
					{ID: "dedup", Type: models.NodeTypeTransform, Config: map[string]interface{}{"rules": []interface{}{
						map[string]interface{}{"type": "deduplicate", "columns": []interface{}{"id"}},
					}}},
					{ID: "dst", Type: models.NodeTypeSinkFile},
				},
				[]models.Edge{{From: "src", To: "dedup"}, {From: "dedup", To: "dst"}})

			rec := by["dedup"]
			if len(rec.Inputs) != 1 || rec.Output == nil {
				t.Fatalf("dedup provenance = %+v, want one input and an output", rec)
			}
			in, out := rec.Inputs[0], *rec.Output
			if in.Digest == "" || in.Format == "" || out.Format == "" {
				t.Fatalf("stored datasets must carry a digest and a format: in=%+v out=%+v", in, out)
			}
			if out.Format != in.Format {
				t.Errorf("dedup stored its output as %q for input stored as %q; a node with one stored "+
					"input must follow its format", out.Format, in.Format)
			}
			if out.Digest != in.Digest {
				t.Errorf("a dedup that removed nothing stored %s for input %s: the same rows, different bytes",
					out.Digest, in.Digest)
			}
			if codec == "ndjson" && in.Format != artifact.FormatNDJSON {
				t.Errorf("with the stream codec on ndjson the source stored %q; the case is not exercising #641", in.Format)
			}
		})
	}
}

// With two stored inputs there is no single format to follow, and an
// output matching one input could not be attested anyway. The node's own
// choice stands.
func TestANodeWithTwoStoredInputsFollowsNeither(t *testing.T) {
	by := runStoredPipeline(t, "ndjson",
		[]models.Node{
			{ID: "a", Type: models.NodeTypeSourceFile},
			{ID: "b", Type: models.NodeTypeSourceFile},
			{ID: "u", Type: models.NodeTypeUnion},
			{ID: "dst", Type: models.NodeTypeSinkFile},
		},
		[]models.Edge{{From: "a", To: "u"}, {From: "b", To: "u"}, {From: "u", To: "dst"}})

	rec := by["u"]
	if len(rec.Inputs) != 2 || rec.Output == nil {
		t.Fatalf("union provenance = %+v, want two inputs and an output", rec)
	}
	for _, in := range rec.Inputs {
		if in.Format != artifact.FormatNDJSON {
			t.Fatalf("input from %s stored as %q, want ndjson; the case is not exercising a hint", in.Node, in.Format)
		}
	}
	if rec.Output.Format != artifact.FormatArrowIPC {
		t.Errorf("union output stored as %q, want arrow-ipc: a node with two inputs must not take one input's format",
			rec.Output.Format)
	}
}
