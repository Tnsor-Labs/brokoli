package engine

// End-to-end task-bundle execution (ADR-031) through the real engine and
// its global worker pool: a task_bundle code node runs a project whose
// entry imports a helper module from inside the bundle, cross-bundle
// module cache pollution is fenced by the digest-joined pool sub-key,
// and the refuse-to-run failure modes surface as failed runs that name
// the task bundle.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/taskbundle"
	"github.com/Tnsor-Labs/brokoli/store"
)

type tbTestEngine struct {
	eng *Engine
	s   *store.SQLiteStore
}

const tbOrg = "org-task"

func newTBEngine(t *testing.T) *tbTestEngine {
	t.Helper()
	eng, s := newResumeTestEngine(t)
	return &tbTestEngine{eng: eng, s: s}
}

// bundle builds a two-file python bundle: helpers.py (apply = ×mult) and
// tasks.py which imports it, uploads it org-scoped, and returns the digest.
func (e *tbTestEngine) bundle(t *testing.T, mult int) string {
	t.Helper()
	m := &taskbundle.Manifest{
		Format:   taskbundle.Format,
		Language: "python",
		TaskName: "mult-suite",
		Entry:    "tasks.py",
		Files:    []string{"helpers.py", "tasks.py"},
	}
	archive, err := taskbundle.Assemble(map[string]string{
		"helpers.py": fmt.Sprintf("def apply(x):\n    return x * %d\n", mult),
		"tasks.py": "from helpers import apply\nout = [{\"v\": apply(int(r[\"v\"]))} for r in rows]\n" +
			"output_data = {\"columns\": [\"v\"], \"rows\": out}\n",
	}, m)
	if err != nil {
		t.Fatal(err)
	}
	digest := taskbundle.DigestOf(archive)
	if created, err := e.s.PutTaskBundle(tbOrg, digest, archive); err != nil || !created {
		t.Fatalf("seed bundle: created=%v err=%v", created, err)
	}
	return digest
}

// runPipeline runs a source-file → code(task_bundle) pipeline and returns
// the run it produced and any execution error. A node failing under test is
// NOT a harness failure: RunPipeline reports it as an error, which the
// failure-path tests assert on.
func (e *tbTestEngine) runPipeline(t *testing.T, id, digest string, nodeOverrides map[string]interface{}) (*models.Run, error) {
	t.Helper()
	dsDir := t.TempDir()
	in := writeCSV(t, dsDir, "in.csv", "v\n2\n")

	codeConfig := map[string]interface{}{
		"task_bundle": map[string]interface{}{"digest": digest, "format": taskbundle.Format},
	}
	for k, v := range nodeOverrides {
		codeConfig[k] = v
	}
	pipeline := &models.Pipeline{
		ID: id, Name: id, Enabled: true, OrgID: tbOrg,
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceFile, Name: "Src", Config: map[string]interface{}{"path": in, "format": "csv"}},
			{ID: "code", Type: models.NodeTypeCode, Name: "Code", Config: codeConfig},
		},
		Edges: []models.Edge{{From: "src", To: "code"}},
	}
	if err := e.s.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}
	return e.eng.RunPipeline(pipeline.ID)
}

// firstCodeRow recovers the code node's expected output row from the run's
// node runs, reading the artifact the engine persisted.
func (e *tbTestEngine) firstCodeRow(t *testing.T, run *models.Run) map[string]interface{} {
	t.Helper()
	ds, err := e.eng.ArtifactStore.ReadArtifact(run.ID, "code", "")
	if err != nil {
		t.Fatalf("read code artifact: %v", err)
	}
	if len(ds.Rows) == 0 {
		t.Fatalf("code node produced no rows (cols %v)", ds.Columns)
	}
	return map[string]interface{}(ds.Rows[0])
}

// toF64 tolerates the numeric widths produced by the streamed (int64) and
// batch (float64) code paths.
func toF64(v interface{}) float64 {
	switch n := v.(type) {
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case float64:
		return n
	default:
		return -1
	}
}

