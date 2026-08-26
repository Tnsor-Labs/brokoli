package engine

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/dbtmanifest"
	"github.com/Tnsor-Labs/brokoli/store"
)

// #353 Phase 2: each way dbt can fail produces a distinct, named status.
//
// The modes below were each produced against real dbt 1.8.9 before being
// encoded, and the tests that need dbt reproduce them rather than trusting
// the table:
//
//	mode              exit    run_results.json
//	compile error     2       absent
//	failing model     1       present, node status "error"
//	failing test      1       present, node status "fail"
//	killed process    signal  absent
//	dbt not installed exec    absent
//
// Neither signal separates all five on its own: exit 1 covers both a failing
// model and a failing test, and an absent results file covers both a compile
// error and a kill. That is why the classifier takes both plus the context.

// The unit table: each combination of signals maps to exactly one kind.
func TestClassifyDBTFailure(t *testing.T) {
	modelErr := &dbtmanifest.RunResults{Results: []dbtmanifest.NodeResult{
		{UniqueID: "model.p.ok", Status: dbtmanifest.StatusSuccess},
		{UniqueID: "model.p.broken", Status: dbtmanifest.StatusError, Message: "Database Error in model broken\n  relation does not exist"},
	}}
	testFail := &dbtmanifest.RunResults{Results: []dbtmanifest.NodeResult{
		{UniqueID: "model.p.ok", Status: dbtmanifest.StatusSuccess},
		{UniqueID: "test.p.not_null_ok_id.abc", Status: dbtmanifest.StatusFail, Message: "Got 3 results, configured to fail if != 0"},
	}}
	both := &dbtmanifest.RunResults{Results: []dbtmanifest.NodeResult{
		{UniqueID: "model.p.broken", Status: dbtmanifest.StatusError, Message: "Database Error"},
		{UniqueID: "test.p.some_test.abc", Status: dbtmanifest.StatusFail, Message: "failed"},
	}}

	cases := []struct {
		name     string
		execErr  error
		ctxErr   error
		results  *dbtmanifest.RunResults
		wantKind DBTFailureKind
		wantNode string
	}{
		{
			name: "a failing model", execErr: errors.New("exit status 1"),
			results: modelErr, wantKind: DBTFailureModel, wantNode: "broken",
		},
		{
			name: "a failing test", execErr: errors.New("exit status 1"),
			results: testFail, wantKind: DBTFailureTest, wantNode: "not_null_ok_id",
		},
		{
			// The build being broken is the more urgent fact, and a test
			// failing may simply be downstream of it.
			name: "a model error outranks a test failure", execErr: errors.New("exit status 1"),
			results: both, wantKind: DBTFailureModel, wantNode: "broken",
		},
		{
			name: "a compile error", execErr: errors.New("exit status 2"),
			results: nil, wantKind: DBTFailureCompile,
		},
		{
			// Cancellation and a compile error look identical from the
			// artifacts: both leave no results. Only the context separates
			// them, which is why it is consulted first.
			name: "a cancelled run", execErr: errors.New("signal: killed"),
			ctxErr: context.Canceled, results: nil, wantKind: DBTFailureCancelled,
		},
		{
			name: "cancelled even when results exist", execErr: errors.New("signal: killed"),
			ctxErr: context.DeadlineExceeded, results: modelErr, wantKind: DBTFailureCancelled,
		},
		{
			name:    "dbt not installed",
			execErr: &exec.Error{Name: "dbt", Err: exec.ErrNotFound},
			results: nil, wantKind: DBTFailureNotInstalled,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := classifyDBTFailure("build", tc.execErr, tc.ctxErr, tc.results)
			if f.Kind != tc.wantKind {
				t.Fatalf("kind = %q, want %q", f.Kind, tc.wantKind)
			}
			if tc.wantNode != "" {
				if len(f.Nodes) == 0 {
					t.Fatalf("no node named; the error should say which one")
				}
				if f.Nodes[0] != tc.wantNode {
					t.Errorf("named %q, want %q", f.Nodes[0], tc.wantNode)
				}
			}
			// Every failure must read as something a person can act on.
			msg := f.Error()
			if !strings.Contains(msg, "dbt build") {
				t.Errorf("message does not name the command: %s", msg)
			}
			if strings.Contains(msg, "unknown") && tc.wantKind != DBTFailureUnknown {
				t.Errorf("message fell back to unknown: %s", msg)
			}
		})
	}
}

// The distinction that matters most, stated as its own test: a failing test
// means the models built and the data is wrong, while a compile error means
// nothing ran. Collapsing them loses what the person on call needs.
func TestTestFailureAndCompileErrorReadDifferently(t *testing.T) {
	testFail := classifyDBTFailure("build", errors.New("exit status 1"),
		nil, &dbtmanifest.RunResults{Results: []dbtmanifest.NodeResult{
			{UniqueID: "test.p.t.abc", Status: dbtmanifest.StatusFail, Message: "Got 3 results"},
		}})
	compile := classifyDBTFailure("build", errors.New("exit status 2"), nil, nil)

	if testFail.Kind == compile.Kind {
		t.Fatal("a failing test and a compile error must not classify the same")
	}
	if !strings.Contains(testFail.Error(), "built") {
		t.Errorf("a test failure should say the models built: %s", testFail.Error())
	}
	if !strings.Contains(compile.Error(), "no model ran") {
		t.Errorf("a compile error should say nothing ran: %s", compile.Error())
	}
}

