package engine

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// SQLGenConfig holds settings for SQL generation.
type SQLGenConfig struct {
	Dialect     string `json:"dialect"`
	Table       string `json:"table"`
	BatchSize   int    `json:"batch_size"`
	CreateTable bool   `json:"create_table"`
}

// GenerateSQL produces SQL statements from a DataSet.
func GenerateSQL(cfg SQLGenConfig, ds *common.DataSet) (string, error) {
	if len(ds.Columns) == 0 {
		return "", fmt.Errorf("no columns in dataset")
	}

	if cfg.Table == "" {
		cfg.Table = "data"
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 100
	}
	if cfg.Dialect == "" {
		cfg.Dialect = "generic"
	}

	d := getDialect(cfg.Dialect)
	var sb strings.Builder

	if cfg.CreateTable {
		colTypes := inferTypes(ds.Columns, ds.Rows)
		sb.WriteString(d.createTable(cfg.Table, ds.Columns, colTypes))
		sb.WriteString("\n\n")
	}

	// Generate INSERT statements in batches
	for i := 0; i < len(ds.Rows); i += cfg.BatchSize {
		end := i + cfg.BatchSize
		if end > len(ds.Rows) {
			end = len(ds.Rows)
		}
		batch := ds.Rows[i:end]
		sb.WriteString(d.insertBatch(cfg.Table, ds.Columns, batch))
		sb.WriteString("\n")
	}

	return sb.String(), nil
}

// --- Type inference ---

func inferTypes(columns []string, rows []common.DataRow) map[string]string {
	types := make(map[string]string, len(columns))
	for _, col := range columns {
		types[col] = inferColumnType(col, rows)
	}
	return types
}

func inferColumnType(col string, rows []common.DataRow) string {
	var intCount, floatCount, boolCount, dateCount, total int

	for _, row := range rows {
		val, ok := row[col]
		if !ok || val == nil {
			continue
		}
		total++
		s := fmt.Sprintf("%v", val)
		if s == "" {
			continue
		}

		if _, err := strconv.ParseInt(s, 10, 64); err == nil {
			intCount++
			continue
		}
		if _, err := strconv.ParseFloat(s, 64); err == nil {
			floatCount++
			continue
		}
		lower := strings.ToLower(s)
		if lower == "true" || lower == "false" {
			boolCount++
			continue
		}
		if isDate(s) {
			dateCount++
		}
	}

	if total == 0 {
		return "TEXT"
	}

	threshold := int(float64(total) * 0.8)
	if intCount >= threshold {
		return "INTEGER"
	}
	if floatCount+intCount >= threshold {
		return "FLOAT"
	}
	if boolCount >= threshold {
		return "BOOLEAN"
	}
	if dateCount >= threshold {
		return "TIMESTAMP"
	}
	return "TEXT"
}

var dateFormats = []string{
	time.RFC3339,
	"2006-01-02",
	"2006-01-02 15:04:05",
	"01/02/2006",
	"02-01-2006",
}

func isDate(s string) bool {
	for _, f := range dateFormats {
		if _, err := time.Parse(f, s); err == nil {
			return true
		}
	}
	return false
}

// --- Dialects ---

type dialect struct {
	name       string
	quoteChar  string
	strQuote   string
	terminator string
	boolTrue   string
	boolFalse  string
	typeMap    map[string]string
}

