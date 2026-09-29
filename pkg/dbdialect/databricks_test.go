package dbdialect

import "testing"

// Spark SQL reads a double-quoted name as a string and lets a backslash
// escape inside a literal, so the generic dialect's quoting would be wrong
// both ways there.
func TestDatabricksQuoting(t *testing.T) {
	d, ok := For("databricks")
	if !ok {
		t.Fatal("databricks is not registered")
	}
	for in, want := range map[string]string{
		"orders":   "`orders`",
		"we`ird":   "`we``ird`",
		`"quoted"`: "`\"quoted\"`",
	} {
		if got := d.QuoteIdent(in); got != want {
			t.Errorf("QuoteIdent(%q) = %s, want %s", in, got, want)
		}
	}
	if got := d.QuoteQualifiedIdent("main.raw"); got != "`main`.`raw`" {
		t.Errorf("QuoteQualifiedIdent = %s", got)
	}
	for in, want := range map[string]string{
		"plain":  `'plain'`,
		"it's":   `'it\'s'`,
		`a\`:     `'a\\'`,
		`a\' OR`: `'a\\\' OR'`,
	} {
		if got := d.QuoteLiteral(in); got != want {
			t.Errorf("QuoteLiteral(%q) = %s, want %s", in, got, want)
		}
	}
	if _, ok := d.(Addresser); ok {
		t.Error("databricks must not claim an Addresser: nothing may push down into it")
	}
	ws := d.(StatementWriter).WriteSyntax()
	if ws.QuoteChar != "`" || !ws.BackslashEscapes {
		t.Errorf("WriteSyntax = %+v, want Spark quoting", ws)
	}
}
