package engine

import (
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
		{"snowflake://user:pass@account/db/schema?warehouse=WH", "snowflake", "user:pass@account/db/schema?warehouse=WH"},
		{"mysql://user:pass@host:3306/db", "mysql", "user:pass@host:3306/db"},
		{"sqlite://test.db", "sqlite", "test.db"},
		{"test.db", "sqlite", "test.db"},
		{"sqlserver://user:pass@host:1433?database=db", "sqlserver", "sqlserver://user:pass@host:1433?database=db"},
		{"mssql://user:pass@host:1433?database=db", "sqlserver", "mssql://user:pass@host:1433?database=db"},
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

	for _, uri := range []string{
		"snowflake://u:p@acct/db",
		"sqlserver://u:p@h:1433?database=d",
		"mssql://u:p@h:1433?database=d",
	} {
		_, _, err := DetectDriver(uri)
		if err == nil {
			t.Errorf("%s: expected an unsupported-type error, got none", uri)
			continue
		}
		if !strings.Contains(err.Error(), "not supported by this build") {
			t.Errorf("%s: error does not name the limitation: %v", uri, err)
		}
		if strings.Contains(err.Error(), ":p@") {
			t.Errorf("%s: error leaks the password: %v", uri, err)
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
		"oracle://u:p@h:1521/svc",
		"bigquery://project/dataset",
		"databricks://token@workspace/warehouse",
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
