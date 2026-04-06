package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hc12r/brokolisql-go/pkg/common"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// DetectDriver returns the Go sql driver name for a connection URI.
func DetectDriver(uri string) (string, string, error) {
	switch {
	case strings.HasPrefix(uri, "postgres://") || strings.HasPrefix(uri, "postgresql://"):
		return "pgx", uri, nil
	case strings.HasPrefix(uri, "redshift://"):
		// Redshift is Postgres-compatible — convert scheme and use pgx
		dsn := "postgres://" + strings.TrimPrefix(uri, "redshift://")
		return "pgx", dsn, nil
	case strings.HasPrefix(uri, "snowflake://"):
		return "snowflake", strings.TrimPrefix(uri, "snowflake://"), nil
	case strings.HasPrefix(uri, "mysql://"):
		dsn := strings.TrimPrefix(uri, "mysql://")
		return "mysql", dsn, nil
	case strings.HasPrefix(uri, "sqlite://"):
		path := strings.TrimPrefix(uri, "sqlite://")
		return "sqlite", path, nil
	case strings.HasSuffix(uri, ".db") || strings.HasSuffix(uri, ".sqlite"):
		return "sqlite", uri, nil
	case strings.HasPrefix(uri, "sqlserver://") || strings.HasPrefix(uri, "mssql://"):
		return "sqlserver", uri, nil
	default:
		return "pgx", uri, nil
	}
}

// BuildConnectionURI constructs a URI from connection fields for various database types.
func BuildConnectionURI(connType, host string, port int, schema, login, password, extra string) string {
	switch connType {
	case "postgres", "redshift":
		scheme := "postgres"
		if connType == "redshift" {
			scheme = "redshift"
		}
		if port == 0 {
			if connType == "redshift" { port = 5439 } else { port = 5432 }
		}
		return fmt.Sprintf("%s://%s:%s@%s:%d/%s?sslmode=require", scheme, login, password, host, port, schema)
	case "snowflake":
		// Snowflake DSN: user:password@account/database/schema?warehouse=X
		warehouse := "COMPUTE_WH"
		if extra != "" {
			// Try to parse warehouse from extra JSON
			var ex map[string]string
			if err := parseJSON(extra, &ex); err == nil {
				if w, ok := ex["warehouse"]; ok { warehouse = w }
			}
		}
		return fmt.Sprintf("snowflake://%s:%s@%s/%s?warehouse=%s", login, password, host, schema, warehouse)
	case "mysql":
		if port == 0 { port = 3306 }
		return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s", login, password, host, port, schema)
	case "mssql", "sqlserver":
		if port == 0 { port = 1433 }
		return fmt.Sprintf("sqlserver://%s:%s@%s:%d?database=%s", login, password, host, port, schema)
	default:
		return host
	}
}

func parseJSON(s string, v interface{}) error {
	return json.Unmarshal([]byte(s), v)
}

// QueryDatabase opens a connection, runs a query, and returns a DataSet.
func QueryDatabase(uri, query string) (*common.DataSet, error) {
	driver, dsn, err := DetectDriver(uri)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", driver, err)
	}
	defer db.Close()

	// 5 minute query timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("ping %s: %w", driver, err)
	}

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("columns: %w", err)
	}

	var dataRows []common.DataRow
	for rows.Next() {
		// Create scan targets
		values := make([]interface{}, len(columns))
		ptrs := make([]interface{}, len(columns))
		for i := range values {
			ptrs[i] = &values[i]
		}

		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}

		row := make(common.DataRow, len(columns))
		for i, col := range columns {
			v := values[i]
			// Convert []byte to string for readability
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			row[col] = v
		}
		dataRows = append(dataRows, row)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration: %w", err)
	}

	return &common.DataSet{
		Columns: columns,
		Rows:    dataRows,
	}, nil
}

// ExecuteSQL opens a connection and executes SQL statements (for sink_db).
func ExecuteSQL(uri, sqlStatements string) (int64, error) {
	driver, dsn, err := DetectDriver(uri)
	if err != nil {
		return 0, err
	}

	db, err := sql.Open(driver, dsn)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", driver, err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		return 0, fmt.Errorf("ping %s: %w", driver, err)
	}

	// Execute in a transaction
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}

	// Split on semicolons and execute each statement
	statements := splitStatements(sqlStatements)
	var totalAffected int64

	for _, stmt := range statements {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		result, err := tx.Exec(stmt)
		if err != nil {
			tx.Rollback()
			return 0, fmt.Errorf("exec: %w", err)
		}
		affected, _ := result.RowsAffected()
		totalAffected += affected
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}

	return totalAffected, nil
}

// splitStatements splits SQL text on semicolons, respecting quoted strings.
func splitStatements(sql string) []string {
	var stmts []string
	var current strings.Builder
	inQuote := false
	quoteChar := byte(0)

	for i := 0; i < len(sql); i++ {
		c := sql[i]
		if inQuote {
			current.WriteByte(c)
			if c == quoteChar {
				// Check for escaped quote
				if i+1 < len(sql) && sql[i+1] == quoteChar {
					current.WriteByte(sql[i+1])
					i++
				} else {
					inQuote = false
				}
			}
		} else if c == '\'' || c == '"' {
			inQuote = true
			quoteChar = c
			current.WriteByte(c)
		} else if c == ';' {
			s := strings.TrimSpace(current.String())
			if s != "" {
				stmts = append(stmts, s)
			}
			current.Reset()
		} else {
			current.WriteByte(c)
		}
	}

	s := strings.TrimSpace(current.String())
	if s != "" {
		stmts = append(stmts, s)
	}
	return stmts
}
