package engine

// brokoli-sdk#12: sink_db writes a dataset on its own — source -> sink_db,
// no sql_generate node — generating INSERT/overwrite/upsert SQL from the
// table + mode + input rows and executing it against the target database.

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// runSinkPipeline runs source_file(csv) -> sink_db(target, mode, keyCols)
// end to end and returns the run.
func runSinkPipeline(t *testing.T, csv, targetDB, table, mode string, keyCols []string) *models.Run {
	t.Helper()
	dir := filepath.Dir(csv)
	tag := filepath.Base(csv) + "-" + mode
	s, err := store.NewSQLiteStore(filepath.Join(dir, "meta-"+tag+".db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	sinkCfg := map[string]interface{}{"uri": targetDB, "table": table}
	if mode != "" {
		sinkCfg["mode"] = mode
	}
	if len(keyCols) > 0 {
		ks := make([]interface{}, len(keyCols))
		for i, k := range keyCols {
			ks[i] = k
		}
		sinkCfg["key_columns"] = ks
	}
	pipeline := &models.Pipeline{
		ID: "sink-" + tag, Name: "sink " + tag,
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceFile, Name: "Src", Config: map[string]interface{}{"path": csv, "format": "csv"}},
			{ID: "sink", Type: models.NodeTypeSinkDB, Name: "Sink", Config: sinkCfg},
		},
		Edges:     []models.Edge{{From: "src", To: "sink"}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}
	eng := NewEngine(s)
	defer eng.Close(context.Background())
	run, err := eng.RunPipeline(pipeline.ID)
	if err != nil {
		t.Fatalf("RunPipeline(%s): %v", mode, err)
	}
	return run
}

func querySinkCount(t *testing.T, dbPath, q string) int {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(q).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", q, err)
	}
	return n
}

