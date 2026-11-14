package engine

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
	"github.com/Tnsor-Labs/brokoli/store/storetest"
)

// migratePipeline builds a one-node migrate pipeline between two sqlite files.
func migratePipeline(id, query, destTable, mode string, keyColumns []interface{}) *models.Pipeline {
	now := time.Now().UTC()
	return &models.Pipeline{
		ID: id, Name: id, Enabled: true, CreatedAt: now, UpdatedAt: now,
		Nodes: []models.Node{{
			ID: "m", Type: models.NodeTypeMigrate, Name: "sync",
			Config: map[string]interface{}{
				"source_query": query, "dest_table": destTable,
				"mode": mode, "create_table": true,
			},
		}},
	}
}

func reconcileEngine(t *testing.T, dir string) (*Engine, *store.SQLiteStore) {
	t.Helper()
	st, err := storetest.NewSQLiteStore(filepath.Join(dir, "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return drainEngineOnCleanup(t, NewEngine(st)), st
}

// An incremental/upsert migrate onto an existing destination that has no
// unique key on its key columns used to fail with a bare ON CONFLICT error.
// The reconcile now adds a unique index (SQLite cannot alter a primary key
// in, but ON CONFLICT reads a unique index the same way), so the sync
// succeeds and upserts. This is the exact gap the managed-syncs testing hit.
func TestReconcileAddsUniqueKeyToExistingDestForUpsert(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "src.db")
	dstPath := filepath.Join(dir, "dst.db")

	src := openFor(t, srcPath)
	mustExec(t, src, `CREATE TABLE s (id INTEGER PRIMARY KEY, name TEXT)`)
	mustExec(t, src, `INSERT INTO s (id, name) VALUES (1,'a'),(2,'b')`)

	// The destination exists already, with NO primary key or unique index.
	dst := openFor(t, dstPath)
	mustExec(t, dst, `CREATE TABLE dst (id INTEGER, name TEXT)`)

	eng, st := reconcileEngine(t, dir)
	p := migratePipeline("rk-1", "SELECT * FROM s", "dst", "upsert", []interface{}{"id"})
	p.Nodes[0].Config["source_uri"] = srcPath
	p.Nodes[0].Config["dest_uri"] = dstPath
	p.Nodes[0].Config["key_columns"] = []interface{}{"id"}
	if err := st.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}

	if r := mustRun(t, eng, p.ID); r.Status != models.RunStatusSuccess {
		t.Fatalf("run 1 (should succeed after reconcile adds the key): %s", r.Error)
	}
	if n := count(t, dst, "dst"); n != 2 {
		t.Fatalf("after run 1: %d rows, want 2", n)
	}
	// The reconcile added a unique key over (id).
	if covered, known := destHasUniqueKey(context.Background(), dstPath, getDialect("sqlite"), "dst", []string{"id"}); !known || !covered {
		t.Fatalf("expected a unique key on (id) after reconcile; covered=%v known=%v", covered, known)
	}

	// Change an existing row and add a new one: the next run must upsert,
	// not duplicate.
	mustExec(t, src, `UPDATE s SET name='A' WHERE id=1`)
	mustExec(t, src, `INSERT INTO s (id, name) VALUES (3,'c')`)
	if r := mustRun(t, eng, p.ID); r.Status != models.RunStatusSuccess {
		t.Fatalf("run 2: %s", r.Error)
	}
	if n := count(t, dst, "dst"); n != 3 {
		t.Fatalf("after run 2: %d rows, want 3 (upsert must not duplicate id=1)", n)
	}
	if v := scalar(t, dst, `SELECT name FROM dst WHERE id=1`); v != "A" {
		t.Errorf("id=1 name=%q, want A (upsert should have updated it)", v)
	}
}

