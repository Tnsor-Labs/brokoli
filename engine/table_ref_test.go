package engine

import "testing"

// sameServer decides whether one node's query may be read directly by another
// node's write. Getting it wrong does not make a run slow, it runs one
// connection's query inside another connection's transaction — so the tests
// that matter here are the ones asserting it says no.
func TestSameServer(t *testing.T) {
	const base = "postgres://u:p@db.internal:5432/analytics"

	t.Run("identical is the same server", func(t *testing.T) {
		if !sameServer(base, base) {
			t.Error("a URI is not the same server as itself")
		}
	})

	t.Run("the default port is the same as writing it out", func(t *testing.T) {
		if !sameServer("postgres://u:p@db.internal/analytics", base) {
			t.Error("an omitted port should default to 5432 on both sides")
		}
	})

	t.Run("postgresql:// and postgres:// are the same scheme", func(t *testing.T) {
		if !sameServer("postgresql://u:p@db.internal:5432/analytics", base) {
			t.Error("the two spellings of the Postgres scheme should agree")
		}
	})

	for _, tc := range []struct{ name, other, why string }{
		{"different host", "postgres://u:p@other.internal:5432/analytics",
			"a different machine entirely"},
		{"different port", "postgres://u:p@db.internal:5433/analytics",
			"a different server on the same machine"},
		{"different database", "postgres://u:p@db.internal:5432/staging",
			"the same server, but rows would land in the wrong database"},
		{"different user", "postgres://other:p@db.internal:5432/analytics",
			"a different role is a different set of permissions; collapsing them " +
				"would run one role's query inside the other's write"},
		{"different password", "postgres://u:other@db.internal:5432/analytics",
			"same reason: the credential is part of the identity"},
		{"mysql", "mysql://u:p@tcp(db.internal:3306)/analytics",
			"only Postgres has an equivalence argument so far"},
		{"sqlite path", "/data/app.db", "not a server"},
		{"empty", "", "nothing to compare"},
		{"unknown query parameter", "postgres://u:p@db.internal:5432/analytics?target_session_attrs=read-only",
			"a parameter that can change which server is actually reached"},
	} {
		t.Run(tc.name+" is refused", func(t *testing.T) {
			if sameServer(base, tc.other) {
				t.Errorf("treated as the same server (%s): %s", tc.why, tc.other)
			}
			if sameServer(tc.other, base) {
				t.Errorf("asymmetric: reversed comparison said same server (%s)", tc.other)
			}
		})
	}

	t.Run("harmless parameters do not block it", func(t *testing.T) {
		if !sameServer(base+"?sslmode=require", base+"?sslmode=require") {
			t.Error("sslmode does not change which database is reached")
		}
	})
}

func TestQuoteQualifiedIdentPG(t *testing.T) {
	for in, want := range map[string]string{
		"orders":             `"orders"`,
		"public.orders":      `"public"."orders"`,
		`we"ird`:             `"we""ird"`,
		`a.b.c`:              `"a.b.c"`,
		`x"; DROP TABLE y--`: `"x""; DROP TABLE y--"`,
	} {
		if got := quoteQualifiedIdentPG(in); got != want {
			t.Errorf("quoteQualifiedIdentPG(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestDataPlaneEscapeHatch(t *testing.T) {
	t.Setenv("BROKOLI_DATA_PLANE", "")
	if dataPlaneInterpreted() {
		t.Error("unset should leave the engine free to push down")
	}
	for _, v := range []string{"interpreted", "INTERPRETED", " interpreted "} {
		t.Setenv("BROKOLI_DATA_PLANE", v)
		if !dataPlaneInterpreted() {
			t.Errorf("%q should force the interpreted path", v)
		}
	}
	t.Setenv("BROKOLI_DATA_PLANE", "something-else")
	if dataPlaneInterpreted() {
		t.Error("an unrecognised value should not silently change the data plane")
	}
}