func writeSinkCSV(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSinkDB_AppendAndOverwrite(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.db")
	if _, err := ExecuteSQL(target, `CREATE TABLE users (id INTEGER, name TEXT);`); err != nil {
		t.Fatal(err)
	}
	csv := filepath.Join(dir, "in.csv")
	writeSinkCSV(t, csv, "id,name\n1,Alice\n2,Bob\n")

	// Append (default) writes the two rows with no sql_generate node.
	if run := runSinkPipeline(t, csv, target, "users", "", nil); run.Status != models.RunStatusSuccess {
		t.Fatalf("append run status = %s, want success (error: %s)", run.Status, run.Error)
	}
	if n := querySinkCount(t, target, "SELECT COUNT(*) FROM users"); n != 2 {
		t.Fatalf("after append: %d rows, want 2", n)
	}

	// Append again accumulates.
	runSinkPipeline(t, csv, target, "users", "append", nil)
	if n := querySinkCount(t, target, "SELECT COUNT(*) FROM users"); n != 4 {
		t.Fatalf("after 2nd append: %d rows, want 4", n)
	}

	// Overwrite clears first, then writes the two rows.
	runSinkPipeline(t, csv, target, "users", "overwrite", nil)
	if n := querySinkCount(t, target, "SELECT COUNT(*) FROM users"); n != 2 {
		t.Fatalf("after overwrite: %d rows, want 2", n)
	}
}

func TestSinkDB_Upsert(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.db")
	if _, err := ExecuteSQL(target, `CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT);`); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(dir, "first.csv")
	writeSinkCSV(t, first, "id,name\n1,Alice\n2,Bob\n")
	if run := runSinkPipeline(t, first, target, "users", "upsert", []string{"id"}); run.Status != models.RunStatusSuccess {
		t.Fatalf("upsert run status = %s (error: %s)", run.Status, run.Error)
	}
	if n := querySinkCount(t, target, "SELECT COUNT(*) FROM users"); n != 2 {
		t.Fatalf("after first upsert: %d rows, want 2", n)
	}

	// Re-upsert id=1 with a new name and add id=3: id=1 updates in place,
	// no duplicate row.
	second := filepath.Join(dir, "second.csv")
	writeSinkCSV(t, second, "id,name\n1,Alicia\n3,Carol\n")
	if run := runSinkPipeline(t, second, target, "users", "upsert", []string{"id"}); run.Status != models.RunStatusSuccess {
		t.Fatalf("second upsert status = %s (error: %s)", run.Status, run.Error)
	}
	if n := querySinkCount(t, target, "SELECT COUNT(*) FROM users"); n != 3 {
		t.Fatalf("after second upsert: %d rows, want 3 (1 updated, 1 added)", n)
	}
	if n := querySinkCount(t, target, "SELECT COUNT(*) FROM users WHERE id=1 AND name='Alicia'"); n != 1 {
		t.Fatalf("id=1 should have been updated to Alicia, got %d", n)
	}
}

// runMigratePipeline runs a single migrate node (source_uri -> dest_uri)
// end to end and returns the run.
func runMigratePipeline(t *testing.T, srcURI, query, dstURI, table, mode string, keyCols []string, tag string) *models.Run {
	t.Helper()
	dir := t.TempDir()
	s, err := store.NewSQLiteStore(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	cfg := map[string]interface{}{
		"source_uri": srcURI, "source_query": query,
		"dest_uri": dstURI, "dest_table": table,
	}
	if mode != "" {
		cfg["mode"] = mode
	}
	if len(keyCols) > 0 {
		ks := make([]interface{}, len(keyCols))
		for i, k := range keyCols {
			ks[i] = k
		}
		cfg["key_columns"] = ks
	}
	pipeline := &models.Pipeline{
		ID: "mig-" + tag, Name: "mig " + tag,
		Nodes:     []models.Node{{ID: "mig", Type: models.NodeTypeMigrate, Name: "Mig", Config: cfg}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}
	eng := NewEngine(s)
	defer eng.Close(context.Background())
	run, err := eng.RunPipeline(pipeline.ID)
	if err != nil {
		t.Fatalf("RunPipeline(migrate %s): %v", tag, err)
	}
	return run
}

func TestMigrate_OverwriteAndUpsert(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.db")
	dst := filepath.Join(dir, "dst.db")
	if _, err := ExecuteSQL(src, `CREATE TABLE src (id INTEGER, name TEXT); INSERT INTO src (id, name) VALUES (1, 'Alice'), (2, 'Bob');`); err != nil {
		t.Fatal(err)
	}
	if _, err := ExecuteSQL(dst, `CREATE TABLE dst (id INTEGER PRIMARY KEY, name TEXT);`); err != nil {
		t.Fatal(err)
	}

	q := "SELECT id, name FROM src"

	// Upsert: first copy writes both rows.
	if run := runMigratePipeline(t, src, q, dst, "dst", "upsert", []string{"id"}, "up1"); run.Status != models.RunStatusSuccess {
		t.Fatalf("upsert migrate status = %s (error: %s)", run.Status, run.Error)
	}
	if n := querySinkCount(t, dst, "SELECT COUNT(*) FROM dst"); n != 2 {
		t.Fatalf("after upsert migrate: %d rows, want 2", n)
	}

	// Change the source and upsert again: id=1 updates, id=3 is added.
	if _, err := ExecuteSQL(src, `UPDATE src SET name='Alicia' WHERE id=1; INSERT INTO src (id, name) VALUES (3, 'Carol');`); err != nil {
		t.Fatal(err)
	}
	runMigratePipeline(t, src, q, dst, "dst", "upsert", []string{"id"}, "up2")
	if n := querySinkCount(t, dst, "SELECT COUNT(*) FROM dst"); n != 3 {
		t.Fatalf("after 2nd upsert migrate: %d rows, want 3", n)
	}
	if n := querySinkCount(t, dst, "SELECT COUNT(*) FROM dst WHERE id=1 AND name='Alicia'"); n != 1 {
		t.Fatal("id=1 should have been updated to Alicia")
	}

	// Overwrite: dest currently has 3 rows; overwrite-migrating the 3 source
	// rows replaces them (still 3, but cleared first — prove by pre-adding a
	// row that must not survive).
	if _, err := ExecuteSQL(dst, `INSERT INTO dst (id, name) VALUES (99, 'Ghost');`); err != nil {
		t.Fatal(err)
	}
	runMigratePipeline(t, src, q, dst, "dst", "overwrite", nil, "ow1")
	if n := querySinkCount(t, dst, "SELECT COUNT(*) FROM dst WHERE id=99"); n != 0 {
		t.Fatal("overwrite should have cleared the pre-existing id=99 row")
	}
	if n := querySinkCount(t, dst, "SELECT COUNT(*) FROM dst"); n != 3 {
		t.Fatalf("after overwrite migrate: %d rows, want the 3 source rows", n)
	}
}
