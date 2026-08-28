package engine

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/store"
)

// ADR-027 phase 3: overwrite arrives with its weaker semantics observed by
// tests rather than hidden, upsert's refusal moves to validation time, the
// pushdown cells are asserted disabled end to end, and the resume carve-out
// reaches the run log of the exact run at risk.

// Overwrite replaces: the equivalence argument against the statement-path
// escape hatch, same shape as the append corpus.
func TestClickHouseOverwriteMatchesTheStatementPath(t *testing.T) {
	dsn := clickhouseTestDSN(t)
	db := openFor(t, dsn)

	results := map[string]string{}
	for _, path := range []string{"statement", "bulk"} {
		table := "chow_" + path
		db.Exec("DROP TABLE IF EXISTS " + table)
		if _, err := db.Exec("CREATE TABLE " + table +
			" (id Int64, city String, amount Float64, note String) ENGINE = MergeTree ORDER BY id"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Exec("DROP TABLE IF EXISTS " + table) })
		// Rows the overwrite must destroy.
		if _, err := db.Exec("INSERT INTO " + table +
			" VALUES (999, 'stale', 0, ''), (998, 'stale', 0, '')"); err != nil {
			t.Fatal(err)
		}

		if path == "statement" {
			t.Setenv("BROKOLI_SINK_COPY", "false")
		} else {
			t.Setenv("BROKOLI_SINK_COPY", "true")
		}
		cfg := SQLGenConfig{Dialect: "clickhouse", Table: table, BatchSize: 2, Mode: ModeOverwrite}
		ds := chHostileRows()

		w, bulk := bulkWriterFor(cfg)
		if wantBulk := path == "bulk"; bulk != wantBulk {
			t.Fatalf("%s run offered the wrong path (bulk=%v)", path, bulk)
		}
		if bulk {
			if _, err := bulkWriteRows(w, dsn, cfg, ds); err != nil {
				t.Fatalf("bulk overwrite: %v", err)
			}
		} else {
			stmt, err := GenerateSQL(cfg, ds)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ExecuteSQL(dsn, stmt); err != nil {
				t.Fatalf("statement overwrite: %v", err)
			}
		}

		rows, err := db.Query("SELECT id, city FROM " + table + " ORDER BY id")
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for rows.Next() {
			var id int64
			var city string
			if err := rows.Scan(&id, &city); err != nil {
				t.Fatal(err)
			}
			out = append(out, fmt.Sprintf("%d|%s", id, city))
		}
		rows.Close()
		results[path] = strings.Join(out, " ")
	}

	if results["bulk"] != results["statement"] {
		t.Errorf("bulk and statement overwrites diverged:\nstatement: %q\nbulk:      %q",
			results["statement"], results["bulk"])
	}
	if strings.Contains(results["bulk"], "stale") {
		t.Error("overwrite left the previous rows behind")
	}
}

