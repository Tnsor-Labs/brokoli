package engine

import (
	"strings"
	"testing"
)

func refs() map[string]sqlColumnRef {
	return map[string]sqlColumnRef{
		"amount": {Ident: `"amount"`, Kind: kindNumeric},
		"city":   {Ident: `"city"`, Kind: kindText},
		"flag":   {Ident: `"flag"`, Kind: kindUnclassified},
	}
}

// The emitted SQL is the specification, so it is worth reading. Each case
// records a decision made to match the Go matcher rather than to be idiomatic
// SQL, and the comment says which.
func TestCompileFilterToSQLShape(t *testing.T) {
	for _, tc := range []struct {
		name, cond, want string
	}{
		{
			// Double precision, not numeric: Go compares through
			// strconv.ParseFloat, so the cast has to lose the same precision.
			name: "numeric ordered casts to double precision",
			cond: "amount > 99",
			want: `CASE WHEN "amount" IS NULL THEN TRUE ELSE (CAST("amount" AS DOUBLE PRECISION) > 99) END`,
		},
		{
			// "<nil>" sorts above "99" byte-wise, so Go keeps NULL rows here
			// and drops them for "<". The branch is a constant either way.
			name: "the NULL branch flips with the operator",
			cond: "amount < 99",
			want: `CASE WHEN "amount" IS NULL THEN FALSE ELSE (CAST("amount" AS DOUBLE PRECISION) < 99) END`,
		},
		{
			// C collation, because Go compares bytes. The NULL branch is
			// FALSE here and TRUE in the numeric case above for the same
			// operator: Go compares the string "<nil>" against the target, and
			// '<' (0x3C) sorts below 'F' (0x46) but above '9' (0x39). Nothing
			// about that is intuitive, which is exactly why it is computed
			// from the target rather than reasoned about.
			name: "text ordered uses the C collation",
			cond: "city > Faro",
			want: `CASE WHEN "city" IS NULL THEN FALSE ELSE ("city" COLLATE "C" > 'Faro') END`,
		},
		{
			// Equality on a numeric column compares the rendered text, since
			// that is what Go compares -- so '100.0' does not match 100.
			name: "numeric equality compares text",
			cond: "amount = 100",
			want: `CASE WHEN "amount" IS NULL THEN FALSE ELSE (CAST("amount" AS TEXT) = '100') END`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := compileFilterToSQL(tc.cond, refs())
			if !ok {
				t.Fatalf("refused %q", tc.cond)
			}
			if got != tc.want {
				t.Errorf("\n got %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestCompileFilterToSQLRefusals(t *testing.T) {
	for _, tc := range []struct{ name, cond, why string }{
		{"unknown column", "nosuch > 1", "a column the compiler was told nothing about"},
		{"unclassified type", "flag = true", "boolean rendering has not been shown to agree"},
		{"text column, numeric target", "city > 9", "Go decides numeric-or-byte per row"},
		{"set membership", "city in [a, b]", "not demonstrated yet"},
		{"unparseable", "city", "not a condition at all"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, ok := compileFilterToSQL(tc.cond, refs()); ok {
				t.Errorf("compiled %q (%s): %s", tc.cond, tc.why, got)
			}
		})
	}
}

// A filter target is attacker-influenced in the sense that it comes from a
// pipeline definition, so it must not be able to close the literal.
func TestCompileFilterToSQLEscapesTarget(t *testing.T) {
	got, ok := compileFilterToSQL("city = O'Brien' OR 1=1 --", refs())
	if !ok {
		t.Skip("refused, which is also safe")
	}
	if strings.Contains(got, "OR 1=1 --'") && !strings.Contains(got, "''") {
		t.Errorf("target not escaped: %s", got)
	}
	if !strings.Contains(got, "''") {
		t.Errorf("expected a doubled quote in the literal: %s", got)
	}
}

func TestClassifyDatabaseType(t *testing.T) {
	for name, want := range map[string]sqlColumnKind{
		"TEXT": kindText, "varchar": kindText, "BPCHAR": kindText,
		"INT8": kindNumeric, "numeric": kindNumeric, "FLOAT8": kindNumeric,
		"BOOL": kindUnclassified, "DATE": kindUnclassified, "JSONB": kindUnclassified,
		"": kindUnclassified, "SOMETHING_NEW": kindUnclassified,
	} {
		if got := classifyDatabaseType(name); got != want {
			t.Errorf("classifyDatabaseType(%q) = %v, want %v", name, got, want)
		}
	}
}
