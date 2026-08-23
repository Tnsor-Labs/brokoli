package api

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tnsor-Labs/brokoli/store"
	_ "github.com/jackc/pgx/v5/stdlib"

	_ "modernc.org/sqlite"
)

// The placeholder rewriter: ? stays ? on SQLite and becomes $n on
// Postgres, counting from one, in order.
func TestUserStorePlaceholderRewrite(t *testing.T) {
	sqlite := &UserStore{dialect: "sqlite"}
	pg := &UserStore{dialect: "postgres"}

	const in = `UPDATE users SET display_name = ?, email = ? WHERE id = ?`
	if got := sqlite.q(in); got != in {
		t.Errorf("sqlite should pass through: %q", got)
	}
	want := `UPDATE users SET display_name = $1, email = $2 WHERE id = $3`
	if got := pg.q(in); got != want {
		t.Errorf("postgres:\n got %q\nwant %q", got, want)
	}
	if got := pg.q(`SELECT 1`); got != `SELECT 1` {
		t.Errorf("no placeholders should be untouched: %q", got)
	}
}

// Account lockout, driven through the store layer on whichever backend
// is configured. This is the control that was silently absent on
// Postgres: UserStore wrote its own SQL against a raw *sql.DB and guessed
// the column types wrong — an integer into a boolean `success`, an
// RFC3339 string into a timestamptz `attempted_at` — while discarding
// every error. The live control database sat at 0 rows through thousands
// of logins and IsLocked answered "not locked" throughout.
func TestAccountLockoutWorksOnBothDialects(t *testing.T) {
	run := func(t *testing.T, st store.Store, db *sql.DB, wantDialect string) {
		t.Helper()
		us, err := NewUserStore(db)
		if err != nil {
			t.Fatalf("NewUserStore: %v", err)
		}
		if us.dialect != wantDialect {
			t.Fatalf("dialect detected as %q, want %q", us.dialect, wantDialect)
		}
		la, ok := st.(store.LoginAttemptStore)
		if !ok {
			t.Fatalf("%s store does not implement LoginAttemptStore", wantDialect)
		}
		us.UseLoginAttemptStore(la)

		if us.IsLocked("lockme") {
			t.Fatal("a fresh account must not be locked")
		}
		for i := 0; i < lockoutThreshold-1; i++ {
			us.RecordLoginAttempt("lockme", "10.0.0.1", false)
		}
		if us.IsLocked("lockme") {
			t.Errorf("%d failures should not lock the account yet", lockoutThreshold-1)
		}
		us.RecordLoginAttempt("lockme", "10.0.0.1", false)
		if !us.IsLocked("lockme") {
			t.Errorf("%d failed attempts must lock the account", lockoutThreshold)
		}

		// A successful login clears the streak.
		us.ClearAttempts("lockme")
		if us.IsLocked("lockme") {
			t.Error("clearing attempts should unlock the account")
		}
	}

	t.Run("sqlite", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "u.db")
		st, err := store.NewSQLiteStore(path)
		if err != nil {
			t.Fatal(err)
		}
		db, ok := st.RawDB().(*sql.DB)
		if !ok {
			t.Fatal("RawDB did not return *sql.DB")
		}
		run(t, st, db, "sqlite")
	})

	t.Run("postgres", func(t *testing.T) {
		uri := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
		if uri == "" {
			t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
		}
		st, err := store.NewPostgresStore(uri)
		if err != nil {
			t.Fatal(err)
		}
		db, ok := st.RawDB().(*sql.DB)
		if !ok {
			t.Fatal("RawDB did not return *sql.DB")
		}
		if _, err := db.Exec("DELETE FROM login_attempts WHERE username = 'lockme'"); err != nil {
			t.Fatal(err)
		}
		run(t, st, db, "postgres")
	})
}

// Without a backend the store must not pretend to enforce lockout, and
// must not blow up either — the server logs the gap at startup.
func TestLockoutWithoutAStoreIsInertNotFatal(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "u.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	us, err := NewUserStore(db)
	if err != nil {
		t.Fatal(err)
	}
	us.RecordLoginAttempt("nobody", "10.0.0.1", false)
	us.ClearAttempts("nobody")
	if us.IsLocked("nobody") {
		t.Error("with no backend wired, IsLocked should report false rather than lock everyone out")
	}
}

// UserStore must not create login_attempts: the store layer owns that
// schema, and a second creator racing it is how the two diverged.
func TestUserStoreDoesNotCreateLoginAttempts(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "u.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := NewUserStore(db); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='login_attempts'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("UserStore created login_attempts; the store layer owns that table")
	}
}
