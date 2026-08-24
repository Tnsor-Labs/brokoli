package dbdialect

import (
	"strings"
	"testing"
)

func my(t *testing.T) Dialect {
	t.Helper()
	d, ok := For("mysql")
	if !ok {
		t.Fatal("the mysql dialect should be registered since the pushdown work")
	}
	return d
}

func TestMySQLQuoting(t *testing.T) {
	d := my(t)
	for in, want := range map[string]string{
		"orders": "`orders`",
		"we`ird": "`we``ird`",
		"x`; --": "`x``; --`",
	} {
		if got := d.QuoteIdent(in); got != want {
			t.Errorf("QuoteIdent(%q) = %s, want %s", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"orders":     "`orders`",
		"app.orders": "`app`.`orders`",
		"a.b.c":      "`a.b.c`",
	} {
		if got := d.QuoteQualifiedIdent(in); got != want {
			t.Errorf("QuoteQualifiedIdent(%q) = %s, want %s", in, got, want)
		}
	}
}

// The half that differs from Postgres, and the reason the whole escaping
// arc happened: a backslash escapes inside MySQL literals by default.
func TestMySQLQuoteLiteralEscapesBackslash(t *testing.T) {
	d := my(t)
	for in, want := range map[string]string{
		"plain":     `'plain'`,
		"it's":      `'it''s'`,
		`a\b`:       `'a\\b'`,
		`C:\Users`:  `'C:\\Users'`,
		`x\`:        `'x\\'`,
		`\'`:        `'\\'''`,
		`'; DROP--`: `'''; DROP--'`,
	} {
		if got := d.QuoteLiteral(in); got != want {
			t.Errorf("QuoteLiteral(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestMySQLClassifyType(t *testing.T) {
	for name, want := range map[string]ColumnKind{
		"VARCHAR": KindText, "TEXT": KindText, "char": KindText,
		"TINYTEXT": KindText, "MEDIUMTEXT": KindText, "LONGTEXT": KindText,
		"TINYINT": KindNumeric, "INT": KindNumeric, "BIGINT": KindNumeric,
		"MEDIUMINT": KindNumeric, "DECIMAL": KindNumeric, "DOUBLE": KindNumeric,
		"FLOAT": KindNumeric, "UNSIGNED BIGINT": KindNumeric, "UNSIGNED INT": KindNumeric,
		// Bytes are not text: a literal is in the connection charset and
		// equality with a binary column's raw bytes is not Go's equality.
		"BINARY": KindUnclassified, "VARBINARY": KindUnclassified, "BLOB": KindUnclassified,
		"DATE": KindUnclassified, "DATETIME": KindUnclassified, "TIMESTAMP": KindUnclassified,
		"JSON": KindUnclassified, "ENUM": KindUnclassified, "SET": KindUnclassified,
		"BIT": KindUnclassified, "GEOMETRY": KindUnclassified, "YEAR": KindUnclassified,
		"": KindUnclassified, "SOMETHING_NEW": KindUnclassified,
	} {
		if got := my(t).ClassifyType(name); got != want {
			t.Errorf("ClassifyType(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestMySQLComparisonForms(t *testing.T) {
	d := my(t)
	if got, want := d.CastToFloat("`amount`"), "CAST(`amount` AS DOUBLE)"; got != want {
		t.Errorf("CastToFloat = %s, want %s", got, want)
	}
	if got, want := d.CastToText("`amount`"), "CAST(`amount` AS CHAR)"; got != want {
		t.Errorf("CastToText = %s, want %s", got, want)
	}
	// The CONVERT is the load-bearing half: CAST AS BINARY alone compares
	// the column's own encoding, not the utf8mb4 bytes the driver hands Go.
	if got, want := d.ByteOrderedText("`city`"), "CAST(CONVERT(`city` USING utf8mb4) AS BINARY)"; got != want {
		t.Errorf("ByteOrderedText = %s, want %s", got, want)
	}
	if !strings.Contains(d.ProbeColumnsSQL("SELECT 1"), "LIMIT 0") {
		t.Error("probe should be a LIMIT 0 wrapper")
	}
}

// The identity rules, with the Postgres rule's strictness: every uncertain
// case answers false, because a wrong answer writes one customer's rows
// using another customer's connection.
func TestMySQLSameServer(t *testing.T) {
	a := my(t).(Addresser)
	base := "mysql://user:pw@tcp(db.internal:3306)/appdb"

	if !a.SameServer(base, base) {
		t.Error("identical URIs are the same server")
	}
	// The driver's default-port normalization applies before comparison.
	if !a.SameServer("mysql://user:pw@tcp(db.internal)/appdb", base) {
		t.Error("an omitted port is the default port")
	}

	for name, other := range map[string]string{
		"different host":     "mysql://user:pw@tcp(db2.internal:3306)/appdb",
		"different port":     "mysql://user:pw@tcp(db.internal:3307)/appdb",
		"different database": "mysql://user:pw@tcp(db.internal:3306)/otherdb",
		"different user":     "mysql://other:pw@tcp(db.internal:3306)/appdb",
		"different password": "mysql://user:pw2@tcp(db.internal:3306)/appdb",
		// charset changes session comparison semantics; anything in the
		// params map is either that or unknowable, so it disqualifies.
		"any dsn parameter": "mysql://user:pw@tcp(db.internal:3306)/appdb?charset=latin1",
		// A socket is a different addressing scheme with its own
		// equivalence argument, which has not been made.
		"unix socket":  "mysql://user:pw@unix(/var/run/mysqld.sock)/appdb",
		"postgres uri": "postgres://user:pw@db.internal:5432/appdb",
		"not a uri":    "not-a-uri",
		"empty":        "",
	} {
		if a.SameServer(base, other) {
			t.Errorf("%s must not be the same server: %s", name, other)
		}
		if a.SameServer(other, base) {
			t.Errorf("%s must not be the same server (reversed): %s", name, other)
		}
	}
}

// Struct-field DSN options (timeouts, parseTime, tls) affect the client
// session, not which server the composed statement runs on -- the statement
// executes entirely server-side. They are deliberately not compared, and
// this pins that decision so a change to it is a choice rather than drift.
func TestMySQLSameServerIgnoresClientOnlyOptions(t *testing.T) {
	a := my(t).(Addresser)
	if !a.SameServer(
		"mysql://user:pw@tcp(db:3306)/appdb?parseTime=true&timeout=10s",
		"mysql://user:pw@tcp(db:3306)/appdb",
	) {
		t.Error("client-only options must not break server identity")
	}
}
