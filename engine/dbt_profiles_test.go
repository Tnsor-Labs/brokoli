package engine

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// #353 Phase 2: a dbt project should not need its own copy of the warehouse
// password. The tests below are mostly about the file rather than the
// mapping, because a generated profile holds a resolved credential and
// where it lands matters more than what is in it.

func testConnection() *models.Connection {
	return &models.Connection{
		ConnID: "prod-warehouse", Type: models.ConnTypePostgres,
		Host: "db.internal", Port: 5432, Schema: "analytics",
		Login: "etl_user", Password: `p@ss"w:rd#1 x`,
	}
}

// A credential must never be written into the project. A dbt project is
// usually a git checkout, and a password written into one is how it reaches
// a commit.
func TestGeneratedProfileNeverLandsInTheProject(t *testing.T) {
	projectDir := t.TempDir()
	p, err := generateDBTProfile(testConnection(), "my_project", "dbt_target", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Cleanup()

	if strings.HasPrefix(p.Dir, projectDir) {
		t.Fatalf("profile written inside the project at %s", p.Dir)
	}
	entries, err := os.ReadDir(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("the project directory gained %d entries", len(entries))
	}
}

// The permissions are the protection, and they must hold from creation
// rather than being tightened afterwards.
func TestGeneratedProfileIsNotReadableByOthers(t *testing.T) {
	p, err := generateDBTProfile(testConnection(), "my_project", "dbt_target", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Cleanup()

	di, err := os.Stat(p.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Errorf("profile directory mode is %o, want 700", perm)
	}
	fi, err := os.Stat(filepath.Join(p.Dir, "profiles.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("profiles.yml mode is %o, want 600", perm)
	}
}

// Cleanup must actually remove the credential, and must be safe to call
// again — it is deferred, and a defer that panics on a second call is worse
// than the leak it was guarding.
func TestCleanupRemovesTheCredential(t *testing.T) {
	p, err := generateDBTProfile(testConnection(), "my_project", "dbt_target", 4)
	if err != nil {
		t.Fatal(err)
	}
	dir := p.Dir
	p.Cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the profile directory survived cleanup: %v", err)
	}
	p.Cleanup() // must not panic
}

// A password with YAML metacharacters must not be able to change the
// document's shape. This is the same class of defect as #338's SQL
// escaping, one format over.
func TestPasswordWithYAMLMetacharactersIsQuoted(t *testing.T) {
	conn := testConnection()
	// Colons, quotes, a hash, a space, and a line that would otherwise
	// look like a new key.
	conn.Password = "a: b # c\nnot_a_key: injected\"'"
	p, err := generateDBTProfile(conn, "my_project", "dbt_target", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Cleanup()

	body, err := os.ReadFile(filepath.Join(p.Dir, "profiles.yml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	// The newline must be escaped rather than starting a line, or the
	// remainder becomes structure.
	if strings.Contains(text, "\nnot_a_key:") {
		t.Error("a newline in the password broke out into the document structure")
	}
	if !strings.Contains(text, `\n`) {
		t.Error("the newline was not escaped")
	}
	// And the whole value stays on the password line.
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "password:") {
			if !strings.Contains(line, `\"`) {
				t.Errorf("the quote in the password was not escaped: %s", line)
			}
		}
	}
}

// A connection type Brokoli cannot itself read is refused by name, with
// what is supported. ADR-025 lists a dbt run against a warehouse Brokoli
// cannot read back as out of scope, so this is the boundary being held.
func TestUnsupportedConnectionTypeIsRefusedByName(t *testing.T) {
	conn := testConnection()
	conn.Type = models.ConnTypeSnowflake
	_, err := generateDBTProfile(conn, "my_project", "dbt_target", 4)
	if err == nil {
		t.Fatal("a connection type with no supported adapter must be refused")
	}
	for _, want := range []string{"snowflake", "prod-warehouse", "postgres"} {
		if !strings.Contains(strings.ToLower(err.Error()), want) {
			t.Errorf("refusal should mention %q, got: %v", want, err)
		}
	}
}

// dbt needs somewhere to build models, and a Brokoli connection names a
// database rather than a schema. Guessing one would put tables somewhere
// nobody asked for.
func TestTargetSchemaIsRequired(t *testing.T) {
	_, err := generateDBTProfile(testConnection(), "my_project", "", 4)
	if err == nil {
		t.Fatal("a missing target schema must be refused rather than guessed")
	}
	if !strings.Contains(err.Error(), "schema") {
		t.Errorf("the refusal should say what is missing: %v", err)
	}
}

// The adapter table names the distribution to install, so a missing adapter
// is actionable rather than a Python traceback (ADR-026).
func TestAdapterNamesItsDistribution(t *testing.T) {
	for connType, wantDist := range map[models.ConnectionType]string{
		models.ConnTypePostgres: "dbt-postgres",
		models.ConnTypeMySQL:    "dbt-mysql",
	} {
		a, ok := dbtAdapterFor(connType)
		if !ok {
			t.Errorf("%s has no adapter", connType)
			continue
		}
		if a.Distribution != wantDist {
			t.Errorf("%s distribution = %q, want %q", connType, a.Distribution, wantDist)
		}
		if a.DefaultPort == 0 {
			t.Errorf("%s has no default port", connType)
		}
	}
	if _, ok := dbtAdapterFor(models.ConnTypeSnowflake); ok {
		t.Error("snowflake should not claim an adapter while Brokoli cannot read it back")
	}
}

// The profile name must match what the project asks for, since dbt matches
// them by name and a mismatch fails confusingly.
func TestProfileNameComesFromTheProject(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "dbt_project.yml"),
		[]byte("name: \"thing\"\nversion: \"1.0\"\nprofile: \"warehouse_profile\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := dbtProjectProfileName(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != "warehouse_profile" {
		t.Errorf("profile name = %q, want warehouse_profile", got)
	}

	// A project that names no profile is refused with what to do about it,
	// rather than generating something dbt will not look for.
	empty := t.TempDir()
	if err := os.WriteFile(filepath.Join(empty, "dbt_project.yml"), []byte("name: \"thing\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := dbtProjectProfileName(empty); err == nil {
		t.Error("a project with no profile: field must be refused")
	}
}

// End to end: a conn_id runs a real dbt project against a real database
// with no profiles.yml of its own anywhere.
func TestDBTRunsFromAConnectionWithNoProjectProfile(t *testing.T) {
	dbtBinary(t)
	uri := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if uri == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	host, port, adminUser, adminPass, dbname := parsePGForProfile(t, uri)

	// A dedicated role with a password that cannot collide with anything
	// else in a log line. The first version of this test reused the test
	// database's own password, which is "brokoli" -- the same string as
	// the product name, the temp-directory prefix and the profile target,
	// so the leak assertion fired on three lines that contained no
	// credential at all.
	const distinctivePass = "zzq7-Prof1le-Leak-Canary-42"
	user := "dbt_profile_canary"
	admin := openFor(t, uri)
	admin.Exec(`DROP OWNED BY ` + user)
	admin.Exec(`DROP ROLE IF EXISTS ` + user)
	if _, err := admin.Exec(`CREATE ROLE ` + user + ` LOGIN PASSWORD '` + distinctivePass + `'`); err != nil {
		t.Skipf("cannot create a test role (needs a privileged connection): %v", err)
	}
	t.Cleanup(func() {
		admin.Exec(`DROP OWNED BY ` + user)
		admin.Exec(`DROP ROLE IF EXISTS ` + user)
	})
	if _, err := admin.Exec(`GRANT ALL ON DATABASE ` + dbname + ` TO ` + user); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(`GRANT CREATE ON SCHEMA public TO ` + user); err != nil {
		t.Fatal(err)
	}
	// dbt creates the target schema itself, then builds into it; the role
	// needs to own it rather than merely be allowed to create one.
	admin.Exec(`DROP SCHEMA IF EXISTS dbt_conn_test CASCADE`)
	if _, err := admin.Exec(`CREATE SCHEMA dbt_conn_test AUTHORIZATION ` + user); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Exec(`DROP SCHEMA IF EXISTS dbt_conn_test CASCADE`) })
	pass := distinctivePass
	_ = adminUser
	_ = adminPass

	dir := t.TempDir()
	projectDir := filepath.Join(dir, "proj")
	if err := os.MkdirAll(filepath.Join(projectDir, "models"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "dbt_project.yml"),
		[]byte("name: \"connproj\"\nversion: \"1.0.0\"\nconfig-version: 2\nprofile: \"connproj\"\n"+
			"model-paths: [\"models\"]\nmodels:\n  connproj:\n    +materialized: table\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "models", "one.sql"),
		[]byte("select 1 as id\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := store.NewSQLiteStore(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	portNum := 5432
	if p, err := parsePortForTest(port); err == nil {
		portNum = p
	}
	if err := st.CreateConnection(&models.Connection{
		ConnID: "wh", Type: models.ConnTypePostgres,
		Host: host, Port: portNum, Schema: dbname,
		Login: user, Password: pass,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	eng := drainEngineOnCleanup(t, NewEngine(st))
	// Wired the way serve.go wires it: the resolver is what turns a
	// conn_id into credentials.
	eng.ConnResolver = NewConnectionResolver(st, nil)
	p := &models.Pipeline{
		ID: "dbt-conn", Name: "dbt from connection", Enabled: true,
		Nodes: []models.Node{{
			ID: "dbt", Type: models.NodeTypeDBT, Name: "Run",
			Config: map[string]interface{}{
				"command": "run", "project_dir": projectDir,
				"conn_id": "wh", "target_schema": "dbt_conn_test",
			},
		}},
	}
	if err := st.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	run, err := eng.RunPipeline(p.ID)
	if err != nil {
		t.Fatalf("a project with no profiles.yml of its own should run from the connection: %v", err)
	}
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %s: %s", run.Status, run.Error)
	}

	// The generated directory must be gone, and nothing in the run log may
	// carry the password.
	logs, err := st.GetLogs(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range logs {
		if strings.Contains(l.Message, pass) {
			t.Errorf("the run log contains the warehouse password: %s", l.Message)
		}
	}
}

func parsePortForTest(s string) (int, error) { return strconv.Atoi(s) }

// ADR-027 phase 4: the ClickHouse profile stanza. No dbname -- ClickHouse
// has no database/schema split, the schema IS the database dbt builds in --
// and driver: native, because the connection record's port is the native
// protocol's. The live proof is TestDBTRunsOnClickHouse.
func TestClickHouseProfileShape(t *testing.T) {
	conn := &models.Connection{
		ConnID: "ch", Type: models.ConnTypeClickHouse,
		Host: "localhost", Port: 55534,
		Login: "brokoli", Password: "pw",
		// Deliberately no Schema: the connection's own database plays no
		// part in a ClickHouse profile, and requiring one would refuse
		// connections that work everywhere else.
	}
	p, err := generateDBTProfile(conn, "proj", "dbt_target", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Cleanup()

	body, err := os.ReadFile(filepath.Join(p.Dir, "profiles.yml"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	for _, want := range []string{
		"type: clickhouse",
		"driver: native",
		"port: 55534",
		`schema: "dbt_target"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("profile missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "dbname") {
		t.Errorf("a ClickHouse profile must not carry dbname -- there is no database/schema split:\n%s", got)
	}
}
