package dbdialect

import "testing"

// The claims are the fold of engine/database.go's old switch (#361); this
// pins their exact coverage so a dropped or renamed claim fails here, by
// name, instead of silently changing which driver opens a URI.
func TestAllURIClaimsCoverage(t *testing.T) {
	want := map[string]struct{ driver, dialect string }{
		"postgres":   {"pgx", "postgres"},
		"postgresql": {"pgx", "postgres"},
		"redshift":   {"pgx", "postgres"},
		"mysql":      {"mysql", "mysql"},
		"sqlite":     {"sqlite", "sqlite"},
		"clickhouse": {"clickhouse", "clickhouse"},
		"sqlserver":  {"sqlserver", "sqlserver"},
		"mssql":      {"sqlserver", "sqlserver"},
		"snowflake":  {"brokoli-snowflake", "snowflake"},
		"oracle":     {"oracle", "oracle"},
		"databricks": {"brokoli-databricks", "databricks"},
	}
	got := AllURIClaims()
	if len(got) != len(want)+1 {
		t.Fatalf("%d claims, want %d: %+v", len(got), len(want)+1, got)
	}
	for _, c := range got {
		if c.Scheme == "bigquery" {
			if !c.Native || c.Driver != "" || c.Dialect != "" {
				t.Errorf("bigquery: %#v, want native claim without SQL driver or dialect", c)
			}
			continue
		}
		w, ok := want[c.Scheme]
		if !ok {
			t.Errorf("unexpected claim for scheme %q", c.Scheme)
			continue
		}
		if c.Driver != w.driver || c.Dialect != w.dialect {
			t.Errorf("%s: (%s, %s), want (%s, %s)", c.Scheme, c.Driver, c.Dialect, w.driver, w.dialect)
		}
	}
	// Deterministic order: sorted by scheme.
	for i := 1; i < len(got); i++ {
		if got[i-1].Scheme > got[i].Scheme {
			t.Fatalf("claims not sorted: %q before %q", got[i-1].Scheme, got[i].Scheme)
		}
	}

	// The DSN transformations, exact: redshift rewrites the scheme,
	// mysql and sqlite strip theirs (sqlite adding a busy timeout), the rest
	// pass the URI through.
	byScheme := map[string]URIClaim{}
	for _, c := range got {
		byScheme[c.Scheme] = c
	}
	if dsn := byScheme["redshift"].DSN("redshift://u:p@h/db"); dsn != "postgres://u:p@h/db" {
		t.Errorf("redshift DSN = %q", dsn)
	}
	if dsn := byScheme["mysql"].DSN("mysql://u:p@tcp(h)/db"); dsn != "u:p@tcp(h)/db" {
		t.Errorf("mysql DSN = %q", dsn)
	}
	if dsn := byScheme["sqlite"].DSN("sqlite:///tmp/x.db"); dsn != "/tmp/x.db?_pragma=busy_timeout(5000)" {
		t.Errorf("sqlite DSN = %q", dsn)
	}
	for _, passthrough := range []string{"postgres", "postgresql", "clickhouse", "sqlserver", "mssql", "oracle", "databricks"} {
		if byScheme[passthrough].DSN != nil {
			t.Errorf("%s should pass the URI through (nil DSN)", passthrough)
		}
	}
	if dsn := byScheme["snowflake"].DSN("snowflake://u:p@account/db/schema?warehouse=WH"); dsn != "u:p@account/db/schema?warehouse=WH" {
		t.Errorf("snowflake DSN = %q", dsn)
	}
}

// A SQLite DSN gets a busy timeout unless it already sets one (#834); the
// rest of the DSN is left as the user wrote it.
func TestSQLiteDSN(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"/tmp/x.db", "/tmp/x.db?_pragma=busy_timeout(5000)"},
		{"file:/tmp/x.db?mode=ro", "file:/tmp/x.db?mode=ro&_pragma=busy_timeout(5000)"},
		{"/tmp/x.db?", "/tmp/x.db?_pragma=busy_timeout(5000)"},
		{"/tmp/x.db?mode=ro&", "/tmp/x.db?mode=ro&_pragma=busy_timeout(5000)"},
		{":memory:", ":memory:?_pragma=busy_timeout(5000)"},
		// The user's own timeout wins, however it is spelled.
		{"/tmp/x.db?_pragma=busy_timeout(100)", "/tmp/x.db?_pragma=busy_timeout(100)"},
		{"/tmp/x.db?_pragma=%20BUSY_TIMEOUT%3D100", "/tmp/x.db?_pragma=%20BUSY_TIMEOUT%3D100"},
		{"/tmp/x.db?_pragma=foreign_keys(1)&_pragma=busy_timeout(0)", "/tmp/x.db?_pragma=foreign_keys(1)&_pragma=busy_timeout(0)"},
		// Other pragmas are kept and the timeout added beside them.
		{"/tmp/x.db?_pragma=foreign_keys(1)", "/tmp/x.db?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"},
		// A query the driver cannot parse is not touched: it fails there
		// exactly as it did.
		{"/tmp/x.db?a=%zz", "/tmp/x.db?a=%zz"},
	} {
		if got := SQLiteDSN(tc.in); got != tc.want {
			t.Errorf("SQLiteDSN(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBigQueryIsNativeURI(t *testing.T) {
	claim, ok := NativeURI("bigquery://project/dataset")
	if !ok {
		t.Fatal("bigquery URI is not claimed")
	}
	if claim.Driver != "" || !claim.Native {
		t.Fatalf("claim = %#v, want native claim with no SQL driver", claim)
	}
}
