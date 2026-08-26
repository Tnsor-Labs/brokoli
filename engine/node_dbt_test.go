package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// These tests run REAL dbt-core against a REAL Postgres. That is the whole
// point of them: the defect they exist to prevent (#348) was that every dbt
// command this node issued failed at dbt's option parser, and it survived to
// production because the only test touching dbt substituted a fake executor
// to avoid needing the tool. A mock would have passed against the broken
// code.
//
// Skips when dbt is not installed, so a contributor without a Python
// environment is not blocked; CI installs it.

func dbtBinary(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("dbt")
	if err != nil {
		t.Skip("dbt not installed; skipping the dbt node's live tests")
	}
	return path
}

// dbtFixtureProject copies the fixture into a temp dir (dbt writes target/
// and logs/ into the project) and writes a profiles.yml pointing at the test
// Postgres.
func dbtFixtureProject(t *testing.T) (projectDir, profilesDir string) {
	t.Helper()
	dbtBinary(t)
	uri := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if uri == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	host, port, user, pass, dbname := parsePGForProfile(t, uri)

	projectDir = filepath.Join(t.TempDir(), "project")
	if err := copyDirForTest("testdata/dbt_project", projectDir); err != nil {
		t.Fatal(err)
	}
	profilesDir = t.TempDir()
	// A schema per test, so two tests cannot collide on one relation.
	schema := "dbt_fixture_" + strings.ToLower(strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()))
	if len(schema) > 60 {
		schema = schema[:60]
	}
	profile := fmt.Sprintf(`brokoli_fixture:
  target: test
  outputs:
    test:
      type: postgres
      host: %s
      port: %s
      user: %s
      password: %q
      dbname: %s
      schema: %s
      threads: 1
`, host, port, user, pass, dbname, schema)
	if err := os.WriteFile(filepath.Join(profilesDir, "profiles.yml"), []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}
	return projectDir, profilesDir
}

func parsePGForProfile(t *testing.T, uri string) (host, port, user, pass, dbname string) {
	t.Helper()
	u := strings.TrimPrefix(strings.TrimPrefix(uri, "postgresql://"), "postgres://")
	if i := strings.IndexByte(u, '?'); i >= 0 {
		u = u[:i]
	}
	at := strings.LastIndexByte(u, '@')
	if at < 0 {
		t.Fatalf("cannot parse %q", uri)
	}
	creds, hostpart := u[:at], u[at+1:]
	user, pass = creds, ""
	if i := strings.IndexByte(creds, ':'); i >= 0 {
		user, pass = creds[:i], creds[i+1:]
	}
	slash := strings.IndexByte(hostpart, '/')
	if slash < 0 {
		t.Fatalf("cannot parse database from %q", uri)
	}
	hostport, dbname := hostpart[:slash], hostpart[slash+1:]
	host, port = hostport, "5432"
	if i := strings.IndexByte(hostport, ':'); i >= 0 {
		host, port = hostport[:i], hostport[i+1:]
	}
	return host, port, user, pass, dbname
}

