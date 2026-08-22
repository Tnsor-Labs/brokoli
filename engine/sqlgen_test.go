package engine

import (
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

func sqlDS() *common.DataSet {
	return &common.DataSet{
		Columns: []string{"id", "name", "amount", "active", "created_at"},
		Rows: []common.DataRow{
			{"id": "1", "name": "Alice", "amount": "150.50", "active": "true", "created_at": "2024-01-15"},
			{"id": "2", "name": "Bob", "amount": "200", "active": "false", "created_at": "2024-02-20"},
			{"id": "3", "name": "Charlie", "amount": "75.25", "active": "true", "created_at": "2024-03-10"},
		},
	}
}

func TestGenerateSQL_Basic(t *testing.T) {
	sql, err := GenerateSQL(SQLGenConfig{
		Dialect: "generic", Table: "users", BatchSize: 100,
	}, sqlDS())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, `INSERT INTO "users"`) {
		t.Error("should contain INSERT INTO users")
	}
	if !strings.Contains(sql, "'Alice'") {
		t.Error("should contain Alice")
	}
}

func TestGenerateSQL_CreateTable(t *testing.T) {
	sql, err := GenerateSQL(SQLGenConfig{
		Dialect: "postgres", Table: "users", BatchSize: 100, CreateTable: true,
	}, sqlDS())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, "CREATE TABLE") {
		t.Error("should contain CREATE TABLE")
	}
	if !strings.Contains(sql, "INTEGER") {
		t.Error("should infer INTEGER for id column")
	}
	if !strings.Contains(sql, "DOUBLE PRECISION") {
		t.Error("should infer DOUBLE PRECISION for amount column")
	}
}

func TestGenerateSQL_Postgres(t *testing.T) {
	sql, err := GenerateSQL(SQLGenConfig{
		Dialect: "postgres", Table: "t", CreateTable: true,
	}, sqlDS())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, `"t"`) {
		t.Error("postgres should use double-quote identifiers")
	}
}

func TestGenerateSQL_MySQL(t *testing.T) {
	sql, err := GenerateSQL(SQLGenConfig{
		Dialect: "mysql", Table: "t", CreateTable: true,
	}, sqlDS())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, "`t`") {
		t.Error("mysql should use backtick identifiers")
	}
	if !strings.Contains(sql, "DOUBLE") {
		t.Error("mysql should use DOUBLE for float")
	}
}

func TestGenerateSQL_SQLite(t *testing.T) {
	sql, err := GenerateSQL(SQLGenConfig{
		Dialect: "sqlite", Table: "t", CreateTable: true,
	}, sqlDS())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, "REAL") {
		t.Error("sqlite should use REAL for float")
	}
}

func TestGenerateSQL_SQLServer(t *testing.T) {
	sql, err := GenerateSQL(SQLGenConfig{
		Dialect: "sqlserver", Table: "t", CreateTable: true,
	}, sqlDS())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, "[t]") {
		t.Error("sqlserver should use bracket identifiers")
	}
}

func TestGenerateSQL_Batching(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"id"},
		Rows:    make([]common.DataRow, 5),
	}
	for i := range ds.Rows {
		ds.Rows[i] = common.DataRow{"id": i + 1}
	}

	sql, err := GenerateSQL(SQLGenConfig{
		Table: "t", BatchSize: 2,
	}, ds)
	if err != nil {
		t.Fatal(err)
	}
	count := strings.Count(sql, "INSERT INTO")
	if count != 3 {
		t.Errorf("expected 3 INSERT statements for 5 rows with batch 2, got %d", count)
	}
}

func TestGenerateSQL_NullHandling(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"id", "name"},
		Rows: []common.DataRow{
			{"id": "1", "name": nil},
			{"id": "2", "name": ""},
		},
	}
	// nil is NULL. An empty string is an empty string: a database source
	// distinguishes the two, and collapsing them here destroyed that.
	// File sources, where the distinction genuinely does not exist, now
	// resolve it at the loader (an empty CSV field arrives as nil), and
	// EmptyStringAsNull restores the old behaviour for anyone who wants
	// it at the sink.
	sql, err := GenerateSQL(SQLGenConfig{Table: "t"}, ds)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(sql, "NULL"); got != 1 {
		t.Errorf("expected 1 NULL (the nil), got %d in:\n%s", got, sql)
	}
	if !strings.Contains(sql, "''") {
		t.Errorf("expected the empty string to survive as an empty literal:\n%s", sql)
	}

	sql, err = GenerateSQL(SQLGenConfig{Table: "t", EmptyStringAsNull: true}, ds)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(sql, "NULL"); got != 2 {
		t.Errorf("with EmptyStringAsNull, expected 2 NULLs, got %d", got)
	}
}

func TestGenerateSQL_QuoteEscaping(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"name"},
		Rows:    []common.DataRow{{"name": "O'Brien"}},
	}
	sql, err := GenerateSQL(SQLGenConfig{Table: "t"}, ds)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, "O''Brien") {
		t.Error("should escape single quotes")
	}
}

func TestGenerateSQL_EmptyDataset(t *testing.T) {
	ds := &common.DataSet{Columns: []string{}, Rows: nil}
	_, err := GenerateSQL(SQLGenConfig{Table: "t"}, ds)
	if err == nil {
		t.Error("should error on empty columns")
	}
}

