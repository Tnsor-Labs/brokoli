package store

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func openMigrationTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func recorded(t *testing.T, db *sql.DB) []int {
	t.Helper()
	rows, err := db.Query(`SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var vs []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		vs = append(vs, v)
	}
	return vs
}

// Each step runs once, in version order, and a later start runs only what
// is new.
func TestMigrationsRunOnceInOrder(t *testing.T) {
	db := openMigrationTestDB(t)
	var ran []string
	list := []migration{
		{version: 1, name: "baseline", baseline: func() error { ran = append(ran, "baseline"); return nil }},
		{version: 2, name: "two", apply: func(tx *sql.Tx) error {
			ran = append(ran, "two")
			_, err := tx.Exec(`CREATE TABLE two (x INTEGER)`)
			return err
		}},
	}
	for start := 0; start < 3; start++ {
		if err := runMigrations(db, sqliteMigrationDialect, list); err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.Join(ran, ","); got != "baseline,two" {
		t.Fatalf("ran %s over three starts, want baseline,two once each", got)
	}

	list = append(list, migration{version: 3, name: "three", apply: func(tx *sql.Tx) error {
		ran = append(ran, "three")
		return nil
	}})
	if err := runMigrations(db, sqliteMigrationDialect, list); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ran, ","); got != "baseline,two,three" {
		t.Fatalf("after adding a step, ran %s, want only the new one", got)
	}
	if got := recorded(t, db); len(got) != 3 || got[2] != 3 {
		t.Fatalf("recorded %v, want [1 2 3]", got)
	}
}

// A failing step stops the start, leaves no trace of its own changes and
// is not recorded, so the next start runs it again.
func TestFailingMigrationRollsBackAndIsNotRecorded(t *testing.T) {
	db := openMigrationTestDB(t)
	boom := errors.New("boom")
	fail := true
	list := []migration{
		{version: 1, name: "baseline", baseline: func() error { return nil }},
		{version: 2, name: "two", apply: func(tx *sql.Tx) error {
			if _, err := tx.Exec(`CREATE TABLE half (x INTEGER)`); err != nil {
				return err
			}
			if fail {
				return boom
			}
			return nil
		}},
	}
	err := runMigrations(db, sqliteMigrationDialect, list)
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "migration 2 (two)") {
		t.Fatalf("err = %v, want the step's error naming migration 2", err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = 'half'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("the failed step's table exists (%d, %v): its transaction was not rolled back", n, err)
	}
	if got := recorded(t, db); len(got) != 1 {
		t.Fatalf("recorded %v, want only the baseline", got)
	}

	fail = false
	if err := runMigrations(db, sqliteMigrationDialect, list); err != nil {
		t.Fatal(err)
	}
	if got := recorded(t, db); len(got) != 2 {
		t.Fatalf("recorded %v after the retry, want [1 2]", got)
	}
}

// A database a newer build migrated still opens: a rollback must not be
// an outage.
func TestNewerDatabaseStillOpens(t *testing.T) {
	db := openMigrationTestDB(t)
	list := []migration{{version: 1, name: "baseline", baseline: func() error { return nil }}}
	if err := runMigrations(db, sqliteMigrationDialect, list); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO schema_migrations VALUES (7, 'from a newer build', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := runMigrations(db, sqliteMigrationDialect, list); err != nil {
		t.Fatalf("an older build refused a newer database: %v", err)
	}
}

func TestMalformedMigrationListsAreRefused(t *testing.T) {
	base := func() error { return nil }
	step := func(*sql.Tx) error { return nil }
	for name, list := range map[string][]migration{
		"empty":             nil,
		"gap":               {{version: 1, name: "baseline", baseline: base}, {version: 3, name: "three", apply: step}},
		"no baseline":       {{version: 1, name: "one", apply: step}},
		"second baseline":   {{version: 1, name: "baseline", baseline: base}, {version: 2, name: "two", baseline: base}},
		"unnamed":           {{version: 1, name: "baseline", baseline: base}, {version: 2, apply: step}},
		"step does nothing": {{version: 1, name: "baseline", baseline: base}, {version: 2, name: "two"}},
	} {
		if err := checkMigrationList(list); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for _, list := range [][]migration{(&SQLiteStore{}).migrations(), (&PostgresStore{}).migrations()} {
		if err := checkMigrationList(list); err != nil {
			t.Errorf("a store's own list is refused: %v", err)
		}
	}
}

// A database from before versioning joins at the baseline: the baseline
// runs on it once more, which it can because it is idempotent, and is
// recorded. Then a start does not run it again, while the per-start
// reconcile still does its work.
func TestSQLiteStoreJoinsVersioningAndRunsBaselineOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v.db")
	s, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if v, err := schemaVersion(s.db); err != nil || v != len(s.migrations()) {
		t.Fatalf("a new database is at version %d (%v), want %d", v, err, len(s.migrations()))
	}
	// What an earlier release leaves: the full schema, no record of it.
	if _, err := s.db.Exec(`DROP TABLE schema_migrations`); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("a database from before versioning did not open: %v", err)
	}
	if got := recorded(t, s.db); len(got) == 0 || got[0] != 1 {
		t.Fatalf("recorded %v, want the baseline", got)
	}

	// The baseline creates this index; a database that recorded the
	// baseline does not get it back. That is the point of recording it.
	if _, err := s.db.Exec(`DROP INDEX idx_runs_pipeline_id`); err != nil {
		t.Fatal(err)
	}
	// The reconcile is not a migration: a system role edited in the
	// database is brought back to the code's definition on every start.
	if _, err := s.db.Exec(`UPDATE roles SET permissions = '[]' WHERE id = 'admin'`); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_runs_pipeline_id'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("the baseline ran again on a database that recorded it (index back: %d, %v)", n, err)
	}
	var perms string
	if err := s.db.QueryRow(`SELECT permissions FROM roles WHERE id = 'admin'`).Scan(&perms); err != nil {
		t.Fatal(err)
	}
	if perms == "[]" {
		t.Fatal("the system role was not reconciled on start")
	}
}

// baselineFingerprint is the hash of the baseline as it shipped. If this
// test fails, a change went into the baseline, where databases that have
// recorded version 1 will never run it. Put it in a new migration
// (store/migrations.go). Only a change that alters no schema, such as a
// rename of a helper, may update the hash, and the commit says so.
const baselineFingerprint = "1ad23581a3825b48211294c2349df51ff162e8fe3327c26ef91fd15389d3e871"

// TestBaselineIsFrozen hashes both baselines, every function in this
// package they reach, and the migration files, without comments.
func TestBaselineIsFrozen(t *testing.T) {
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	funcs := map[string][]*ast.FuncDecl{} // by name; methods of both stores share names
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range file.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok {
				funcs[fd.Name.Name] = append(funcs[fd.Name.Name], fd)
			}
		}
	}

	reached := map[*ast.FuncDecl]bool{}
	var walk func(fd *ast.FuncDecl)
	walk = func(fd *ast.FuncDecl) {
		if reached[fd] {
			return
		}
		reached[fd] = true
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			var name string
			switch c := n.(type) {
			case *ast.CallExpr:
				switch fn := c.Fun.(type) {
				case *ast.Ident:
					name = fn.Name
				case *ast.SelectorExpr:
					name = fn.Sel.Name
				}
			case *ast.SelectorExpr:
				// A method value passed along, such as s.GetSetting.
				name = c.Sel.Name
			}
			if name == "Close" {
				// rows.Close() and the like, not the stores' Close.
				return true
			}
			for _, callee := range funcs[name] {
				walk(callee)
			}
			return true
		})
	}
	for _, fd := range funcs["migrateBaseline"] {
		walk(fd)
	}
	if len(funcs["migrateBaseline"]) != 2 {
		t.Fatalf("found %d migrateBaseline functions, want one per store", len(funcs["migrateBaseline"]))
	}

	var parts []string
	for fd := range reached {
		var buf bytes.Buffer
		doc := fd.Doc
		fd.Doc = nil
		if err := printer.Fprint(&buf, fset, fd); err != nil {
			t.Fatal(err)
		}
		fd.Doc = doc
		parts = append(parts, buf.String())
	}
	err = fs.WalkDir(os.DirFS("migrations"), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(filepath.Join("migrations", p))
		parts = append(parts, p+"\n"+string(b))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	got := hex.EncodeToString(sum[:])

	if got != baselineFingerprint {
		var names []string
		for fd := range reached {
			names = append(names, fd.Name.Name)
		}
		sort.Strings(names)
		t.Fatalf("the baseline changed (fingerprint %s, want %s).\n"+
			"Databases that recorded schema version 1 never run the baseline again, so a change there reaches new databases only. "+
			"Add a migration instead (store/migrations.go).\nThe baseline covers: %s and store/migrations/*.sql",
			got, baselineFingerprint, strings.Join(names, ", "))
	}
}

// forgetMigrations makes db look like a database from before versioning,
// which has its schema but no record of it: the state an earlier release
// leaves, and the only one in which the baseline runs on existing data.
func forgetMigrations(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`DROP TABLE schema_migrations`); err != nil {
		t.Fatalf("forget migrations: %v", err)
	}
}
