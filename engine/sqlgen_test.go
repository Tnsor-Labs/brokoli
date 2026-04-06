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
	sql, err := GenerateSQL(SQLGenConfig{Table: "t"}, ds)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(sql, "NULL") != 2 {
		t.Errorf("expected 2 NULLs, got %d", strings.Count(sql, "NULL"))
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
