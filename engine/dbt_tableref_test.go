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

// #353 Phase 3: a model dbt built is a named relation on a known server, so
// a sink on the same connection composes through the pushdown compiler that
// already exists.
//
// The claim being tested is not "it produces the right rows" -- an ordinary
// dataset hand-off would too. It is that the rows never enter the engine:
// the dbt node hands downstream a reference, and the sink turns it into one
// statement on the server. That is only observable from the run log and from
// the sink's contents matching without a row count passing through.

// dbtRefProject writes a project with one model worth referencing.
func dbtRefProject(t *testing.T, dir, host, port, user, pass, dbname, schema string) string {
	t.Helper()
	projectDir := filepath.Join(dir, "proj")
	if err := os.MkdirAll(filepath.Join(projectDir, "models"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(p, body string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(projectDir, "dbt_project.yml"),
		"name: \"refproj\"\nversion: \"1.0.0\"\nconfig-version: 2\nprofile: \"refproj\"\n"+
			"model-paths: [\"models\"]\nmodels:\n  refproj:\n    +materialized: table\n")
	write(filepath.Join(projectDir, "models", "customers.sql"),
		"select 1 as id, 'alice' as name union all select 2, 'bob' union all select 3, 'carol'\n")
	// An ephemeral model has no relation at all, which is a case worth
	// refusing by name rather than papering over.
	write(filepath.Join(projectDir, "models", "inlined.sql"),
		"{{ config(materialized='ephemeral') }}\nselect 1 as id\n")
	return projectDir
}

func dbtRefConnection(t *testing.T, st *store.SQLiteStore, uri, schema string) {
	t.Helper()
	host, port, user, pass, dbname := parsePGForProfile(t, uri)
	portNum, _ := strconv.Atoi(port)
	if portNum == 0 {
		portNum = 5432
	}
	if err := st.CreateConnection(&models.Connection{
		ConnID: "wh", Type: models.ConnTypePostgres,
		Host: host, Port: portNum, Schema: dbname,
		Login: user, Password: pass,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
}

// The payoff: dbt builds a model, a sink on the same connection writes it,
// and the rows stay in the database the whole way.
func TestDBTModelComposesWithASinkOnTheSameConnection(t *testing.T) {
	dbtBinary(t)
	uri := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if uri == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	db := openFor(t, uri)
	const schema = "dbt_ref_test"

	if _, err := db.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`) })
	if _, err := db.Exec(`CREATE TABLE ` + schema + `.sink (id int, name text)`); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	host, port, user, pass, dbname := parsePGForProfile(t, uri)
	projectDir := dbtRefProject(t, dir, host, port, user, pass, dbname, schema)

	st, err := store.NewSQLiteStore(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	dbtRefConnection(t, st, uri, schema)

	eng := drainEngineOnCleanup(t, NewEngine(st))
	eng.ConnResolver = NewConnectionResolver(st, nil)

	p := &models.Pipeline{
		ID: "dbt-ref", Name: "dbt ref", Enabled: true,
		Nodes: []models.Node{
			{ID: "dbt", Type: models.NodeTypeDBT, Name: "Build", Config: map[string]interface{}{
				"command": "run", "project_dir": projectDir,
				"conn_id": "wh", "target_schema": schema,
				"select": "customers", "output_model": "customers",
			}},
			{ID: "sink", Type: models.NodeTypeSinkDB, Name: "Sink", Config: map[string]interface{}{
				"conn_id": "wh", "table": schema + ".sink", "mode": "append",
			}},
		},
		Edges: []models.Edge{{From: "dbt", To: "sink"}},
	}
	if err := st.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	run, err := eng.RunPipeline(p.ID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run status %s: %s", run.Status, run.Error)
	}

	// The rows arrived.
	rows := map[int]string{}
	q, err := db.Query(`SELECT id, name FROM ` + schema + `.sink ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	for q.Next() {
		var id int
		var name string
		if err := q.Scan(&id, &name); err != nil {
			t.Fatal(err)
		}
		rows[id] = name
	}
	if len(rows) != 3 || rows[1] != "alice" || rows[3] != "carol" {
		t.Fatalf("sink contents = %v, want the model's three rows", rows)
	}

	// And the engine never carried them. This is the assertion that
	// distinguishes composition from an ordinary hand-off: without it the
	// test would pass on a path that read every row into memory.
	logs, err := st.GetLogs(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var sawReference, sawEngineMovedNoRows bool
	for _, l := range logs {
		if strings.Contains(l.Message, "as a reference") {
			sawReference = true
		}
		if strings.Contains(l.Message, "the engine moved no rows") {
			sawEngineMovedNoRows = true
		}
	}
	if !sawReference {
		t.Error("the dbt node did not hand its model downstream as a reference")
	}
	if !sawEngineMovedNoRows {
		t.Error("the segment did not run in the database; the rows went through the engine")
	}
}

// An ephemeral model has no relation, and asking for one is a configuration
// mistake worth naming rather than a case to work around.
func TestEphemeralModelIsRefusedByName(t *testing.T) {
	dbtBinary(t)
	uri := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if uri == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	db := openFor(t, uri)
	const schema = "dbt_ephem_test"
	db.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`)
	if _, err := db.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`) })

	dir := t.TempDir()
	host, port, user, pass, dbname := parsePGForProfile(t, uri)
	projectDir := dbtRefProject(t, dir, host, port, user, pass, dbname, schema)

	st, err := store.NewSQLiteStore(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	dbtRefConnection(t, st, uri, schema)

	eng := drainEngineOnCleanup(t, NewEngine(st))
	eng.ConnResolver = NewConnectionResolver(st, nil)

	p := &models.Pipeline{
		ID: "dbt-ephem", Name: "ephemeral", Enabled: true,
		Nodes: []models.Node{{
			ID: "dbt", Type: models.NodeTypeDBT, Name: "Build", Config: map[string]interface{}{
				"command": "run", "project_dir": projectDir,
				"conn_id": "wh", "target_schema": schema,
				"output_model": "inlined",
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
	if !strings.Contains(combined, "ephemeral") {
		t.Errorf("an ephemeral output_model should be refused by name, got: %s", combined)
	}
}

// Composition needs a server, and only a conn_id names one. A project using
// its own profiles.yml still runs; it just cannot hand a reference on.
func TestOutputModelWithoutAConnectionIsRefused(t *testing.T) {
	r := &Runner{}
	_, err := r.dbtOutputTableRef(
		models.Node{ID: "dbt", Config: map[string]interface{}{"output_model": "customers"}},
		"/nonexistent", nil, nil)
	if err == nil {
		t.Fatal("output_model without conn_id must be refused")
	}
	if !strings.Contains(err.Error(), "conn_id") {
		t.Errorf("the refusal should say what is missing: %v", err)
	}
}

// Not asking for a reference is the common case and must stay silent.
func TestNoOutputModelIsNotAnError(t *testing.T) {
	r := &Runner{}
	ref, err := r.dbtOutputTableRef(models.Node{ID: "dbt", Config: map[string]interface{}{}}, "", nil, nil)
	if err != nil {
		t.Fatalf("a node that asks for nothing must not error: %v", err)
	}
	if ref != nil {
		t.Error("a node that asks for nothing must not produce a reference")
	}
}