func wantV(t *testing.T, row map[string]interface{}, want float64) {
	t.Helper()
	if got := toF64(row["v"]); got != want {
		t.Fatalf("code output v = %v (type %T), want %v", row["v"], row["v"], want)
	}
}

func TestTaskBundleRunsProjectCodeWithRelativeImport(t *testing.T) {
	e := newTBEngine(t)
	digest := e.bundle(t, 2)
	run, err := e.runPipeline(t, "p-tb-basic", digest, nil)
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	nodeRuns, _ := e.s.ListNodeRunsByRun(run.ID)
	wantV(t, e.firstCodeRow(t, run), 4)
	for _, nr := range nodeRuns {
		if nr.NodeID == "code" && nr.RowCount != 1 {
			t.Errorf("code node row_count = %d, want 1", nr.RowCount)
		}
	}
}

func TestTaskBundleIsolatesHelperModulesAcrossBundlesByDigest(t *testing.T) {
	// Both bundles define the SAME module name with DIFFERENT behavior.
	// A warm worker's sys.modules is process-global, so if the pool did
	// not fence by digest (one sub-key per bundle), the second run would
	// reuse the first's cached helpers and return ×2 instead of ×3.
	e := newTBEngine(t)
	two := e.bundle(t, 2)
	three := e.bundle(t, 3)

	runA, err := e.runPipeline(t, "p-tb-iso-a", two, nil)
	if err != nil {
		t.Fatalf("run A returned error: %v", err)
	}
	wantV(t, e.firstCodeRow(t, runA), 4)

	runB, err := e.runPipeline(t, "p-tb-iso-b", three, nil)
	if err != nil {
		t.Fatalf("run B returned error: %v", err)
	}
	wantV(t, e.firstCodeRow(t, runB), 6)
}

func TestTaskBundleRunsTwiceShareTheDigestSubPool(t *testing.T) {
	// The same digest executed twice should reuse the SAME warm worker —
	// the amortization task bundles were meant to keep. Correctness is
	// asserted by identical output; the sub-key sharing is the mechanism.
	e := newTBEngine(t)
	digest := e.bundle(t, 5)
	for _, id := range []string{"p-tb-warm-a", "p-tb-warm-b"} {
		run, err := e.runPipeline(t, id, digest, nil)
		if err != nil {
			t.Fatalf("%s returned error: %v", id, err)
		}
		wantV(t, e.firstCodeRow(t, run), 10)
	}
}

func TestTaskBundleEntryOutsideFileListFailsTheRun(t *testing.T) {
	e := newTBEngine(t)
	// A manifest whose entry is NOT in its own authoritative file list is
	// refused at the mount point (extraction), before any code runs.
	m := &taskbundle.Manifest{
		Format: taskbundle.Format, Language: "python", Entry: "tasks.py",
		Files: []string{"other.py"}, // lies: tasks.py is present but excluded
	}
	archive, err := taskbundle.Assemble(map[string]string{"tasks.py": "output_data = {}"}, m)
	if err != nil {
		t.Fatal(err)
	}
	digest := taskbundle.DigestOf(archive)
	if _, err := e.s.PutTaskBundle(tbOrg, digest, archive); err != nil {
		t.Fatal(err)
	}
	_, execErr := e.runPipeline(t, "p-tb-lies", digest, nil)
	if execErr == nil {
		t.Fatal("a bundle whose entry is excluded from its file list ran to success")
	}
	if !strings.Contains(execErr.Error(), "task bundle") {
		t.Fatalf("failure does not name the task bundle: %s", execErr)
	}
}

func TestTaskBundleMissingFromStoreFailsTheRun(t *testing.T) {
	e := newTBEngine(t)
	_, execErr := e.runPipeline(t, "p-tb-missing", "sha256:"+strings.Repeat("f", 64), nil)
	if execErr == nil {
		t.Fatal("a pipeline referencing an unstored bundle ran to success")
	}
	if !strings.Contains(execErr.Error(), "not stored for this org") {
		t.Fatalf("missing-bundle failure does not explain itself: %s", execErr)
	}
}
