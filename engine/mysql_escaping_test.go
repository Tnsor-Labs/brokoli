package engine

import (
	"database/sql"
	"fmt"

	"os"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// mysqlTestURI is the live server these tests need. What a literal means is
// decided by the destination's parser, and a string comparison in Go cannot
// answer that: the whole defect is that the generated text looks correct and
// means something else once MySQL reads it.
func mysqlTestURI(t *testing.T) string {
	t.Helper()
	uri := os.Getenv("BROKOLI_TEST_MYSQL_URL")
	if uri == "" {
		t.Skip("BROKOLI_TEST_MYSQL_URL not set")
	}
	return uri
}

func openMySQLForTest(t *testing.T, uri string) *sql.DB {
	t.Helper()
	db, err := sql.Open("mysql", strings.TrimPrefix(uri, "mysql://"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// Values are rendered into SQL text rather than bound as parameters, so
// dialect.quoteString is the only thing between a source value and the
// destination's parser. It doubled the quote character and did nothing about
// backslash.
//
// MySQL treats a backslash as an escape inside a string literal unless
// NO_BACKSLASH_ESCAPES is set, which is not the default. So a value
// containing one is altered on arrival, and a value ending in one escapes the
// quote meant to close the literal — after which the following text is parsed
// as SQL rather than data. Postgres is unaffected only because
// standard_conforming_strings makes a backslash literal there, which is why
// this survived.
//
// The contract asserted here is fidelity: whatever a source produces must
// arrive unchanged. That is a stronger and more useful property than "no
// injection", and it fails today for values as ordinary as a Windows path.
// hostileValues is one corpus shared by every backend. A value's journey
// through the engine must not depend on where it lands, so the same list is
// asserted against each server rather than each backend getting a list that
// happens to suit it.
func hostileValues() []struct{ name, value string } {
	return []struct{ name, value string }{
		{"plain", "hello"},
		{"single quote", "it's"},
		{"backslash", `a\b`},
		{"windows path", `C:\Users\report.csv`},
		{"trailing backslash", `x\`},
		{"backslash before quote", `x\'y`},
		{"double backslash", `x\\y`},
		{"newline", "line1\nline2"},
		{"tab", "a\tb"},
		{"percent and underscore", `100% a_b`},
		{"injection attempt", `'); DROP TABLE esc_canary; -- `},
		{"injection with trailing backslash", `x\`},
	}
}

func TestMySQLValuesRoundTripIntact(t *testing.T) {
	uri := mysqlTestURI(t)
	assertValuesRoundTrip(t, uri, "mysql", openMySQLForTest(t, uri))
}

// The same corpus against Postgres. It passes today, and the point of having
// it is that it must keep passing: Postgres takes a backslash literally
// (standard_conforming_strings), so escaping there would corrupt values in
// the opposite direction. This is what stops the MySQL fix being applied
// everywhere "to be safe".
func TestPostgresValuesRoundTripIntact(t *testing.T) {
	uri := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if uri == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	db, err := sql.Open("pgx", uri)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	assertValuesRoundTrip(t, uri, "postgres", db)
}

func assertValuesRoundTrip(t *testing.T, uri, dialectName string, db *sql.DB) {
	t.Helper()
	cases := hostileValues()

	for _, s := range []string{
		`DROP TABLE IF EXISTS esc_target`,
		`DROP TABLE IF EXISTS esc_canary`,
		`CREATE TABLE esc_target (id INT, v TEXT)`,
		`CREATE TABLE esc_canary (v TEXT)`,
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP TABLE IF EXISTS esc_target`)
		_, _ = db.Exec(`DROP TABLE IF EXISTS esc_canary`)
	})

	// One statement carrying every case, because a multi-row VALUES is what
	// the sink actually emits and it is where a runaway literal does its
	// damage: the literal escapes into the next row's value.
	ds := &common.DataSet{Columns: []string{"id", "v"}}
	for i, tc := range cases {
		ds.Rows = append(ds.Rows, common.DataRow{"id": int64(i), "v": tc.value})
	}

	stmts, err := GenerateSQL(SQLGenConfig{Dialect: dialectName, Table: "esc_target", Mode: "append"}, ds)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ExecuteSQL(uri, stmts); err != nil {
		t.Fatalf("the generated SQL did not execute, so no value arrived at all: %v\n\n%s", err, stmts)
	}

	// Nothing may have executed beyond the insert.
	canaryQuery := `SELECT COUNT(*) FROM information_schema.tables
		 WHERE table_schema = DATABASE() AND table_name = 'esc_canary'`
	if dialectName == "postgres" {
		canaryQuery = `SELECT COUNT(*) FROM information_schema.tables
		 WHERE table_schema = current_schema() AND table_name = 'esc_canary'`
	}
	var canary int
	if err := db.QueryRow(canaryQuery).Scan(&canary); err != nil {
		t.Fatal(err)
	}
	if canary == 0 {
		t.Fatal("SQL INJECTION: a source value dropped a table it was never meant to touch")
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			q := `SELECT v FROM esc_target WHERE id = ?`
			if dialectName == "postgres" {
				q = `SELECT v FROM esc_target WHERE id = $1`
			}
			if err := db.QueryRow(q, i).Scan(&got); err != nil {
				t.Fatalf("no row arrived: %v", err)
			}
			if got != tc.value {
				t.Errorf("value changed in transit:\n got %q\nwant %q", got, tc.value)
			}
		})
	}
}

// The generated text is worth pinning on its own, without a server, so a
// regression is caught by `go test ./...` on a machine with no MySQL.
func TestMySQLQuoteStringEscapesBackslash(t *testing.T) {
	d := getDialect("mysql")
	for _, tc := range []struct{ in, want string }{
		{`a\b`, `'a\\b'`},
		{`x\`, `'x\\'`},
		{`it's`, `'it''s'`},
		{`C:\Users`, `'C:\\Users'`},
	} {
		if got := d.quoteString(tc.in); got != tc.want {
			t.Errorf("quoteString(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}

	// Postgres must keep doubling and must NOT escape backslash:
	// standard_conforming_strings makes a backslash literal there, so
	// escaping it would corrupt the value in the other direction.
	pg := getDialect("postgres")
	if got, want := pg.quoteString(`a\b`), `'a\b'`; got != want {
		t.Errorf("postgres quoteString(%q) = %s, want %s — Postgres takes backslash literally", `a\b`, got, want)
	}
	if got, want := pg.quoteString(`it's`), `'it''s'`; got != want {
		t.Errorf("postgres quoteString(%q) = %s, want %s", `it's`, got, want)
	}
}

// A MySQL password containing characters that mean something in a URI could
// not authenticate. Connection.BuildURI assembles the DSN with net/url, which
// percent-encodes the userinfo, but go-sql-driver reads the password
// verbatim — it unescapes only the database name and the parameters
// (dsn.go). So `p@ss` was handed to MySQL as `p%40ss`.
//
// The password used here is deliberately awkward in every way a URI cares
// about: it contains the userinfo separator, the host separator, a path
// separator, a fragment marker, a percent sign, and a space.
func TestMySQLPasswordWithURIMetacharactersAuthenticates(t *testing.T) {
	host := os.Getenv("BROKOLI_TEST_MYSQL_HOST")
	if host == "" {
		t.Skip("BROKOLI_TEST_MYSQL_HOST not set")
	}
	port := 3306
	if p := os.Getenv("BROKOLI_TEST_MYSQL_PORT"); p != "" {
		fmt.Sscanf(p, "%d", &port)
	}
	user := os.Getenv("BROKOLI_TEST_MYSQL_USER")
	pass := os.Getenv("BROKOLI_TEST_MYSQL_PASSWORD")
	dbname := os.Getenv("BROKOLI_TEST_MYSQL_DB")
	if user == "" || pass == "" || dbname == "" {
		t.Skip("BROKOLI_TEST_MYSQL_USER/PASSWORD/DB not set")
	}

	c := &models.Connection{
		Type: models.ConnTypeMySQL, Host: host, Port: port,
		Schema: dbname, Login: user, Password: pass,
	}
	uri := c.BuildURI()
	t.Logf("BuildURI() = %s", strings.Replace(uri, pass, "<password>", 1))

	driver, dsn, err := DetectDriver(uri)
	if err != nil {
		t.Fatalf("DetectDriver: %v", err)
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var one int
	if err := db.QueryRow("SELECT 1").Scan(&one); err != nil {
		t.Fatalf("a connection record with an ordinary-but-awkward password could not "+
			"authenticate, which means the DSN does not carry the password the operator "+
			"typed: %v", err)
	}
}