func TestTypeInference(t *testing.T) {
	tests := []struct {
		name     string
		values   []interface{}
		expected string
	}{
		{"integers", []interface{}{"1", "2", "3", "4", "5"}, "INTEGER"},
		{"floats", []interface{}{"1.5", "2.3", "3.7"}, "FLOAT"},
		{"mixed_int_float", []interface{}{"1", "2.5", "3", "4.1"}, "FLOAT"},
		{"booleans", []interface{}{"true", "false", "true"}, "BOOLEAN"},
		{"dates", []interface{}{"2024-01-01", "2024-02-15", "2024-03-20"}, "TIMESTAMP"},
		{"text", []interface{}{"hello", "world", "foo"}, "TEXT"},
		{"empty", []interface{}{nil, nil, nil}, "TEXT"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows := make([]common.DataRow, len(tt.values))
			for i, v := range tt.values {
				rows[i] = common.DataRow{"col": v}
			}
			result := inferColumnType("col", rows)
			if result != tt.expected {
				t.Errorf("inferColumnType(%s) = %q, want %q", tt.name, result, tt.expected)
			}
		})
	}
}

func TestSplitStatements(t *testing.T) {
	input := `CREATE TABLE t (id INT);INSERT INTO t VALUES (1, 'hello;world');INSERT INTO t VALUES (2, 'test')`
	stmts := splitStatements(input)
	if len(stmts) != 3 {
		t.Errorf("expected 3 statements, got %d: %v", len(stmts), stmts)
	}
	if !strings.Contains(stmts[1], "hello;world") {
		t.Error("should preserve semicolons inside quotes")
	}
}

// brokoli-sdk#12: write modes for standalone sink_db.

func TestGenerateSQL_Overwrite(t *testing.T) {
	sql, err := GenerateSQL(SQLGenConfig{
		Dialect: "postgres", Table: "users", Mode: "overwrite",
	}, sqlDS())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, `DELETE FROM "users";`) {
		t.Errorf("overwrite should clear the table first:\n%s", sql)
	}
	if !strings.Contains(sql, `INSERT INTO "users"`) {
		t.Errorf("overwrite should still insert:\n%s", sql)
	}
	if strings.Index(sql, "DELETE FROM") > strings.Index(sql, "INSERT INTO") {
		t.Error("DELETE must come before INSERT")
	}
}

func TestGenerateSQL_UpsertPostgres(t *testing.T) {
	sql, err := GenerateSQL(SQLGenConfig{
		Dialect: "postgres", Table: "users", Mode: "upsert", KeyColumns: []string{"id"},
	}, sqlDS())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, `ON CONFLICT ("id") DO UPDATE SET`) {
		t.Errorf("postgres upsert should use ON CONFLICT DO UPDATE:\n%s", sql)
	}
	if !strings.Contains(sql, `"name" = EXCLUDED."name"`) {
		t.Errorf("should set non-key columns from EXCLUDED:\n%s", sql)
	}
	if strings.Contains(sql, `"id" = EXCLUDED."id"`) {
		t.Error("must not update the key column itself")
	}
}

func TestGenerateSQL_UpsertMySQL(t *testing.T) {
	sql, err := GenerateSQL(SQLGenConfig{
		Dialect: "mysql", Table: "users", Mode: "upsert", KeyColumns: []string{"id"},
	}, sqlDS())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, "ON DUPLICATE KEY UPDATE") {
		t.Errorf("mysql upsert should use ON DUPLICATE KEY UPDATE:\n%s", sql)
	}
	if !strings.Contains(sql, "`name` = VALUES(`name`)") {
		t.Errorf("should set columns from VALUES():\n%s", sql)
	}
}

func TestGenerateSQL_UpsertRequiresKeyColumns(t *testing.T) {
	_, err := GenerateSQL(SQLGenConfig{
		Dialect: "postgres", Table: "users", Mode: "upsert",
	}, sqlDS())
	if err == nil {
		t.Fatal("postgres upsert without key_columns must error")
	}
	if !strings.Contains(err.Error(), "key_columns") {
		t.Errorf("error should name key_columns: %v", err)
	}
}

func TestGenerateSQL_UpsertUnsupportedDialect(t *testing.T) {
	_, err := GenerateSQL(SQLGenConfig{
		Dialect: "sqlserver", Table: "users", Mode: "upsert", KeyColumns: []string{"id"},
	}, sqlDS())
	if err == nil {
		t.Fatal("upsert on an unsupported dialect must error, not silently insert")
	}
}

func TestGenerateSQL_UnknownModeErrors(t *testing.T) {
	_, err := GenerateSQL(SQLGenConfig{
		Dialect: "postgres", Table: "users", Mode: "merge",
	}, sqlDS())
	if err == nil {
		t.Fatal("an unknown write mode must error")
	}
}

func TestDialectForURI(t *testing.T) {
	cases := map[string]string{
		"postgres://x":  "postgres",
		"mysql://x":     "mysql",
		"/tmp/a.db":     "sqlite",
		"sqlserver://x": "sqlserver",
		"snowflake://x": "generic",
	}
	for uri, want := range cases {
		if got := dialectForURI(uri); got != want {
			t.Errorf("dialectForURI(%q) = %q, want %q", uri, got, want)
		}
	}
}