// The classifier must not invent a kind it cannot support. An exit with
// results that contain no failure at all is genuinely unexplained.
func TestUnexplainedFailureSaysSo(t *testing.T) {
	f := classifyDBTFailure("run", errors.New("exit status 1"), nil,
		&dbtmanifest.RunResults{Results: []dbtmanifest.NodeResult{
			{UniqueID: "model.p.a", Status: dbtmanifest.StatusSuccess},
		}})
	if f.Kind != DBTFailureUnknown {
		t.Errorf("kind = %q; with no failing node the honest answer is unknown", f.Kind)
	}
	if !strings.Contains(f.Error(), "do not identify") {
		t.Errorf("the message should admit it: %s", f.Error())
	}
}

// The failure wraps the underlying error, so a caller can still reach it.
func TestFailureWrapsTheExecError(t *testing.T) {
	inner := errors.New("exit status 1")
	f := classifyDBTFailure("run", inner, nil, nil)
	if !errors.Is(f, inner) {
		t.Error("the classified failure should unwrap to the exec error")
	}
}

// Against real dbt: a compile error must classify as one, and must not be
// mistaken for a model failure. This is the mode with no run_results.json,
// which is the signal the classifier leans on.
func TestRealCompileErrorIsClassified(t *testing.T) {
	dbtBinary(t)
	uri := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if uri == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	host, port, user, pass, dbname := parsePGForProfile(t, uri)

	dir := t.TempDir()
	projectDir, profilesDir := writeSkipChainProject(t, dir, host, port, user, pass, dbname, "dbt_compile_test")
	// An unresolvable ref fails the whole project at parse time, which is
	// the point: nothing runs, so no results are written.
	if err := os.WriteFile(filepath.Join(projectDir, "models", "unresolvable.sql"),
		[]byte("select * from {{ ref('no_model_by_this_name') }}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := store.NewSQLiteStore(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	eng := drainEngineOnCleanup(t, NewEngine(st))

	p := &models.Pipeline{
		ID: "dbt-compile", Name: "compile", Enabled: true,
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
	run, runErr := eng.RunPipeline(p.ID)

	combined := ""
	if runErr != nil {
		combined = runErr.Error()
	}
	if run != nil {
		combined += " " + run.Error
	}
	if !strings.Contains(combined, string(DBTFailureCompile)) &&
		!strings.Contains(combined, "did not compile") {
		t.Errorf("a project that cannot compile should say so, got: %s", combined)
	}
	// And it must not claim a model failed, because none ran.
	if strings.Contains(combined, string(DBTFailureModel)) {
		t.Errorf("a compile error was reported as a model failure: %s", combined)
	}
}

// Against real dbt: a failing model classifies as a model error and names
// the model, which an exit code cannot do.
func TestRealModelFailureIsClassified(t *testing.T) {
	dbtBinary(t)
	uri := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if uri == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	host, port, user, pass, dbname := parsePGForProfile(t, uri)

	dir := t.TempDir()
	projectDir, profilesDir := writeSkipChainProject(t, dir, host, port, user, pass, dbname, "dbt_modelfail_test")

	st, err := store.NewSQLiteStore(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	eng := drainEngineOnCleanup(t, NewEngine(st))

	p := &models.Pipeline{
		ID: "dbt-modelfail", Name: "modelfail", Enabled: true,
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
	run, runErr := eng.RunPipeline(p.ID)

	combined := ""
	if runErr != nil {
		combined = runErr.Error()
	}
	if run != nil {
		combined += " " + run.Error
	}
	if !strings.Contains(combined, "a model failed") {
		t.Errorf("expected a model failure, got: %s", combined)
	}
	if !strings.Contains(combined, "broken") {
		t.Errorf("the failure should name the model: %s", combined)
	}
	if strings.Contains(combined, "did not compile") {
		t.Errorf("a model failure was reported as a compile error: %s", combined)
	}
}

// A dbt id names a resource differently depending on its kind, and the
// segment that matters is the same one in both. Reporting a failing test by
// its trailing hash would be technically accurate and useless.
func TestShortDBTNodeNamesTheResource(t *testing.T) {
	for id, want := range map[string]string{
		"model.my_project.city_totals":                  "city_totals",
		"seed.my_project.raw_orders":                    "raw_orders",
		"test.my_project.not_null_orders_id.8cf5724805": "not_null_orders_id",
		"snapshot.my_project.orders_snapshot":           "orders_snapshot",
		// Nothing recognisable: give back what we were given rather than
		// a confidently wrong fragment of it.
		"weird":    "weird",
		"a.b":      "a.b",
		"model.p.": "model.p.",
	} {
		if got := shortDBTNode(id); got != want {
			t.Errorf("shortDBTNode(%q) = %q, want %q", id, got, want)
		}
	}
}
