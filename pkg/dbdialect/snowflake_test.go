package dbdialect

import "testing"

// A backslash escapes inside a Snowflake string literal, so quote doubling
// alone lets a value ending in one swallow the closing quote. Both the
// compiler's QuoteLiteral and the statement writer's syntax must say so.
func TestSnowflakeQuoteLiteralEscapesBackslash(t *testing.T) {
	d, ok := For("snowflake")
	if !ok {
		t.Fatal("snowflake is not registered")
	}
	for in, want := range map[string]string{
		`plain`:                 `'plain'`,
		`O'Brien`:               `'O''Brien'`,
		`x\`:                    `'x\\'`,
		`x\'; DROP TABLE t; --`: `'x\\''; DROP TABLE t; --'`,
		`C:\temp\new`:           `'C:\\temp\\new'`,
	} {
		if got := d.QuoteLiteral(in); got != want {
			t.Errorf("QuoteLiteral(%q) = %s, want %s", in, got, want)
		}
	}
	if !d.(StatementWriter).WriteSyntax().BackslashEscapes {
		t.Error("snowflake WriteSyntax does not escape backslashes")
	}
}
