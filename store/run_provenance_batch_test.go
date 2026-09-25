package store

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

// The batch read behind the lineage graph returns exactly the runs asked
// for, grouped by run. "Exactly" is the property worth testing: an IN
// clause that quietly matched nothing, or everything, would still return
// a well-formed map.

type provenanceBatchStore interface {
	CreatePipeline(*models.Pipeline) error
	CreateRun(*models.Run) error
	SaveNodeProvenance(*models.NodeProvenance) error
	GetNodeProvenanceForRuns([]string) (map[string][]models.NodeProvenance, error)
}

func seedProvenanceRuns(t *testing.T, s provenanceBatchStore, prefix string) (runs [3]string) {
	t.Helper()
	pipelineID := prefix + "-p"
	if err := s.CreatePipeline(&models.Pipeline{
		ID: pipelineID, Name: pipelineID, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
	started := time.Now().UTC()
	for i := range runs {
		runs[i] = fmt.Sprintf("%s-run-%d", prefix, i)
		if err := s.CreateRun(&models.Run{ID: runs[i], PipelineID: pipelineID, Status: models.RunStatusSuccess, StartedAt: &started}); err != nil {
			t.Fatalf("create run: %v", err)
		}
	}
	// run 0 has two nodes, run 1 has one, run 2 has one but is never asked for.
	for _, rec := range []models.NodeProvenance{
		{RunID: runs[0], NodeID: "a", Output: &models.DatasetFact{Digest: "sha256:0a", RowCount: 1}},
		{RunID: runs[0], NodeID: "b", Inputs: []models.DatasetFact{{Node: "a", Digest: "sha256:0a"}}},
		{RunID: runs[1], NodeID: "a", Output: &models.DatasetFact{Digest: "sha256:1a", RowCount: 2}},
		{RunID: runs[2], NodeID: "a", Output: &models.DatasetFact{Digest: "sha256:2a"}},
	} {
		rec := rec
		if err := s.SaveNodeProvenance(&rec); err != nil {
			t.Fatalf("save provenance: %v", err)
		}
	}
	return runs
}

func assertProvenanceBatch(t *testing.T, s provenanceBatchStore, runs [3]string) {
	t.Helper()
	got, err := s.GetNodeProvenanceForRuns([]string{runs[0], runs[1], "no-such-run"})
	if err != nil {
		t.Fatalf("batch read: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d run(s), want exactly the two requested that have records: %v", len(got), keysOfProvenance(got))
	}
	if _, present := got[runs[2]]; present {
		t.Error("a run that was not asked for came back")
	}
	if _, present := got["no-such-run"]; present {
		t.Error("an unknown run came back")
	}
	if n := len(got[runs[0]]); n != 2 {
		t.Errorf("run 0 returned %d record(s), want 2", n)
	}
	if recs := got[runs[1]]; len(recs) != 1 || recs[0].Output == nil || recs[0].Output.Digest != "sha256:1a" {
		t.Errorf("run 1 = %+v, want its one record with digest sha256:1a", recs)
	}
	for _, rec := range got[runs[0]] {
		if rec.RunID != runs[0] {
			t.Errorf("a record filed under run 0 names run %q", rec.RunID)
		}
	}

	empty, err := s.GetNodeProvenanceForRuns(nil)
	if err != nil || len(empty) != 0 {
		t.Errorf("no ids: got %v, %v; want an empty map", empty, err)
	}
}

func keysOfProvenance(m map[string][]models.NodeProvenance) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestProvenanceForRunsReturnsExactlyTheRunsAskedFor(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "batch.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	assertProvenanceBatch(t, s, seedProvenanceRuns(t, s, "sq"))
}

// Postgres has its own literal query, so it gets its own run rather than
// being assumed to behave like SQLite's.
func TestPostgresProvenanceForRunsReturnsExactlyTheRunsAskedFor(t *testing.T) {
	s := openAttributionTestPostgresStore(t)
	prefix := fmt.Sprintf("pgbatch-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = s.db.Exec(`DELETE FROM runs WHERE pipeline_id = $1`, prefix+"-p")
		_, _ = s.db.Exec(`DELETE FROM pipelines WHERE id = $1`, prefix+"-p")
	})
	assertProvenanceBatch(t, s, seedProvenanceRuns(t, s, prefix))
}
