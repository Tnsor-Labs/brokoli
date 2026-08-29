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
	}
	got := AllURIClaims()
	if len(got) != len(want) {
		t.Fatalf("%d claims, want %d: %+v", len(got), len(want), got)
	}
	for _, c := range got {
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
	// mysql and sqlite strip theirs, the rest pass the URI through.
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
	if dsn := byScheme["sqlite"].DSN("sqlite:///tmp/x.db"); dsn != "/tmp/x.db" {
		t.Errorf("sqlite DSN = %q", dsn)
	}
	for _, passthrough := range []string{"postgres", "postgresql", "clickhouse", "sqlserver", "mssql"} {
		if byScheme[passthrough].DSN != nil {
			t.Errorf("%s should pass the URI through (nil DSN)", passthrough)
		}
	}
}
