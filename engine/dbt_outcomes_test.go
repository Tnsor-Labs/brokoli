package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// #353 Phase 1, end to end: one dbt invocation, an outcome recorded per
// model.
//
// This runs real dbt against real Postgres, because the whole claim is about
// what dbt reports and a fake would define that away. The project contains a
// model that fails and two downstream of it, so the interesting states --
// failed, skipped-because-of-it, and unaffected-and-still-ran -- all occur in
// one invocation.

// writeSkipChainProject builds a project where broken fails, two models sit
// downstream of it, and two are unrelated.
func writeSkipChainProject(t *testing.T, dir string, host, port, user, pass, dbname, schema string) (projectDir, profilesDir string) {
	t.Helper()
	projectDir = filepath.Join(dir, "proj")
	profilesDir = filepath.Join(dir, "profiles")
	for _, d := range []string{filepath.Join(projectDir, "models"), profilesDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, body string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(projectDir, "dbt_project.yml"),
		"name: \"skipchain\"\nversion: \"1.0.0\"\nconfig-version: 2\nprofile: \"skipchain\"\n"+
			"model-paths: [\"models\"]\nmodels:\n  skipchain:\n    +materialized: table\n")
	write(filepath.Join(profilesDir, "profiles.yml"),
		"skipchain:\n  target: test\n  outputs:\n    test:\n      type: postgres\n"+
			"      host: "+host+"\n      port: "+port+"\n      user: "+user+"\n"+
			"      password: \""+pass+"\"\n      dbname: "+dbname+"\n      schema: "+schema+"\n      threads: 4\n")

	m := filepath.Join(projectDir, "models")
	write(filepath.Join(m, "ok_root.sql"), "select 1 as id\n")
	// Compiles, fails at execution: a missing ref would fail the whole
	// project at parse time and there would be nothing to observe.
	write(filepath.Join(m, "broken.sql"), "select * from brokoli_no_such_relation_anywhere\n")
	write(filepath.Join(m, "downstream_of_broken.sql"), "select * from {{ ref('broken') }}\n")
	write(filepath.Join(m, "two_hops.sql"), "select * from {{ ref('downstream_of_broken') }}\n")
	write(filepath.Join(m, "unrelated.sql"), "select * from {{ ref('ok_root') }}\n")
	return projectDir, profilesDir
}

// One invocation must leave a durable record per model, and the records must
// distinguish the model that broke from the ones it blocked and the ones
// that ran anyway.
func TestDBTRecordsAnOutcomePerModel(t *testing.T) {
	dbtBinary(t)
	uri := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if uri == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	host, port, user, pass, dbname := parsePGForProfile(t, uri)

	dir := t.TempDir()
	projectDir, profilesDir := writeSkipChainProject(t, dir, host, port, user, pass, dbname, "dbt_outcomes_test")

	st, err := store.NewSQLiteStore(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	eng := drainEngineOnCleanup(t, NewEngine(st))

	p := &models.Pipeline{
		ID: "dbt-outcomes", Name: "dbt outcomes", Enabled: true,
		Nodes: []models.Node{{
			ID: "dbt", Type: models.NodeTypeDBT, Name: "Build",
			Config: map[string]interface{}{
				"command": "build", "project_dir": projectDir, "profiles_dir": profilesDir,
			},
		}},
	}
	if err := st.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	// The run itself fails, because a model failed. That is correct and is
	// not what this test is about.
	run, _ := eng.RunPipeline(p.ID)
	if run == nil {
		t.Fatal("no run")
	}

	attempts, err := st.ListExecutionAttemptsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	byModel := map[string]models.ExecutionAttempt{}
	for _, a := range attempts {
		if a.InstanceKey == "" {
			continue // the pipeline-level outbox row
		}
		short := a.InstanceKey[strings.LastIndex(a.InstanceKey, ".")+1:]
		byModel[short] = a
	}

	if len(byModel) < 5 {
		t.Fatalf("recorded %d per-model attempts, want one per model in the project: %v",
			len(byModel), keysOf(byModel))
	}

	// The model that actually broke.
	broken, ok := byModel["broken"]
	if !ok {
		t.Fatal("no record for the model that failed")
	}
	if broken.Status != models.AttemptStatusFailed {
		t.Errorf("broken recorded as %q, want failed", broken.Status)
	}
	if broken.Error == "" {
		t.Error("broken has no error; an exit code would have told us that much")
	}

	// The model it blocked, and the one two hops away. Both must name the
	// failure rather than merely reporting that they did not run -- that
	// naming is the thing dbt's own output does not give you.
	for _, name := range []string{"downstream_of_broken", "two_hops"} {
		a, ok := byModel[name]
		if !ok {
			t.Errorf("no record for %s", name)
			continue
		}
		if !strings.Contains(a.Error, "broken") {
			t.Errorf("%s recorded as %q with error %q; it should name the model that blocked it",
				name, a.Status, a.Error)
		}
	}

	// And the models with no relationship to the failure ran and are
	// recorded as having succeeded. This is the property an orchestrator
	// could not previously see: one model failing does not mean the run
	// tells you nothing about the others.
	for _, name := range []string{"ok_root", "unrelated"} {
		a, ok := byModel[name]
		if !ok {
			t.Errorf("no record for %s", name)
			continue
		}
		if a.Status != models.AttemptStatusCompleted {
			t.Errorf("%s recorded as %q with error %q, want completed -- it does not depend on the failure",
				name, a.Status, a.Error)
		}
	}

	// The run log carries the same facts, since that is what a person
	// reads first and it is the fallback when history cannot be written.
	logs, err := st.GetLogs(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var sawSkipReason, sawSummary bool
	for _, l := range logs {
		if strings.Contains(l.Message, "skipped because") && strings.Contains(l.Message, "broken") {
			sawSkipReason = true
		}
		if strings.Contains(l.Message, "succeeded") && strings.Contains(l.Message, "skipped") {
			sawSummary = true
		}
	}
	if !sawSkipReason {
		t.Error("the log never says which model caused the skips")
	}
	if !sawSummary {
		t.Error("the log has no per-invocation summary")
	}
}

// A project whose artifacts cannot be read must still run exactly as it did
// before: per-model history is an improvement on the run log, never a
// precondition for it.
func TestDBTWithoutArtifactsStillReports(t *testing.T) {
	dir := t.TempDir()
	st, err := store.NewSQLiteStore(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	r := &Runner{ctx: nil, store: st, run: &models.Run{ID: "no-artifacts"}}

	// A directory with no target/ at all.
	summary, ok := r.recordDBTModelOutcomesFromProject(
		models.Node{ID: "dbt", Type: models.NodeTypeDBT}, dir)
	if ok {
		t.Fatal("a project with no artifacts must report that history is unavailable")
	}
	if summary.Succeeded != 0 || summary.Failed != 0 {
		t.Errorf("summary should be empty, got %+v", summary)
	}
}

func keysOf(m map[string]models.ExecutionAttempt) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