// The weaker semantics, observed on purpose: a failure after the truncate
// leaves neither the old rows nor the full new set -- the truncate and the
// committed batches are durable, because there is no transaction to roll
// them back into. This test is the documentation with teeth: if ClickHouse
// ever gains a way to make this atomic and the writer uses it, this test
// fails and the docs get updated to say something better.
func TestClickHouseOverwriteFailureLeavesTheWeakerState(t *testing.T) {
	dsn := clickhouseTestDSN(t)
	db := openFor(t, dsn)
	db.Exec("DROP TABLE IF EXISTS chow_weak")
	if _, err := db.Exec(
		"CREATE TABLE chow_weak (id Int64, city String) ENGINE = MergeTree ORDER BY id"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec("DROP TABLE IF EXISTS chow_weak") })
	if _, err := db.Exec("INSERT INTO chow_weak VALUES (999, 'old')"); err != nil {
		t.Fatal(err)
	}

	cfg := SQLGenConfig{Dialect: "clickhouse", Table: "chow_weak", Mode: ModeOverwrite}
	w, ok := bulkWriterFor(cfg)
	if !ok {
		t.Fatal("no writer")
	}

	// Two batches: the first is good and commits; the second carries a
	// value the driver cannot put in an Int64 column, so the write fails
	// after real work has been acknowledged.
	batches := []*common.DataSet{
		{Columns: []string{"id", "city"}, Rows: []common.DataRow{{"id": int64(1), "city": "new"}}},
		{Columns: []string{"id", "city"}, Rows: []common.DataRow{{"id": "not-a-number", "city": "bad"}}},
	}
	i := 0
	next := func() (*common.DataSet, error) {
		if i >= len(batches) {
			return nil, io.EOF
		}
		b := batches[i]
		i++
		return b, nil
	}
	if _, err := w(context.Background(), dsn, cfg, []string{"id", "city"}, next); err == nil {
		t.Fatal("the second batch should have failed the write")
	}

	// What remains is exactly the weaker promise: old rows gone, the
	// acknowledged batch present, the failed one absent.
	rows, err := db.Query("SELECT id, city FROM chow_weak ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var id int64
		var city string
		if err := rows.Scan(&id, &city); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%d|%s", id, city))
	}
	if len(got) != 1 || got[0] != "1|new" {
		t.Errorf("after a failed overwrite the table holds %v; the documented state is the truncate "+
			"plus the acknowledged batch (1|new), nothing else", got)
	}
}

// Upsert's refusal at validation time: the editor's validate call reports
// it before anything runs.
func TestClickHouseUpsertRefusedAtValidation(t *testing.T) {
	p := &models.Pipeline{
		ID: "chval", Name: "chval",
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceDB, Name: "S",
				Config: map[string]interface{}{"uri": "postgres://u:p@h/db", "query": "SELECT 1"}},
			{ID: "sink", Type: models.NodeTypeSinkDB, Name: "K",
				Config: map[string]interface{}{
					"uri": "clickhouse://u:p@h:9000/db", "table": "t",
					"mode": "upsert", "key_columns": []interface{}{"id"}}},
		},
		Edges: []models.Edge{{From: "src", To: "sink"}},
	}
	ve := ValidatePipeline(p)
	if !ve.HasErrors() {
		t.Fatal("a ClickHouse upsert must fail validation")
	}
	if !strings.Contains(ve.Error(), "ReplacingMergeTree") {
		t.Errorf("the validation error should explain the native alternative, got: %v", ve)
	}

	// Append validates clean: the refusal is the mode's, not the backend's.
	p.Nodes[1].Config["mode"] = "append"
	delete(p.Nodes[1].Config, "key_columns")
	if ve := ValidatePipeline(p); ve.HasErrors() {
		t.Errorf("a ClickHouse append must validate: %v", ve)
	}
}

