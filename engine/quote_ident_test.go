package engine

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// quoteIdent wrapped a name in the dialect's quotes without escaping a
// quote inside it, so it was safe only where every caller had run
// validateIdentifier first. One path did not: the same-server Postgres
// migrate quotes the column names of the source query's result, which come
// from whatever table the query reads. A quote in the name is now doubled,
// which is how every one of these dialects writes it inside a quoted
// identifier, so a quoted name cannot end early whoever chose it.
func TestQuoteIdentEscapesTheQuoteCharacter(t *testing.T) {
	cases := []struct {
		dialect, quote, in, want string
	}{
		{"postgres", `"`, `say "hi"`, `"say ""hi"""`},
		{"postgres", `"`, `x") ; DROP TABLE t; --`, `"x"") ; DROP TABLE t; --"`},
		{"postgres", `"`, `analytics.daily`, `"analytics"."daily"`},
		{"mysql", "`", "a`b", "`a``b`"},
		{"sqlserver", "[", "a]b", "[a]]b]"},
		{"sqlserver", "[", "plain", "[plain]"},
	}
	for _, c := range cases {
		d := getDialect(c.dialect)
		if d.quoteChar != c.quote {
			t.Fatalf("%s quotes with %q, the test expects %q", c.dialect, d.quoteChar, c.quote)
		}
		if got := d.quoteIdent(c.in); got != c.want {
			t.Errorf("%s quoteIdent(%q) = %s, want %s", c.dialect, c.in, got, c.want)
		}
	}
}

// End to end on Postgres: a migrate whose source and destination are the
// same server stages the query there and merges by the result's column
// names. A column whose name holds a double quote broke the merge's SQL
// (and could have extended it); it now migrates like any other.
func TestSameServerMigrateHandlesAColumnNameWithAQuote(t *testing.T) {
	uri := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if uri == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	db, err := sql.Open("pgx", uri)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	tag := fmt.Sprintf("%d", time.Now().UnixNano())
	src, dst := "brokoli_quote_src_"+tag, "brokoli_quote_dst_"+tag
	if _, err := db.Exec(`CREATE TABLE ` + src + ` (id bigint PRIMARY KEY, "say ""hi""" text)`); err != nil {
		t.Fatalf("create source: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE ` + dst + ` (id bigint PRIMARY KEY, "say ""hi""" text)`); err != nil {
		t.Fatalf("create destination: %v", err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`DROP TABLE IF EXISTS ` + src + `; DROP TABLE IF EXISTS ` + dst) })
	if _, err := db.Exec(`INSERT INTO ` + src + ` VALUES (1, 'hello'), (2, 'world')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	meta, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer meta.Close()
	p := &models.Pipeline{
		ID: "quote-migrate-" + tag, Name: "quote migrate",
		Nodes: []models.Node{{
			ID: "migrate", Type: models.NodeTypeMigrate, Name: "Migrate",
			Config: map[string]interface{}{
				"source_uri": uri, "source_query": "SELECT * FROM " + src,
				"dest_uri": uri, "dest_table": dst, "mode": "upsert",
				"key_columns": []interface{}{"id"}, "create_table": false,
			},
		}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := meta.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	eng := NewEngine(meta)
	defer eng.Close(context.Background())

	run, err := eng.RunPipeline(p.ID)
	if err != nil {
		t.Fatalf("RunPipeline: %v", err)
	}
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %s: %s", run.Status, run.Error)
	}
	logs, _ := meta.GetLogs(run.ID)
	direct := false
	for _, l := range logs {
		if l.Message == "Migration: staging the source query inside PostgreSQL" {
			direct = true
		}
	}
	if !direct {
		t.Fatal("the migrate did not take the same-server path this test is about")
	}
	var got string
	if err := db.QueryRow(`SELECT "say ""hi""" FROM ` + dst + ` WHERE id = 2`).Scan(&got); err != nil || got != "world" {
		t.Fatalf("destination row 2 = %q, %v; want world", got, err)
	}
}

// The same-server path quoted dest_table and key_columns without the
// validation GenerateSQL applies to them. They now get it, with the same
// message, before anything reaches the database.
func TestSameServerMigrateValidatesItsIdentifiers(t *testing.T) {
	for _, c := range []struct {
		table string
		keys  []string
	}{
		{`t"; DROP TABLE users; --`, []string{"id"}},
		{"t", []string{`id"`}},
	} {
		_, err := migratePostgresSameServer(context.Background(), "postgres://unused.invalid/db", "SELECT 1", c.table, c.keys, nil, false)
		if err == nil || !containsAll(err.Error(), "invalid identifier") {
			t.Errorf("table %q keys %q: err = %v, want an invalid identifier error", c.table, c.keys, err)
		}
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}
