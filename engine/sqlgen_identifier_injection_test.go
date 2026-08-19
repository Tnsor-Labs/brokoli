package engine

import (
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// TestGenerateSQL_RejectsColumnNameBreakout pins the fix for the
// second-order SQL injection ADR-022 documents: quoteIdent wraps an
// identifier in the dialect's quote character but never escaped an
// embedded occurrence of it, and ds.Columns for a CSV/Excel source is
// the file's own header row -- not something a pipeline builder
// necessarily controls or reviews. A crafted header used to break out
// of the quoted identifier into arbitrary SQL.
func TestGenerateSQL_RejectsColumnNameBreakout(t *testing.T) {
	// The exact shape of attack ADR-022 describes: a column header that
	// closes the quoted identifier and appends a second statement.
	maliciousColumn := `id") VALUES ('x'); DROP TABLE users; --`
	ds := &common.DataSet{
		Columns: []string{"safe_col", maliciousColumn},
		Rows:    []common.DataRow{{"safe_col": 1, maliciousColumn: "x"}},
	}
	sql, err := GenerateSQL(SQLGenConfig{Table: "orders"}, ds)
	if err == nil {
		t.Fatalf("expected GenerateSQL to reject a column name containing a quote character, got SQL:\n%s", sql)
	}
	if !strings.Contains(err.Error(), "invalid identifier") {
		t.Errorf("error = %q, want it to explain the identifier was rejected", err.Error())
	}
	// The critical property: no SQL text was generated at all -- a
	// caller that only checks `err != nil` before logging/discarding
	// still never had injectable text to execute in the first place.
	if sql != "" {
		t.Errorf("expected no SQL text on rejection, got:\n%s", sql)
	}
}

// TestGenerateSQL_RejectsMaliciousTableName covers the sibling case: the
// sink_db node's own `table` config, not just CSV-header-derived column
// names.
func TestGenerateSQL_RejectsMaliciousTableName(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"id"},
		Rows:    []common.DataRow{{"id": 1}},
	}
	_, err := GenerateSQL(SQLGenConfig{Table: `data"; DROP TABLE users; --`}, ds)
	if err == nil {
		t.Fatal("expected GenerateSQL to reject a table name containing a quote character")
	}
}

// TestGenerateSQL_RejectsMaliciousKeyColumn covers upsert's KeyColumns,
// the third place an identifier reaches quoteIdent.
func TestGenerateSQL_RejectsMaliciousKeyColumn(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"id", "name"},
		Rows:    []common.DataRow{{"id": 1, "name": "x"}},
	}
	_, err := GenerateSQL(SQLGenConfig{
		Table:      "t",
		Dialect:    "postgres",
		Mode:       ModeUpsert,
		KeyColumns: []string{`id") ON CONFLICT DO NOTHING; DROP TABLE users; --`},
	}, ds)
	if err == nil {
		t.Fatal("expected GenerateSQL to reject a key column containing a quote character")
	}
}

// TestGenerateSQL_OrdinaryPunctuationStillAllowed confirms the fix
// doesn't overcorrect into rejecting legitimate CSV headers -- spaces,
// hyphens, and similar are common in real-world column names and don't
// let anything break out of quoteIdent's wrapping.
func TestGenerateSQL_OrdinaryPunctuationStillAllowed(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"Order Date", "unit-price", "qty (kg)"},
		Rows:    []common.DataRow{{"Order Date": "2026-01-01", "unit-price": 9.5, "qty (kg)": 2}},
	}
	sql, err := GenerateSQL(SQLGenConfig{Table: "orders"}, ds)
	if err != nil {
		t.Fatalf("expected ordinary punctuation in column names to be allowed, got: %v", err)
	}
	if !strings.Contains(sql, `"Order Date"`) || !strings.Contains(sql, `"unit-price"`) {
		t.Errorf("expected legitimate column names to be quoted and present, got:\n%s", sql)
	}
}

func TestValidateIdentifier(t *testing.T) {
	cases := []struct {
		id      string
		wantErr bool
	}{
		{"orders", false},
		{"Order Date", false},
		{"unit-price", false},
		{"", true},
		{`foo"bar`, true},
		{"foo`bar", true},
		{"foo[bar", true},
		{"foo]bar", true},
		{"foo;bar", true},
		{"foo\x00bar", true},
	}
	for _, tc := range cases {
		err := validateIdentifier(tc.id)
		if tc.wantErr && err == nil {
			t.Errorf("validateIdentifier(%q): expected an error, got nil", tc.id)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("validateIdentifier(%q): expected no error, got %v", tc.id, err)
		}
	}
}
