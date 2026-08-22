package engine

import (
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

func qualifiedDS() *common.DataSet {
	return &common.DataSet{
		Columns: []string{"id", "amount"},
		Rows:    []common.DataRow{{"id": 1, "amount": 10}},
	}
}

// A schema-qualified table has to be quoted part by part. Quoting it
// whole asks for a table whose name contains a dot, which is why a sink
// pointed at another schema failed with "relation does not exist".
func TestGenerateSQLQuotesSchemaQualifiedTablePerPart(t *testing.T) {
	sql, err := GenerateSQL(SQLGenConfig{
		Dialect: "postgres",
		Table:   "analytics.daily_revenue",
		Mode:    ModeOverwrite,
	}, qualifiedDS())
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !strings.Contains(sql, `"analytics"."daily_revenue"`) {
		t.Fatalf("expected per-part quoting, got:\n%s", sql)
	}
	if strings.Contains(sql, `"analytics.daily_revenue"`) {
		t.Fatalf("table quoted as a single identifier:\n%s", sql)
	}
}

// The same applies to the bracket dialect.
func TestGenerateSQLQualifiedTableSQLServer(t *testing.T) {
	sql, err := GenerateSQL(SQLGenConfig{
		Dialect: "sqlserver",
		Table:   "dbo.orders",
	}, qualifiedDS())
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !strings.Contains(sql, "[dbo].[orders]") {
		t.Fatalf("expected [dbo].[orders], got:\n%s", sql)
	}
}

// CREATE TABLE goes through the same path.
func TestGenerateSQLQualifiedCreateTable(t *testing.T) {
	sql, err := GenerateSQL(SQLGenConfig{
		Dialect:     "postgres",
		Table:       "analytics.daily_revenue",
		CreateTable: true,
	}, qualifiedDS())
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !strings.Contains(sql, `CREATE TABLE IF NOT EXISTS "analytics"."daily_revenue"`) &&
		!strings.Contains(sql, `CREATE TABLE "analytics"."daily_revenue"`) {
		t.Fatalf("expected a qualified CREATE TABLE, got:\n%s", sql)
	}
}

// Unqualified names are untouched.
func TestGenerateSQLPlainTableUnchanged(t *testing.T) {
	sql, err := GenerateSQL(SQLGenConfig{Dialect: "postgres", Table: "orders"}, qualifiedDS())
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !strings.Contains(sql, `"orders"`) || strings.Contains(sql, `"".""`) {
		t.Fatalf("unexpected quoting for a plain table:\n%s", sql)
	}
}

// An empty part would produce an empty quoted token rather than a name.
func TestGenerateSQLRejectsEmptyQualifiedPart(t *testing.T) {
	for _, table := range []string{".daily_revenue", "analytics.", "a..b"} {
		if _, err := GenerateSQL(SQLGenConfig{Dialect: "postgres", Table: table}, qualifiedDS()); err == nil {
			t.Fatalf("expected %q to be rejected", table)
		}
	}
}

// Injection protection still applies to every part.
func TestGenerateSQLStillRejectsBreakoutCharsInQualifiedName(t *testing.T) {
	if _, err := GenerateSQL(SQLGenConfig{
		Dialect: "postgres",
		Table:   `analytics."; DROP TABLE users; --`,
	}, qualifiedDS()); err == nil {
		t.Fatal("expected an identifier with a quote character to be rejected")
	}
}
