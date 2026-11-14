package engine

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store/storetest"
)

// An incremental migrate must read only rows at or past the high-water mark
// of the last run: a row changed below the mark is not re-read, a row added
// above it is, and the mark advances. Proven end to end on SQLite.
func TestMigrateIncrementalReadsOnlyNewRows(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "src.db")
	dstPath := filepath.Join(dir, "dst.db")

	src := openFor(t, srcPath)
	mustExec(t, src, `CREATE TABLE t (id INTEGER PRIMARY KEY, val TEXT)`)
	mustExec(t, src, `INSERT INTO t (id, val) VALUES (1,'a'),(2,'b'),(3,'c')`)

	st, err := storetest.NewSQLiteStore(filepath.Join(dir, "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	eng := drainEngineOnCleanup(t, NewEngine(st))

	now := time.Now().UTC()
	p := &models.Pipeline{
		ID: "inc-1", Name: "inc", Enabled: true,
		Nodes: []models.Node{{
			ID: "m", Type: models.NodeTypeMigrate, Name: "orders",
			Config: map[string]interface{}{
				"source_uri": srcPath, "source_query": "SELECT * FROM t",
				"dest_uri": dstPath, "dest_table": "t",
				"mode": "upsert", "key_columns": []interface{}{"id"}, "create_table": true,
				"incremental": true, "cursor_column": "id",
			},
		}},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := st.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}

	// Run 1: full load, mark set to the max id.
	if r := mustRun(t, eng, p.ID); r.Status != models.RunStatusSuccess {
		t.Fatalf("run 1: %s", r.Error)
	}
	dst := openFor(t, dstPath)
	if n := count(t, dst, "t"); n != 3 {
		t.Fatalf("after run 1: %d rows, want 3", n)
	}
	if wm, has, _ := st.GetNodeWatermark("inc-1", "m"); !has || wm != "3" {
		t.Fatalf("watermark after run 1 = %q (has=%v), want 3", wm, has)
	}

	// Change a row BELOW the mark and add one ABOVE it.
	mustExec(t, src, `UPDATE t SET val='B' WHERE id=2`)
	mustExec(t, src, `INSERT INTO t (id, val) VALUES (4,'d')`)

	// Run 2: should read id>=3 only.
	if r := mustRun(t, eng, p.ID); r.Status != models.RunStatusSuccess {
		t.Fatalf("run 2: %s", r.Error)
	}
	if n := count(t, dst, "t"); n != 4 {
		t.Fatalf("after run 2: %d rows, want 4", n)
	}
	// The row below the mark must NOT have been re-read.
	if v := scalar(t, dst, `SELECT val FROM t WHERE id=2`); v != "b" {
		t.Errorf("id=2 val=%q, want b -- incremental re-read a row below the mark", v)
	}
	// The row above the mark must be present.
	if v := scalar(t, dst, `SELECT val FROM t WHERE id=4`); v != "d" {
		t.Errorf("id=4 val=%q, want d -- incremental missed a new row", v)
	}
	if wm, _, _ := st.GetNodeWatermark("inc-1", "m"); wm != "4" {
		t.Errorf("watermark after run 2 = %q, want 4", wm)
	}
}

func TestNodeWatermarkStoreRoundTrip(t *testing.T) {
	st, err := storetest.NewSQLiteStore(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	if _, has, err := st.GetNodeWatermark("p", "n"); err != nil || has {
		t.Fatalf("empty get: has=%v err=%v, want false/nil", has, err)
	}
	if err := st.SetNodeWatermark("p", "n", "42"); err != nil {
		t.Fatal(err)
	}
	if v, has, _ := st.GetNodeWatermark("p", "n"); !has || v != "42" {
		t.Fatalf("get = %q (has=%v), want 42", v, has)
	}
	if err := st.SetNodeWatermark("p", "n", "99"); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := st.GetNodeWatermark("p", "n"); v != "99" {
		t.Fatalf("after update = %q, want 99", v)
	}
	// A different node keeps its own mark.
	if _, has, _ := st.GetNodeWatermark("p", "other"); has {
		t.Error("another node should not share the mark")
	}
}

// --- small sqlite helpers ---

func mustExec(t *testing.T, db *sql.DB, q string) {
	t.Helper()
	if _, err := db.Exec(q); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func mustRun(t *testing.T, eng *Engine, pipelineID string) *models.Run {
	t.Helper()
	run, err := eng.RunPipeline(pipelineID)
	if err != nil {
		t.Fatalf("run %s: %v", pipelineID, err)
	}
	return run
}

func count(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func scalar(t *testing.T, db *sql.DB, q string) string {
	t.Helper()
	var v string
	if err := db.QueryRow(q).Scan(&v); err != nil {
		t.Fatalf("scalar %q: %v", q, err)
	}
	return v
}