// The pushdown cells, asserted disabled end to end: a same-server ClickHouse
// source -> transform -> sink pipeline runs and produces correct rows, and
// the logs prove every row moved through the engine -- no segment compiled,
// nothing "kept in the database as a reference".
func TestClickHouseSameServerPipelineDoesNotPushDown(t *testing.T) {
	dsn := clickhouseTestDSN(t)
	db := openFor(t, dsn)
	db.Exec("DROP TABLE IF EXISTS chpd_src")
	db.Exec("DROP TABLE IF EXISTS chpd_dst")
	if _, err := db.Exec("CREATE TABLE chpd_src (id Int64, city String) ENGINE = MergeTree ORDER BY id"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE chpd_dst (id Int64, city String) ENGINE = MergeTree ORDER BY id"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec("DROP TABLE IF EXISTS chpd_src"); db.Exec("DROP TABLE IF EXISTS chpd_dst") })
	if _, err := db.Exec("INSERT INTO chpd_src VALUES (1, 'lisbon'), (2, 'porto'), (3, 'faro')"); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	st, err := store.NewSQLiteStore(filepath.Join(dir, "pd.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	eng := drainEngineOnCleanup(t, NewEngine(st))

	p := &models.Pipeline{
		ID: "chpd", Name: "chpd", Enabled: true,
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceDB, Name: "S",
				Config: map[string]interface{}{"uri": dsn, "query": "SELECT id, city FROM chpd_src ORDER BY id"}},
			{ID: "xf", Type: models.NodeTypeTransform, Name: "X",
				Config: map[string]interface{}{"rules": []map[string]interface{}{
					{"type": "filter_rows", "condition": "id > 1"}}}},
			{ID: "sink", Type: models.NodeTypeSinkDB, Name: "K",
				Config: map[string]interface{}{"uri": dsn, "table": "chpd_dst", "mode": "append"}},
		},
		Edges:     []models.Edge{{From: "src", To: "xf"}, {From: "xf", To: "sink"}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := st.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	run, err := eng.RunPipeline(p.ID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run failed: %s", run.Error)
	}

	var n uint64
	if err := db.QueryRow("SELECT count() FROM chpd_dst").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("dst rows = %d, want 2 (filter dropped id=1)", n)
	}

	logs, err := st.GetLogs(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range logs {
		for _, marker := range []string{
			"as a reference", "Compiled into the upstream query", "the engine moved no rows",
		} {
			if strings.Contains(l.Message, marker) {
				t.Errorf("pushdown engaged for a ClickHouse segment (%q); every cell must stay "+
					"disabled until ADR-027 phase 4 earns it with the differential corpus", l.Message)
			}
		}
	}
}

// The resume carve-out reaches the run at risk: a resumed run that re-runs
// a ClickHouse write warns, in its own log, that already-committed batches
// are still in the table.
func TestClickHouseResumedRunWarnsAboutDuplicates(t *testing.T) {
	dsn := clickhouseTestDSN(t)
	db := openFor(t, dsn)
	db.Exec("DROP TABLE IF EXISTS chres_dst")
	t.Cleanup(func() { db.Exec("DROP TABLE IF EXISTS chres_dst") })
	// Deliberately NOT created: the first run's sink fails against the
	// missing table, leaving a failed run to resume.

	dir := t.TempDir()
	csv := filepath.Join(dir, "in.csv")
	if err := os.WriteFile(csv, []byte("id,city\n1,lisbon\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.NewSQLiteStore(filepath.Join(dir, "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	eng := drainEngineOnCleanup(t, NewEngine(st))

	p := &models.Pipeline{
		ID: "chres", Name: "chres", Enabled: true,
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceFile, Name: "S",
				Config: map[string]interface{}{"path": csv, "format": "csv"}},
			{ID: "sink", Type: models.NodeTypeSinkDB, Name: "K",
				Config: map[string]interface{}{"uri": dsn, "table": "chres_dst", "mode": "append"}},
		},
		Edges:     []models.Edge{{From: "src", To: "sink"}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := st.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	first, err := eng.RunPipeline(p.ID)
	if err == nil && first.Status == models.RunStatusSuccess {
		t.Fatal("the first run should have failed against the missing table")
	}
	if first == nil {
		t.Fatal("no run recorded")
	}

	// Create the table so the resume can proceed past the warning. String
	// columns deliberately: a CSV source delivers strings, and the native
	// batch does not coerce them into numeric columns (the driver refuses
	// "converting string to Int64") -- a real gap between the bulk and
	// statement paths for string-typed inputs, recorded on #382 rather
	// than papered over here.
	if _, err := db.Exec(
		"CREATE TABLE chres_dst (id String, city String) ENGINE = MergeTree ORDER BY id"); err != nil {
		t.Fatal(err)
	}

	resumed, err := eng.ResumeRun(first.ID)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if resumed.ResumedFromRunID != first.ID {
		t.Fatalf("resumed run does not record its origin")
	}

	logs, err := st.GetLogs(resumed.ID)
	if err != nil {
		t.Fatal(err)
	}
	warned := false
	for _, l := range logs {
		if strings.Contains(l.Message, "no transaction to roll them back") {
			warned = true
		}
	}
	if !warned {
		t.Error("the resumed run's log must carry the duplicate-rows warning -- it is the " +
			"operator-visible half of ADR-027's resume carve-out")
	}
}
