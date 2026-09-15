package engine

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// conn_id is unique across the whole store, so before this a pipeline in
// one workspace could name another workspace's connection and run with
// its credentials. A run now resolves a conn_id only within its own
// pipeline's workspace.

func twoWorkspaceStore(t *testing.T) *store.SQLiteStore {
	t.Helper()
	st, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "ws.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	for _, c := range []models.Connection{
		{ID: "a", ConnID: "wh-a", Type: models.ConnTypePostgres, Host: "a.example", Port: 5432, Schema: "db", Login: "u", Password: "pa", WorkspaceID: "ws-a"},
		{ID: "b", ConnID: "wh-b", Type: models.ConnTypePostgres, Host: "b.example", Port: 5432, Schema: "db", Login: "u", Password: "pb", WorkspaceID: "ws-b"},
	} {
		c := c
		c.CreatedAt, c.UpdatedAt = time.Now().UTC(), time.Now().UTC()
		if err := st.CreateConnection(&c); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func TestAConnectionServesOnlyItsOwnWorkspace(t *testing.T) {
	cr := NewConnectionResolver(twoWorkspaceStore(t), nil)

	if _, err := cr.ResolveConnectionIn("wh-b", "ws-a"); err == nil || !strings.Contains(err.Error(), "not found in this pipeline's workspace") {
		t.Fatalf("another workspace's connection: err = %v", err)
	}
	if c, err := cr.ResolveConnectionIn("wh-a", "ws-a"); err != nil || c.Password != "pa" {
		t.Fatalf("own connection: %+v %v", c, err)
	}
	// No pipeline workspace to compare: not refused.
	if _, err := cr.ResolveConnectionIn("wh-b", ""); err != nil {
		t.Fatalf("unscoped: %v", err)
	}

	cfg, warnings := cr.ResolveWithWarningsIn(map[string]interface{}{"conn_id": "wh-b"}, models.NodeTypeSourceDB, "ws-a")
	if _, ok := cfg["uri"]; ok {
		t.Fatalf("another workspace's credentials were injected: %v", cfg["uri"])
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "not found in this pipeline's workspace") {
		t.Fatalf("warnings = %v", warnings)
	}
	cfg, _ = cr.ResolveWithWarningsIn(map[string]interface{}{"conn_id": "wh-b"}, models.NodeTypeSourceDB, "ws-b")
	if uri, _ := cfg["uri"].(string); !strings.Contains(uri, "b.example") {
		t.Fatalf("own workspace: uri = %q", uri)
	}
	if cfg := cr.ResolveIn(map[string]interface{}{"conn_id": "wh-a"}, models.NodeTypeSinkDB, "ws-b"); cfg["uri"] != nil {
		t.Fatalf("ResolveIn injected another workspace's credentials")
	}
}

// A run in workspace A naming B's SFTP connection fails without ever
// connecting; the same pipeline in B's own workspace delivers.
func TestARunCannotUseAnotherWorkspacesConnection(t *testing.T) {
	allowLoopback(t)
	f := newSFTPFixture(t, false, nil) // "partner" lives in the default workspace
	in := f.localFile(t, "in.csv", ordersCSV)
	for _, tc := range []struct {
		workspace string
		want      models.RunStatus
	}{{"ws-other", models.RunStatusFailed}, {models.DefaultWorkspaceID, models.RunStatusSuccess}} {
		p := chain("ws-"+tc.workspace,
			fileNode("src", models.NodeTypeSourceFile, map[string]interface{}{"path": in}),
			fileNode("out", models.NodeTypeSinkFile, map[string]interface{}{"path": "orders-" + tc.workspace + ".csv", "conn_id": "partner"}),
		)
		p.WorkspaceID = tc.workspace
		before := f.srv.Accepted()
		run, logs := f.run(t, p)
		if run.Status != tc.want {
			t.Fatalf("%s: status %s, want %s\n%s", tc.workspace, run.Status, tc.want, logs)
		}
		if tc.want == models.RunStatusFailed {
			if !strings.Contains(logs, "not found in this pipeline's workspace") {
				t.Fatalf("%s: the refusal is not named:\n%s", tc.workspace, logs)
			}
			if f.srv.Accepted() != before {
				t.Fatalf("%s: a pipeline reached another workspace's server", tc.workspace)
			}
		}
	}
}

// Every run-time resolution goes through the workspace-scoped methods. The
// unscoped ones stay for callers with no pipeline; a node handler calling
// one would quietly reopen the gap, and no single end-to-end test covers
// every node type that resolves a connection (dbt, migrate, file, db).
func TestEngineResolvesConnectionsOnlyWithinAWorkspace(t *testing.T) {
	unscoped := regexp.MustCompile(`connResolver\.(ResolveConnection|Resolve|ResolveWithWarnings)\(`)
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	scanned := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		scanned++
		for i, line := range strings.Split(string(src), "\n") {
			if unscoped.MatchString(line) {
				t.Errorf("%s:%d resolves a connection without the pipeline's workspace: %s", f, i+1, strings.TrimSpace(line))
			}
		}
	}
	if scanned < 20 {
		t.Fatalf("scanned %d files; the guard is not looking at the engine", scanned)
	}
}

// The runner's general resolution (every DB and API node) is scoped too:
// a source_db in workspace A naming B's connection gets no credentials,
// and the run log says why.
func TestADatabaseNodeCannotUseAnotherWorkspacesConnection(t *testing.T) {
	st := twoWorkspaceStore(t)
	eng := drainEngineOnCleanup(t, NewEngine(st))
	eng.ConnResolver = NewConnectionResolver(st, nil)
	p := &models.Pipeline{ID: "ws-db", Name: "ws-db", Enabled: true, WorkspaceID: "ws-a",
		Nodes: []models.Node{{ID: "q", Name: "q", Type: models.NodeTypeSourceDB,
			Config: map[string]interface{}{"conn_id": "wh-b", "query": "select 1"}}}}
	if err := st.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	run, _ := eng.RunPipeline(p.ID)
	if run == nil {
		t.Fatal("no run")
	}
	logs, _ := st.GetLogs(run.ID)
	var all strings.Builder
	for _, l := range logs {
		all.WriteString(l.Message + "\n")
	}
	if run.Status != models.RunStatusFailed || !strings.Contains(all.String(), "not found in this pipeline's workspace") {
		t.Fatalf("status %s; the run must fail and name the refusal:\n%s", run.Status, all.String())
	}
	if strings.Contains(all.String(), "b.example") {
		t.Fatalf("the run reached for another workspace's host:\n%s", all.String())
	}
}
