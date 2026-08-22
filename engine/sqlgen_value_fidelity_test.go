package engine

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

func oneValue(v interface{}) *common.DataSet {
	return &common.DataSet{Columns: []string{"v"}, Rows: []common.DataRow{{"v": v}}}
}

func genFor(t *testing.T, v interface{}) string {
	t.Helper()
	sql, err := GenerateSQL(SQLGenConfig{Dialect: "postgres", Table: "t"}, oneValue(v))
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	return sql
}

// A string that looks like a number is still a string. Emitting it bare
// stored a zip code "00123" as 123 and a card number as a numeric.
func TestStringsThatLookNumericStayStrings(t *testing.T) {
	for _, s := range []string{"00123", "123", "1.50", "4111111111111111", "+1", "0x10", "1e5"} {
		sql := genFor(t, s)
		if !strings.Contains(sql, "'"+s+"'") {
			t.Errorf("%q was not written as a quoted string:\n%s", s, sql)
		}
	}
}

// Likewise for text that reads as a boolean: a text column handed a bare
// TRUE is rejected by Postgres outright.
func TestStringsThatLookBooleanStayStrings(t *testing.T) {
	for _, s := range []string{"true", "TRUE", "false", "False"} {
		sql := genFor(t, s)
		if !strings.Contains(sql, "'"+s+"'") {
			t.Errorf("%q was not written as a quoted string:\n%s", s, sql)
		}
	}
}

// An empty string is not unknown.
func TestEmptyStringIsNotNull(t *testing.T) {
	sql := genFor(t, "")
	if strings.Contains(sql, "NULL") {
		t.Errorf("empty string became NULL:\n%s", sql)
	}
	if !strings.Contains(sql, "''") {
		t.Errorf("expected an empty literal:\n%s", sql)
	}
}

// ...unless the writer is told to treat it that way, for file sources
// where the distinction genuinely does not exist.
func TestEmptyStringAsNullOptIn(t *testing.T) {
	sql, err := GenerateSQL(SQLGenConfig{Dialect: "postgres", Table: "t", EmptyStringAsNull: true}, oneValue(""))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, "NULL") {
		t.Errorf("expected NULL with EmptyStringAsNull:\n%s", sql)
	}
}

// Real typed values keep their bare form.
func TestTypedValuesAreNotQuoted(t *testing.T) {
	for _, v := range []interface{}{42, int64(-7), 3.5, float32(1.25), uint(9)} {
		sql := genFor(t, v)
		if strings.Contains(sql, "'") {
			t.Errorf("%v (%T) was quoted:\n%s", v, v, sql)
		}
	}
	if sql := genFor(t, true); !strings.Contains(sql, "TRUE") || strings.Contains(sql, "'") {
		t.Errorf("real bool should be a bare TRUE:\n%s", sql)
	}
}

func TestJSONNumberKeepsItsExactText(t *testing.T) {
	sql := genFor(t, json.Number("00123.4500"))
	if !strings.Contains(sql, "00123.4500") {
		t.Errorf("json.Number lost its text:\n%s", sql)
	}
}

func TestNilIsNull(t *testing.T) {
	if !strings.Contains(genFor(t, nil), "NULL") {
		t.Error("nil should be NULL")
	}
}

// Quote escaping still holds, now via the shared helper.
func TestQuotesInsideStringsAreDoubled(t *testing.T) {
	sql := genFor(t, "O'Brien said 'hi'")
	if !strings.Contains(sql, "'O''Brien said ''hi'''") {
		t.Errorf("quote escaping wrong:\n%s", sql)
	}
}

// Values that are neither typed nor string are rendered and quoted, not
// emitted bare.
func TestUnknownTypesAreQuoted(t *testing.T) {
	type weird struct{ A int }
	sql := genFor(t, weird{A: 1})
	if !strings.Contains(sql, "'") {
		t.Errorf("unknown type should be quoted:\n%s", sql)
	}
}

// MySQL uses the same string quote, so escaping applies there too.
func TestEscapingAcrossDialects(t *testing.T) {
	for _, d := range []string{"mysql", "sqlite", "sqlserver", "generic"} {
		sql, err := GenerateSQL(SQLGenConfig{Dialect: d, Table: "t"}, oneValue("it's"))
		if err != nil {
			t.Fatalf("%s: %v", d, err)
		}
		if !strings.Contains(sql, "'it''s'") {
			t.Errorf("%s did not escape the quote:\n%s", d, sql)
		}
	}
}