func getDialect(name string) dialect {
	switch strings.ToLower(name) {
	case "postgres", "postgresql":
		return dialect{
			name: "postgres", quoteChar: `"`, strQuote: "'", terminator: ";",
			boolTrue: "TRUE", boolFalse: "FALSE",
			typeMap: map[string]string{"INTEGER": "INTEGER", "FLOAT": "DOUBLE PRECISION", "BOOLEAN": "BOOLEAN", "TEXT": "TEXT", "TIMESTAMP": "TIMESTAMP"},
		}
	case "mysql":
		return dialect{
			name: "mysql", quoteChar: "`", strQuote: "'", terminator: ";",
			boolTrue: "TRUE", boolFalse: "FALSE",
			typeMap: map[string]string{"INTEGER": "INT", "FLOAT": "DOUBLE", "BOOLEAN": "BOOLEAN", "TEXT": "TEXT", "TIMESTAMP": "DATETIME"},
		}
	case "sqlite":
		return dialect{
			name: "sqlite", quoteChar: `"`, strQuote: "'", terminator: ";",
			boolTrue: "1", boolFalse: "0",
			typeMap: map[string]string{"INTEGER": "INTEGER", "FLOAT": "REAL", "BOOLEAN": "INTEGER", "TEXT": "TEXT", "TIMESTAMP": "TEXT"},
		}
	case "sqlserver", "mssql":
		return dialect{
			name: "sqlserver", quoteChar: "[", strQuote: "'", terminator: ";",
			boolTrue: "1", boolFalse: "0",
			typeMap: map[string]string{"INTEGER": "INT", "FLOAT": "FLOAT", "BOOLEAN": "BIT", "TEXT": "NVARCHAR(MAX)", "TIMESTAMP": "DATETIME2"},
		}
	default:
		return dialect{
			name: "generic", quoteChar: `"`, strQuote: "'", terminator: ";",
			boolTrue: "TRUE", boolFalse: "FALSE",
			typeMap: map[string]string{"INTEGER": "INTEGER", "FLOAT": "FLOAT", "BOOLEAN": "BOOLEAN", "TEXT": "TEXT", "TIMESTAMP": "TIMESTAMP"},
		}
	}
}

func (d dialect) quoteIdent(s string) string {
	if d.quoteChar == "[" {
		return "[" + s + "]"
	}
	return d.quoteChar + s + d.quoteChar
}

func (d dialect) formatValue(v interface{}) string {
	if v == nil {
		return "NULL"
	}
	s := fmt.Sprintf("%v", v)
	if s == "" {
		return "NULL"
	}
	lower := strings.ToLower(s)
	if lower == "true" {
		return d.boolTrue
	}
	if lower == "false" {
		return d.boolFalse
	}
	if _, err := strconv.ParseInt(s, 10, 64); err == nil {
		return s
	}
	if _, err := strconv.ParseFloat(s, 64); err == nil {
		return s
	}
	// String — escape single quotes
	escaped := strings.ReplaceAll(s, "'", "''")
	return d.strQuote + escaped + d.strQuote
}

func (d dialect) createTable(table string, columns []string, types map[string]string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("CREATE TABLE %s (\n", d.quoteIdent(table)))
	for i, col := range columns {
		sqlType := d.typeMap[types[col]]
		if sqlType == "" {
			sqlType = "TEXT"
		}
		sb.WriteString(fmt.Sprintf("  %s %s", d.quoteIdent(col), sqlType))
		if i < len(columns)-1 {
			sb.WriteString(",")
		}
		sb.WriteString("\n")
	}
	sb.WriteString(")" + d.terminator)
	return sb.String()
}

func (d dialect) insertBatch(table string, columns []string, rows []common.DataRow) string {
	if len(rows) == 0 {
		return ""
	}

	quotedCols := make([]string, len(columns))
	for i, c := range columns {
		quotedCols[i] = d.quoteIdent(c)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("INSERT INTO %s (%s) VALUES\n",
		d.quoteIdent(table), strings.Join(quotedCols, ", ")))

	for i, row := range rows {
		vals := make([]string, len(columns))
		for j, col := range columns {
			vals[j] = d.formatValue(row[col])
		}
		sb.WriteString("  (" + strings.Join(vals, ", ") + ")")
		if i < len(rows)-1 {
			sb.WriteString(",\n")
		}
	}
	sb.WriteString(d.terminator)
	return sb.String()
}
