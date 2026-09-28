package engine

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// Scheme-to-driver mapping, independent of whether the driver is compiled in.
// DetectDriver adds that second check on top; this covers the mapping itself,
// including the schemes only the unsupported-type error path reaches.
func TestDetectDriverSchemeMapping(t *testing.T) {
	tests := []struct {
		uri        string
		wantDriver string
		wantDSN    string
	}{
		{"postgres://user:pass@host:5432/db", "pgx", "postgres://user:pass@host:5432/db"},
		{"postgresql://user:pass@host/db", "pgx", "postgresql://user:pass@host/db"},
		{"redshift://user:pass@cluster.us-east-1.redshift.amazonaws.com:5439/db", "pgx", "postgres://user:pass@cluster.us-east-1.redshift.amazonaws.com:5439/db"},
		{"snowflake://user:pass@account/db/schema?warehouse=WH", "brokoli-snowflake", "user:pass@account/db/schema?warehouse=WH"},
		{"databricks://token:p%40ss@workspace:443/sql/1.0/warehouses/wh", "databricks", "token:p%40ss@workspace:443/sql/1.0/warehouses/wh"},
		{"mysql://user:pass@host:3306/db", "mysql", "user:pass@host:3306/db"},
		{"sqlite://test.db", "sqlite", "test.db"},
		{"test.db", "sqlite", "test.db"},
		{"sqlserver://user:pass@host:1433?database=db", "sqlserver", "sqlserver://user:pass@host:1433?database=db"},
		{"mssql://user:pass@host:1433?database=db", "sqlserver", "mssql://user:pass@host:1433?database=db"},
		// go-ora needs the scheme: stripped, it cannot find the port.
		{"oracle://user:pass@host:1521/service", "oracle", "oracle://user:pass@host:1521/service"},
		// Default falls through to pgx
		{"host:5432/db", "pgx", "host:5432/db"},
	}

	for _, tt := range tests {
		t.Run(tt.uri, func(t *testing.T) {
			driver, dsn, err := detectDriver(tt.uri)
			if err != nil {
				t.Fatalf("detectDriver(%q) error: %v", tt.uri, err)
			}
			if driver != tt.wantDriver {
				t.Errorf("driver = %q, want %q", driver, tt.wantDriver)
			}
			if dsn != tt.wantDSN {
				t.Errorf("dsn = %q, want %q", dsn, tt.wantDSN)
			}
		})
	}
}

// The Oracle driver is registered by engine/database.go's import, not by
// a test file: no test in this package imports go-ora, so removing that
// import fails here rather than passing on the tests' own registration.
func TestOracleDriverIsCompiledIn(t *testing.T) {
	for _, name := range sql.Drivers() {
		if name == "oracle" {
			return
		}
	}
	t.Fatalf("no database/sql driver named oracle is registered: %v", sql.Drivers())
}

// go-ora quotes the whole DSN when it cannot parse it. The run log must get
// the reason without the password, in either its encoded or decoded form.
func TestOracleConnectErrorsDoNotCarryThePassword(t *testing.T) {
	for _, uri := range []string{
		"oracle://svc:s3cr%40t-pw@bad host:1521/ORCL",
		"oracle://svc:s3cr%40t-pw@127.0.0.1:1521:443/ORCL",
	} {
		_, err := QueryDatabase(uri, "SELECT 1 FROM dual")
		if err == nil {
			t.Fatalf("%s: expected a connection error", uri)
		}
		msg := err.Error()
		if strings.Contains(msg, "s3cr%40t-pw") || strings.Contains(msg, "s3cr@t-pw") {
			t.Fatalf("the error carries the password: %s", msg)
		}
	}
}

func TestRedactDSNError(t *testing.T) {
	cases := []struct {
		dsn, err, want string
	}{
		{"oracle://u:p%40ss@bad host:1521/x", `parse "oracle://u:p%40ss@bad host:1521/x": invalid character`, `parse "oracle://u:xxxxx@bad host:1521/x": invalid character`},
		{"oracle://u:p%40ss@h/x", "login as u with p@ss failed", "login as u with xxxxx failed"},
		// No password, nothing to remove.
		{"oracle://u@h/x", "boom u@h", "boom u@h"},
		{"postgres://h/x", "boom", "boom"},
	}
	for _, c := range cases {
		if got := RedactDSNError(errors.New(c.err), c.dsn).Error(); got != c.want {
			t.Errorf("RedactDSNError(%q, %q) = %q, want %q", c.err, c.dsn, got, c.want)
		}
	}
	if RedactDSNError(nil, "oracle://u:p@h/x") != nil {
		t.Error("a nil error must stay nil")
	}
}

