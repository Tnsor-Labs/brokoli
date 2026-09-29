package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// The Oracle write refusal at validation time, where the editor shows it,
// for a sink with an explicit uri. A conn_id resolves at run time, and
// refuseUnearnedWrite catches it there (the test below).
func TestOracleSinkRefusedAtValidation(t *testing.T) {
	pipeline := func(sinkURI string) *models.Pipeline {
		return &models.Pipeline{
			ID: "oraval", Name: "oraval",
			Nodes: []models.Node{
				{ID: "src", Type: models.NodeTypeSourceDB, Name: "S",
					Config: map[string]interface{}{"uri": "oracle://u:p@h:1521/svc", "query": "SELECT 1 FROM dual"}},
				{ID: "sink", Type: models.NodeTypeSinkDB, Name: "K",
					Config: map[string]interface{}{"uri": sinkURI, "table": "t", "mode": "append"}},
			},
			Edges: []models.Edge{{From: "src", To: "sink"}},
		}
	}
	ve := ValidatePipeline(pipeline("oracle://u:p@h:1521/svc"))
	if !ve.HasErrors() || !strings.Contains(ve.Error(), "read-only") {
		t.Fatalf("an Oracle sink must fail validation by name, got: %v", ve)
	}
	// Reading from Oracle into another backend validates clean.
	if ve := ValidatePipeline(pipeline("postgres://u:p@h/db")); ve.HasErrors() {
		t.Errorf("an Oracle source must validate: %v", ve)
	}
}

// A sink_db through a saved Oracle connection is refused at run time, before
// anything connects: the host here does not exist, so any other failure
// would name the connection attempt instead.
func TestOracleSinkThroughConnectionIsRefusedInTheRun(t *testing.T) {
	dir := t.TempDir()
	csv := filepath.Join(dir, "in.csv")
	if err := os.WriteFile(csv, []byte("id,city\n1,lisbon\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.NewSQLiteStore(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.CreateConnection(&models.Connection{
		ConnID: "ora", Type: models.ConnTypeOracle,
		Host: "oracle.invalid", Port: 1521, Schema: "ORCL", Login: "etl", Password: "pw",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	eng := drainEngineOnCleanup(t, NewEngine(st))
	eng.ConnResolver = NewConnectionResolver(st, nil)
	p := &models.Pipeline{
		ID: "ora-write", Name: "oracle write", Enabled: true,
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceFile, Name: "Read", Config: map[string]interface{}{"path": csv, "format": "csv"}},
			{ID: "sink", Type: models.NodeTypeSinkDB, Name: "Write", Config: map[string]interface{}{
				"conn_id": "ora", "table": "cities", "mode": "overwrite"}},
		},
		Edges:     []models.Edge{{From: "src", To: "sink"}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := st.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	run, err := eng.RunPipeline(p.ID)
	reason := ""
	if run != nil {
		if run.Status == models.RunStatusSuccess {
			t.Fatal("a write to Oracle succeeded")
		}
		reason = run.Error
	}
	if err != nil {
		reason += " " + err.Error()
	}
	if !strings.Contains(reason, "Oracle connections are read-only") {
		t.Fatalf("the run must fail with the read-only refusal, got: %q", reason)
	}
}