func copyDirForTest(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

func dbtRunner(t *testing.T) *Runner {
	t.Helper()
	dir := t.TempDir()
	st, err := store.NewSQLiteStore(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &Runner{ctx: context.Background(), store: st, run: &models.Run{ID: "dbt-test-run"}}
}

func dbtNode(cfg map[string]interface{}) models.Node {
	return models.Node{ID: "dbt", Type: models.NodeTypeDBT, Name: "dbt", Config: cfg}
}

// The defect itself: every command the UI offers must actually execute.
// Against the broken code each of these failed with
// "Error: No such option '--output'" before dbt read the project.
// dbtTestsRunInParallel: every dbt invocation costs about ten seconds of
// Python interpreter startup before it does any work, and these tests spend
// nearly all their time waiting on that subprocess rather than using CPU.
// They are independent by construction -- each gets its own copied project
// directory and its own Postgres schema -- so running them concurrently
// turns a serial sum into roughly the longest single test.
//
// Measured on the engine package: the dbt tests were 84s of a 180s package,
// the single largest cost in it (Tnsor-Labs/brokoli#329).
//
// TestDBTDoesNotClobberProfilesDirFromTheEnvironment is deliberately NOT
// parallel: it uses t.Setenv, which Go refuses to combine with t.Parallel
// because the environment is process-wide.
func TestDBTCommandsActuallyRun(t *testing.T) {
	t.Parallel()
	projectDir, profilesDir := dbtFixtureProject(t)
	r := dbtRunner(t)

	// seed first: the models ref it.
	if _, err := r.runDBT(dbtNode(map[string]interface{}{
		"command": "seed", "project_dir": projectDir, "profiles_dir": profilesDir,
	})); err != nil {
		t.Fatalf("dbt seed: %v", err)
	}

	res, err := r.runDBT(dbtNode(map[string]interface{}{
		"command": "run", "project_dir": projectDir, "profiles_dir": profilesDir,
		"select": "stg_orders city_totals",
	}))
	if err != nil {
		t.Fatalf("dbt run: %v", err)
	}
	// The per-model results, not a blob of stdout.
	if got := fmt.Sprint(res.output.Columns); !strings.Contains(got, "model") || !strings.Contains(got, "status") {
		t.Fatalf("expected per-model results, got columns %v", res.output.Columns)
	}
	if len(res.output.Rows) != 2 {
		t.Fatalf("expected 2 model results, got %d: %v", len(res.output.Rows), res.output.Rows)
	}
	for _, row := range res.output.Rows {
		if row["status"] != "success" {
			t.Errorf("model %v: status %v, want success", row["model"], row["status"])
		}
	}

	if _, err := r.runDBT(dbtNode(map[string]interface{}{
		"command": "test", "project_dir": projectDir, "profiles_dir": profilesDir,
		"select": "stg_orders city_totals",
	})); err != nil {
		t.Fatalf("dbt test: %v", err)
	}

	if _, err := r.runDBT(dbtNode(map[string]interface{}{
		"command": "build", "project_dir": projectDir, "profiles_dir": profilesDir,
		"select": "stg_orders city_totals",
	})); err != nil {
		t.Fatalf("dbt build: %v", err)
	}
}

// ls is the one command that does take --output json, and it must keep
// getting it -- the fix is conditional, not a blanket removal.
func TestDBTListStillUsesOutputJSON(t *testing.T) {
	t.Parallel()
	projectDir, profilesDir := dbtFixtureProject(t)
	r := dbtRunner(t)
	res, err := r.runDBT(dbtNode(map[string]interface{}{
		"command": "ls", "project_dir": projectDir, "profiles_dir": profilesDir,
	}))
	if err != nil {
		t.Fatalf("dbt ls: %v", err)
	}
	out := fmt.Sprint(res.output.Rows)
	if !strings.Contains(out, "stg_orders") {
		t.Errorf("ls output should name the fixture's models: %s", out)
	}
}

// A failing model fails the node, and the error names which model failed --
// read from run_results.json, which dbt writes even on failure. An exit code
// alone cannot say that.
func TestDBTFailingModelNamesTheModel(t *testing.T) {
	t.Parallel()
	projectDir, profilesDir := dbtFixtureProject(t)
	r := dbtRunner(t)

	_, err := r.runDBT(dbtNode(map[string]interface{}{
		"command": "run", "project_dir": projectDir, "profiles_dir": profilesDir,
		"select": "always_fails",
	}))
	if err == nil {
		t.Fatal("a failing model must fail the node")
	}
	if !strings.Contains(err.Error(), "always_fails") {
		t.Errorf("the error should name the failing model, got: %v", err)
	}
}

// run_results.json is read from the PROJECT's target directory. Reading it
// relative to the engine's working directory -- which is what this did --
// resolved to the wrong path for every project that is not the process cwd.
func TestDBTReadsResultsFromTheProjectDirectory(t *testing.T) {
	t.Parallel()
	projectDir, profilesDir := dbtFixtureProject(t)
	r := dbtRunner(t)

	if _, err := r.runDBT(dbtNode(map[string]interface{}{
		"command": "seed", "project_dir": projectDir, "profiles_dir": profilesDir,
	})); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "target", "run_results.json")); err != nil {
		t.Fatalf("dbt should have written run_results.json into the project: %v", err)
	}
	// And nothing was written into the engine's own working directory.
	if _, err := os.Stat(filepath.Join("target", "run_results.json")); err == nil {
		t.Error("run_results.json resolved against the engine cwd, not the project")
	}
}

// A cancelled run stops dbt rather than outliving it. Without the attempt's
// context the node ignored both cancellation and the node timeout.
func TestDBTCancellationStopsTheProcess(t *testing.T) {
	projectDir, profilesDir := dbtFixtureProject(t)
	r := dbtRunner(t)
	ctx, cancel := context.WithCancel(context.Background())
	r.ctx = ctx
	cancel()

	_, err := r.runDBT(dbtNode(map[string]interface{}{
		"command": "run", "project_dir": projectDir, "profiles_dir": profilesDir,
	}))
	if err == nil {
		t.Fatal("a cancelled run must fail the node")
	}
	if !strings.Contains(err.Error(), "cancelled") {
		t.Errorf("the error should say it was cancelled, got: %v", err)
	}
}

// An unset profiles_dir must leave DBT_PROFILES_DIR alone.
//
// Exporting it empty -- which this node did -- overrode whatever the
// operator had set, so a deployment that configures DBT_PROFILES_DIR in the
// worker's environment (the ordinary way to do it) had it silently replaced
// with nothing and could not resolve its profile.
//
// Deliberately tests the inherited environment rather than dbt's ~/.dbt
// fallback: overriding HOME mid-process is not a property this code
// controls, and a test that depends on it is testing the harness.
func TestDBTDoesNotClobberProfilesDirFromTheEnvironment(t *testing.T) {
	projectDir, profilesDir := dbtFixtureProject(t)

	// The operator's setting, in the environment the worker inherits.
	t.Setenv("DBT_PROFILES_DIR", profilesDir)

	r := dbtRunner(t)
	if _, err := r.runDBT(dbtNode(map[string]interface{}{
		// No profiles_dir configured on the node.
		"command": "seed", "project_dir": projectDir,
	})); err != nil {
		t.Fatalf("with no profiles_dir on the node, the environment's DBT_PROFILES_DIR must survive: %v", err)
	}
}

// The JSON log stream is rendered as its human message; anything that does
// not parse passes through, so a stack trace still reaches the author.
func TestDBTLogLineRendering(t *testing.T) {
	for in, want := range map[string]string{
		`{"info":{"msg":"1 of 2 OK created table model x","level":"info"}}`: "1 of 2 OK created table model x",
		`plain text line`:      "plain text line",
		`{"not":"a log line"}`: `{"not":"a log line"}`,
		`{broken`:              `{broken`,
	} {
		if got := dbtLogLine(in); got != want {
			t.Errorf("dbtLogLine(%q) = %q, want %q", in, got, want)
		}
	}
}