// When the destination already has a primary key over the key columns, the
// reconcile leaves it alone: the upsert works and no second unique index is
// added.
func TestReconcileLeavesExistingKeyAlone(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "src.db")
	dstPath := filepath.Join(dir, "dst.db")

	src := openFor(t, srcPath)
	mustExec(t, src, `CREATE TABLE s (id INTEGER PRIMARY KEY, name TEXT)`)
	mustExec(t, src, `INSERT INTO s (id, name) VALUES (1,'a')`)

	dst := openFor(t, dstPath)
	mustExec(t, dst, `CREATE TABLE dst (id INTEGER PRIMARY KEY, name TEXT)`)

	eng, st := reconcileEngine(t, dir)
	p := migratePipeline("rk-2", "SELECT * FROM s", "dst", "upsert", nil)
	p.Nodes[0].Config["source_uri"] = srcPath
	p.Nodes[0].Config["dest_uri"] = dstPath
	p.Nodes[0].Config["key_columns"] = []interface{}{"id"}
	if err := st.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	if r := mustRun(t, eng, p.ID); r.Status != models.RunStatusSuccess {
		t.Fatalf("run: %s", r.Error)
	}
	// Exactly one unique index/pk over (id): the pre-existing primary key.
	sets, err := sqliteUniqueKeySets(context.Background(), dstPath, getDialect("sqlite"), "dst")
	if err != nil {
		t.Fatal(err)
	}
	onID := 0
	for _, s := range sets {
		if len(s) == 1 && s[0] == "id" {
			onID++
		}
	}
	if onID != 1 {
		t.Errorf("unique keys over (id) = %d, want 1 (reconcile should not add a redundant one); sets=%v", onID, sets)
	}
}

// The reconcile adds a column the source has gained and the destination
// lacks, so a migrate onto a narrower existing table keeps working.
func TestReconcileAddsMissingColumn(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "src.db")
	dstPath := filepath.Join(dir, "dst.db")

	src := openFor(t, srcPath)
	mustExec(t, src, `CREATE TABLE s (id INTEGER PRIMARY KEY, name TEXT, city TEXT)`)
	mustExec(t, src, `INSERT INTO s (id, name, city) VALUES (1,'a','lisbon')`)

	// The destination exists but has no "city" column yet.
	dst := openFor(t, dstPath)
	mustExec(t, dst, `CREATE TABLE dst (id INTEGER PRIMARY KEY, name TEXT)`)

	eng, st := reconcileEngine(t, dir)
	p := migratePipeline("rc-1", "SELECT * FROM s", "dst", "overwrite", nil)
	p.Nodes[0].Config["source_uri"] = srcPath
	p.Nodes[0].Config["dest_uri"] = dstPath
	if err := st.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	if r := mustRun(t, eng, p.ID); r.Status != models.RunStatusSuccess {
		t.Fatalf("run: %s", r.Error)
	}
	if v := scalar(t, dst, `SELECT city FROM dst WHERE id=1`); v != "lisbon" {
		t.Errorf("city=%q, want lisbon -- the column should have been added and loaded", v)
	}
}

func TestAddUniqueKeyStatementByDialect(t *testing.T) {
	cases := map[string]struct {
		addable bool
		wantSub string
	}{
		"postgres":  {true, "ADD CONSTRAINT"},
		"mysql":     {true, "ADD CONSTRAINT"},
		"sqlite":    {true, "CREATE UNIQUE INDEX IF NOT EXISTS"},
		"sqlserver": {false, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			stmt, addable := addUniqueKeyStatement(getDialect(name), "orders", []string{"id"})
			if addable != tc.addable {
				t.Fatalf("addable = %v, want %v (stmt %q)", addable, tc.addable, stmt)
			}
			if tc.addable && !strings.Contains(stmt, tc.wantSub) {
				t.Errorf("stmt %q does not contain %q", stmt, tc.wantSub)
			}
		})
	}
}

func TestUniqueKeyNameIsDeterministicAndSafe(t *testing.T) {
	a := uniqueKeyName("public.orders", []string{"id"})
	b := uniqueKeyName("public.orders", []string{"id"})
	if a != b {
		t.Errorf("name not deterministic: %q vs %q", a, b)
	}
	if a != "brk_uk_orders_id" {
		t.Errorf("name = %q, want brk_uk_orders_id", a)
	}
}
