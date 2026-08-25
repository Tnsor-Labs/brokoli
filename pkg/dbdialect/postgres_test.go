package dbdialect

import "testing"

func pg(t *testing.T) Dialect {
	t.Helper()
	d, ok := For("postgres")
	if !ok {
		t.Fatal("the postgres dialect should always be registered")
	}
	return d
}

// The compiler decides whether a rule can be pushed down from this answer, so
// an unrecognised name must be unclassified rather than guessed: a wrong
// guess produces rows that differ depending on which backend ran.
func TestPostgresClassifyType(t *testing.T) {
	for name, want := range map[string]ColumnKind{
		"TEXT": KindText, "varchar": KindText, "BPCHAR": KindText,
		"INT8": KindNumeric, "numeric": KindNumeric, "FLOAT8": KindNumeric,
		"BOOL": KindUnclassified, "DATE": KindUnclassified, "JSONB": KindUnclassified,
		"": KindUnclassified, "SOMETHING_NEW": KindUnclassified,
		// citext compares case-insensitively under a non-deterministic
		// collation, so SQL would match 'ALICE' against 'alice' where the Go
		// matcher, comparing bytes, would not.
		"CITEXT": KindUnclassified,
	} {
		if got := pg(t).ClassifyType(name); got != want {
			t.Errorf("ClassifyType(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestPostgresQuoting(t *testing.T) {
	d := pg(t)
	for in, want := range map[string]string{
		"orders":             `"orders"`,
		`we"ird`:             `"we""ird"`,
		`x"; DROP TABLE y--`: `"x""; DROP TABLE y--"`,
	} {
		if got := d.QuoteIdent(in); got != want {
			t.Errorf("QuoteIdent(%q) = %s, want %s", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"orders":        `"orders"`,
		"public.orders": `"public"."orders"`,
		`we"ird`:        `"we""ird"`,
		// More qualification than a schema and a table: quoted whole so it
		// fails loudly rather than being silently reinterpreted.
		"a.b.c": `"a.b.c"`,
	} {
		if got := d.QuoteQualifiedIdent(in); got != want {
			t.Errorf("QuoteQualifiedIdent(%q) = %s, want %s", in, got, want)
		}
	}
}

// Postgres doubles the quote and leaves a backslash alone. That second half is
// the load-bearing one: standard_conforming_strings makes a backslash an
// ordinary character here, so escaping it — as MySQL requires — would turn one
// backslash into two. This is what stops the MySQL escaping fix being applied
// everywhere "to be safe".
func TestPostgresQuoteLiteralDoesNotEscapeBackslash(t *testing.T) {
	d := pg(t)
	for in, want := range map[string]string{
		"plain":     `'plain'`,
		"it's":      `'it''s'`,
		`a\b`:       `'a\b'`,
		`C:\Users`:  `'C:\Users'`,
		`x\`:        `'x\'`,
		`'; DROP--`: `'''; DROP--'`,
	} {
		if got := d.QuoteLiteral(in); got != want {
			t.Errorf("QuoteLiteral(%q) = %s, want %s", in, got, want)
		}
	}
}

// The emitted casts and collation are the specification for how a value is
// compared, so they are worth reading rather than inferring from behaviour.
func TestPostgresComparisonForms(t *testing.T) {
	d := pg(t)
	// Double precision, not numeric: Go compares through strconv.ParseFloat,
	// so the cast has to lose the same precision. A more accurate cast would
	// disagree with the engine.
	if got, want := d.CastToFloat(`"amount"`), `CAST("amount" AS DOUBLE PRECISION)`; got != want {
		t.Errorf("CastToFloat = %s, want %s", got, want)
	}
	if got, want := d.CastToText(`"amount"`), `CAST("amount" AS TEXT)`; got != want {
		t.Errorf("CastToText = %s, want %s", got, want)
	}
	// C collation, because Go compares bytes.
	if got, want := d.ByteOrderedText(`"city"`), `"city" COLLATE "C"`; got != want {
		t.Errorf("ByteOrderedText = %s, want %s", got, want)
	}
	if got, want := d.ProbeColumnsSQL("SELECT 1"), "SELECT * FROM (SELECT 1) AS brokoli_probe LIMIT 0"; got != want {
		t.Errorf("ProbeColumnsSQL = %s, want %s", got, want)
	}
}

// Postgres implements the optional same-server capability; a backend that does
// not simply never pushes down.
func TestPostgresIsAnAddresser(t *testing.T) {
	if _, ok := pg(t).(Addresser); !ok {
		t.Fatal("postgres should implement Addresser, or pushdown cannot engage")
	}
}

func TestForUnknownDialect(t *testing.T) {
	if _, ok := For("nosuchdialect"); ok {
		t.Error("an unknown dialect must not resolve")
	}
	if _, ok := For(""); ok {
		t.Error("an empty name must not resolve")
	}
}
