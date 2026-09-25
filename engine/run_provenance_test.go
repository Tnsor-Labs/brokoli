package engine

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// Provenance is recorded by a real run, not only by a unit test calling
// the recorder.
//
// This distinction has bitten twice in this codebase already: the
// OpenLineage emitter was correct and called from nowhere, and
// DeleteRunAttribution was documented as a cleanup path and called from
// nowhere. A recorder that is never invoked writes an empty table, and
// every test that calls it directly still passes.

func provenanceTestEngine(t *testing.T) (*Engine, store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.NewSQLiteStore(filepath.Join(dir, "prov.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	in := filepath.Join(dir, "orders.csv")
	if err := os.WriteFile(in, []byte("id,price,qty\n1,10,2\n2,20,3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pipe := &models.Pipeline{
		ID: "p-prov", Name: "p-prov", Enabled: true,
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceFile, Name: "orders",
				Config: map[string]interface{}{"path": in, "format": "csv"}},
			{ID: "tf", Type: models.NodeTypeTransform, Name: "total",
				Config: map[string]interface{}{"rules": []interface{}{
					map[string]interface{}{"type": "add_column", "name": "total", "expression": "price * qty"},
				}}},
			{ID: "dst", Type: models.NodeTypeSinkFile, Name: "out",
				Config: map[string]interface{}{"path": filepath.Join(dir, "out.csv"), "format": "csv"}},
		},
		Edges:     []models.Edge{{From: "src", To: "tf"}, {From: "tf", To: "dst"}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := s.CreatePipeline(pipe); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
	return drainEngineOnCleanup(t, NewEngine(s)), s, pipe.ID
}

func waitForRun(t *testing.T, s store.Store, runID string) *models.Run {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		run, err := s.GetRun(runID)
		if err == nil && run.Status != models.RunStatusRunning && run.Status != models.RunStatusPending {
			return run
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s never reached a terminal state", runID)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestARealRunRecordsProvenanceForEveryNode(t *testing.T) {
	eng, s, pipelineID := provenanceTestEngine(t)

	run, err := eng.RunPipeline(pipelineID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	finished := waitForRun(t, s, run.ID)
	if finished.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %s, want success", finished.Status)
	}

	records, err := s.GetRunProvenance(run.ID)
	if err != nil {
		t.Fatalf("get provenance: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("the run recorded no provenance at all. Nothing calls the recorder, " +
			"which is the defect this test exists for.")
	}

	byNode := map[string]models.NodeProvenance{}
	for _, p := range records {
		byNode[p.NodeID] = p
	}
	for _, id := range []string{"src", "tf", "dst"} {
		if _, ok := byNode[id]; !ok {
			t.Errorf("no provenance for node %s; recorded: %v", id, nodeIDsOf(records))
		}
	}

	// The source has no upstream, so no inputs, and it produced rows.
	src := byNode["src"]
	if len(src.Inputs) != 0 {
		t.Errorf("the source records %d input(s), want none", len(src.Inputs))
	}
	if src.Output == nil || src.Output.RowCount != 2 {
		t.Errorf("source output = %+v, want 2 rows", src.Output)
	}

	// The transform consumed the source and produced the derived column.
	tf := byNode["tf"]
	if len(tf.Inputs) != 1 || tf.Inputs[0].Node != "src" {
		t.Fatalf("transform inputs = %+v, want one from src", tf.Inputs)
	}
	if tf.Output == nil {
		t.Fatal("the transform recorded no output")
	}
	if !hasString(tf.Output.Columns, "total") {
		t.Errorf("transform output columns = %v, want the derived column", tf.Output.Columns)
	}
	if tf.Inputs[0].RowCount != 2 {
		t.Errorf("the transform consumed %d row(s), want 2", tf.Inputs[0].RowCount)
	}

	// Recorded timestamps are real.
	if tf.RecordedAt.IsZero() {
		t.Error("provenance has no recorded_at")
	}
}

// A dataset that went through the artifact store is recorded with its
// digest, on both sides of the edge.
//
// Every other test here runs datasets small enough to stay in memory,
// which never get a digest, so without this one the recorder could drop
// every digest in the product and still pass. The digest is the entire
// basis of the `attested` level. Forcing everything to spill is how the
// streaming tests exercise the same path.
//
// Both sides are compared, not only checked for presence: the input fact
// the transform records for `src` and the output fact `src` records for
// itself describe the same stored bytes, so they must carry the same
// digest. If they differ, one of the two records is describing something
// other than what was consumed.
func TestAStoredDatasetIsRecordedWithItsDigest(t *testing.T) {
	eng, s, pipelineID := provenanceTestEngine(t)
	eng.SpillThresholdBytes = 1 // everything spills

	run, err := eng.RunPipeline(pipelineID)
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

	tf, ok := byNode["tf"]
	if !ok || len(tf.Inputs) != 1 {
		t.Fatalf("transform provenance = %+v, want one input", tf)
	}
	consumed := tf.Inputs[0].Digest
	if len(consumed) < len("sha256:") || consumed[:len("sha256:")] != "sha256:" {
		t.Fatalf("the transform's input from src has digest %q, want a sha256 digest; "+
			"a spilled dataset was recorded as if it had never been stored", consumed)
	}

	src, ok := byNode["src"]
	if !ok || src.Output == nil {
		t.Fatalf("source provenance = %+v, want an output", src)
	}
	if src.Output.Digest != consumed {
		t.Errorf("src recorded its own output with digest %q, but the transform consumed %q. "+
			"The two records describe the same stored bytes and must agree.", src.Output.Digest, consumed)
	}
}

// A branch a condition routed away from is not an input.
//
// `merge` has two incoming edges: one from the active branch, one from a
// branch that was skipped. It still executes, on the active branch's
// data. Recording the skipped edge as an input would say `merge` consumed
// a dataset that was never produced -- and with a zero row count it
// would read as "the branch ran and produced nothing", a different and
// false statement.
func TestASkippedBranchIsNotRecordedAsAnInput(t *testing.T) {
	executor := newRoutingTestExecutor()
	pipeline := basicConditionalPipeline("always_true")
	pipeline.Nodes = append(pipeline.Nodes,
		models.Node{ID: "inactive-tail", Type: routingTestNode, Name: "Inactive Tail", Config: map[string]interface{}{}},
		models.Node{ID: "merge", Type: routingTestNode, Name: "Merge", Config: map[string]interface{}{}},
	)
	pipeline.Edges = append(pipeline.Edges,
		models.Edge{From: "no", To: "inactive-tail"},
		models.Edge{From: "yes", To: "merge"},
		models.Edge{From: "inactive-tail", To: "merge"},
	)

	run, s := runRoutingPipeline(t, pipeline, executor)
	if executor.callCount("merge") != 1 {
		t.Fatalf("merge ran %d time(s), want 1; the fixture does not exercise a mixed input", executor.callCount("merge"))
	}

	records, err := s.GetRunProvenance(run.ID)
	if err != nil {
		t.Fatalf("get provenance: %v", err)
	}
	var merge *models.NodeProvenance
	for i := range records {
		if records[i].NodeID == "merge" {
			merge = &records[i]
		}
		if records[i].NodeID == "no" || records[i].NodeID == "inactive-tail" {
			t.Errorf("skipped node %s recorded provenance", records[i].NodeID)
		}
	}
	if merge == nil {
		t.Fatalf("merge recorded no provenance; recorded: %v", nodeIDsOf(records))
	}
	if len(merge.Inputs) != 1 || merge.Inputs[0].Node != "yes" {
		t.Errorf("merge inputs = %+v, want only the active branch `yes`", merge.Inputs)
	}
}

// The same upstream wired twice is one dataset consumed once.
//
// If validation refuses the duplicate edge, this test fails at the run
// and the dedupe in inputFacts is unreachable code that should go.
func TestADuplicatedEdgeIsOneInput(t *testing.T) {
	eng, s, _ := provenanceTestEngine(t)

	p, err := s.GetPipeline("p-prov")
	if err != nil {
		t.Fatalf("get pipeline: %v", err)
	}
	p.ID, p.Name = "p-dup", "p-dup"
	p.Edges = append(p.Edges, models.Edge{From: "src", To: "tf"})
	if err := s.CreatePipeline(p); err != nil {
		t.Fatalf("create pipeline with a duplicated edge: %v", err)
	}

	run, err := eng.RunPipeline("p-dup")
	if err != nil {
		t.Fatalf("run with a duplicated edge: %v", err)
	}
	if finished := waitForRun(t, s, run.ID); finished.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %s, want success", finished.Status)
	}

	records, err := s.GetRunProvenance(run.ID)
	if err != nil {
		t.Fatalf("get provenance: %v", err)
	}
	for _, rec := range records {
		if rec.NodeID == "tf" && len(rec.Inputs) != 1 {
			t.Errorf("tf recorded %d input(s) for one upstream wired twice, want 1: %+v", len(rec.Inputs), rec.Inputs)
		}
	}
}

// The record dies with the run, per the ADR. Tested by purging, not by
// reading the schema.
func TestProvenanceIsPurgedWithItsRun(t *testing.T) {
	eng, s, pipelineID := provenanceTestEngine(t)

	run, err := eng.RunPipeline(pipelineID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	waitForRun(t, s, run.ID)

	if records, _ := s.GetRunProvenance(run.ID); len(records) == 0 {
		t.Fatal("nothing to purge; the run recorded no provenance")
	}

	// Age the run past the cutoff, then purge.
	sq, ok := s.(*store.SQLiteStore)
	if !ok {
		t.Fatal("expected a SQLite store")
	}
	db, ok := sq.RawDB().(*sql.DB)
	if !ok {
		t.Fatal("RawDB did not return *sql.DB")
	}
	aged := time.Now().UTC().AddDate(0, 0, -90).Format("2006-01-02 15:04:05")
	if _, err := db.Exec(`UPDATE runs SET started_at = ? WHERE id = ?`, aged, run.ID); err != nil {
		t.Fatalf("ageing the run: %v", err)
	}

	if _, err := s.PurgeRunsOlderThan(30); err != nil {
		t.Fatalf("purge: %v", err)
	}
	records, err := s.GetRunProvenance(run.ID)
	if err != nil {
		t.Fatalf("get provenance after purge: %v", err)
	}
	if len(records) != 0 {
		t.Errorf("%d provenance record(s) outlived their run", len(records))
	}
}

// A failed node records nothing: its output either does not exist or is
// a partial result nothing downstream consumed, and attesting bytes that
// never became anybody's input would be a false record.
func TestAFailedNodeRecordsNoProvenance(t *testing.T) {
	dir := t.TempDir()
	s, err := store.NewSQLiteStore(filepath.Join(dir, "fail.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	in := filepath.Join(dir, "in.csv")
	if err := os.WriteFile(in, []byte("id\n1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pipe := &models.Pipeline{
		ID: "p-fail", Name: "p-fail", Enabled: true,
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceFile,
				Config: map[string]interface{}{"path": in, "format": "csv"}},
			// A node that accepts input and fails during execution: a
			// blocking check on a column the dataset does not have.
			{ID: "boom", Type: models.NodeTypeQualityCheck,
				Config: map[string]interface{}{"rules": []interface{}{
					map[string]interface{}{"column": "no_such_column", "rule": "not_null", "on_failure": "block"},
				}}},
		},
		Edges:     []models.Edge{{From: "src", To: "boom"}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := s.CreatePipeline(pipe); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	eng := drainEngineOnCleanup(t, NewEngine(s))
	// This path reports the failing node as the run's error, which is the
	// point of the fixture, so the error is expected rather than fatal.
	run, runErr := eng.RunPipeline(pipe.ID)
	if run == nil {
		runs, err := s.ListRunsByPipeline(pipe.ID, 1)
		if err != nil || len(runs) == 0 {
			t.Fatalf("no run was created (run error: %v, list error: %v)", runErr, err)
		}
		run = &runs[0]
	}
	// The fixture has to actually fail, or this test passes for the
	// wrong reason: a node that succeeded would record provenance, but a
	// run rejected before execution would record nothing at all.
	if finished := waitForRun(t, s, run.ID); finished.Status != models.RunStatusFailed {
		t.Fatalf("run status = %s, want failed; the fixture does not exercise a failing node", finished.Status)
	}

	records, err := s.GetRunProvenance(run.ID)
	if err != nil {
		t.Fatalf("get provenance: %v", err)
	}
	for _, p := range records {
		if p.NodeID == "boom" {
			t.Errorf("the failed node recorded provenance: %+v", p)
		}
	}
	// The node that did succeed still recorded: a failure partway must
	// not lose the record of what ran before it, which is the part
	// somebody debugging the failure wants.
	if len(records) == 0 {
		t.Error("the successful node recorded nothing either")
	}
}

func nodeIDsOf(records []models.NodeProvenance) []string {
	out := make([]string, 0, len(records))
	for _, p := range records {
		out = append(out, p.NodeID)
	}
	return out
}

func hasString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