// go-ora returns every NUMBER as text. The decoded value keeps every digit:
// int64 for whole numbers that fit, float64 only where the text round-trips,
// text otherwise.
func TestDecodeOracleNumber(t *testing.T) {
	cases := []struct {
		in   interface{}
		want interface{}
	}{
		{"1", int64(1)},
		{"-7", int64(-7)},
		{"9223372036854775807", int64(9223372036854775807)},
		{"-9223372036854775808", int64(-9223372036854775808)},
		// Past int64: 20 digits cannot be a float64 without rounding.
		{"9223372036854775808", "9223372036854775808"},
		{"12345678901234567890123", "12345678901234567890123"},
		{"1.5", 1.5},
		{"-0.25", -0.25},
		{"0.1", 0.1},
		{"123456789012.345", 123456789012.345},
		// 16 significant digits: not every one survives float64.
		{"1234567890123.4567", "1234567890123.4567"},
		{"0.0000000000000000000000000001", 1e-28},
		{nil, nil},
		{float64(2.5), float64(2.5)},
	}
	for _, c := range cases {
		if got := decodeOracleNumber(c.in); got != c.want {
			t.Errorf("decodeOracleNumber(%#v) = %#v (%T), want %#v (%T)", c.in, got, got, c.want, c.want)
		}
	}
}

// Oracle has earned reads only: every write mode is refused by name, before
// any statement is generated.
func TestOracleWritesAreRefused(t *testing.T) {
	for _, mode := range []string{"", ModeAppend, ModeOverwrite, "replace", ModeUpsert, "create_table"} {
		err := refuseUnearnedWrite("oracle://u:p@h:1521/svc", mode)
		if err == nil || !strings.Contains(err.Error(), "Oracle connections are read-only") {
			t.Errorf("mode %q: err = %v, want the read-only refusal", mode, err)
		}
	}
	// The control: the refusal is Oracle's, not every backend's.
	if err := refuseUnearnedWrite("postgres://u:p@h/db", ModeAppend); err != nil {
		t.Errorf("postgres append refused: %v", err)
	}
}

func TestDatabricksDSNIsAcceptedByDriver(t *testing.T) {
	driver, dsn, err := DetectDriver("databricks://token:p%40ss@workspace.cloud.databricks.com:443/sql/1.0/warehouses/wh")
	if err != nil {
		t.Fatal(err)
	}
	if driver != "databricks" {
		t.Fatalf("driver = %q, want databricks", driver)
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
}

// The connection catalog offers more database types than this build has
// drivers for. That is a product limitation, not a defect, and the error has
// to say so: database/sql's own message for an unregistered driver is
// "unknown driver \"snowflake\" (forgotten import?)", which sends an operator
// looking for a broken build.
func TestDetectDriverRejectsUncompiledDrivers(t *testing.T) {
	supported := []struct{ uri, driver string }{
		{"postgres://u:p@h:5432/d", "pgx"},
		{"postgresql://u:p@h:5432/d", "pgx"},
		{"redshift://u:p@h:5439/d", "pgx"},
		{"mysql://u:p@tcp(h:3306)/d", "mysql"},
		{"sqlite:///data/app.db", "sqlite"},
		{"sqlserver://u:p@h:1433?database=d", "sqlserver"},
		{"mssql://u:p@h:1433?database=d", "sqlserver"},
		{"oracle://u:p@h:1521/svc", "oracle"},
		{"databricks://token:p@h:443/sql/1.0/warehouses/wh", "databricks"},
	}
	for _, tc := range supported {
		got, _, err := DetectDriver(tc.uri)
		if err != nil {
			t.Errorf("%s: %v", tc.uri, err)
			continue
		}
		if got != tc.driver {
			t.Errorf("%s: driver = %q, want %q", tc.uri, got, tc.driver)
		}
	}

}

// #383: an unknown scheme is refused by that name, where it used to fall
// through to pgx and fail with a Postgres error about the wrong backend.
// Schemeless strings keep the pgx default -- libpq keyword DSNs and bare
// host:port/db strings are historically Postgres, and the mapping test
// above pins that.
func TestUnknownSchemesAreRefusedByName(t *testing.T) {
	for _, uri := range []string{
		"gopher://why:not@h/x",
	} {
		t.Run(uri, func(t *testing.T) {
			_, _, err := DetectDriver(uri)
			if err == nil {
				t.Fatal("an unknown scheme must be refused, not routed into pgx")
			}
			scheme := uri[:strings.Index(uri, "://")]
			if !strings.Contains(err.Error(), `"`+scheme+`"`) {
				t.Errorf("the refusal must name the scheme %q, got: %v", scheme, err)
			}
			if !strings.Contains(err.Error(), "supported:") {
				t.Errorf("the refusal should list what this build supports, got: %v", err)
			}
			if strings.Contains(err.Error(), uri) {
				t.Errorf("the error echoes the URI, which carries the password: %v", err)
			}
		})
	}

	// The behaviors the fix must NOT change: keyword DSNs and schemeless
	// host strings still reach pgx, and every named scheme still maps.
	for _, uri := range []string{"host=localhost dbname=x", "host:5432/db"} {
		driver, _, err := detectDriver(uri)
		if err != nil || driver != "pgx" {
			t.Errorf("schemeless %q: driver=%q err=%v, want the pinned pgx default", uri, driver, err)
		}
	}
}

func TestNativeDatabaseSchemesAreRefusedBeforeSQL(t *testing.T) {
	_, _, err := DetectDriver("bigquery://project/dataset")
	if err == nil || err.Error() != "BigQuery does not support database/sql driver detection in this build" {
		t.Fatalf("error = %v, want explicit native-backend refusal", err)
	}
}
