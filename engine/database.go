package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/common"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// DetectDriver returns the Go sql driver name for a connection URI.
// dialectForURI maps a connection URI to the SQL dialect name GenerateSQL
// understands, via the same scheme detection DetectDriver uses. Snowflake
// and anything unrecognized fall back to "generic" — append/overwrite work
// there, and upsert (which has no portable form) errors with a name.
func dialectForURI(uri string) string {
	driver, _, err := DetectDriver(uri)
	if err != nil {
		return "generic"
	}
	switch driver {
	case "pgx":
		return "postgres"
	case "mysql":
		return "mysql"
	case "sqlite":
		return "sqlite"
	case "sqlserver":
		return "sqlserver"
	default:
		return "generic"
	}
}

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
			if connType == "redshift" {
				port = 5439
			} else {
				port = 5432
			}
		}
		return fmt.Sprintf("%s://%s:%s@%s:%d/%s?sslmode=require", scheme, login, password, host, port, schema)
	case "snowflake":
		// Snowflake DSN: user:password@account/database/schema?warehouse=X
		warehouse := "COMPUTE_WH"
		if extra != "" {
			// Try to parse warehouse from extra JSON
			var ex map[string]string
			if err := parseJSON(extra, &ex); err == nil {
				if w, ok := ex["warehouse"]; ok {
					warehouse = w
				}
			}
		}
		return fmt.Sprintf("snowflake://%s:%s@%s/%s?warehouse=%s", login, password, host, schema, warehouse)
	case "mysql":
		if port == 0 {
			port = 3306
		}
		return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s", login, password, host, port, schema)
	case "mssql", "sqlserver":
		if port == 0 {
			port = 1433
		}
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

	// Guard the in-memory dataset against the worker's memory budget.
	// Without this, a query returning more than the pod can hold takes
	// the whole process down with an OOM kill — losing every other run
	// on that worker and leaving this one to be failed later by
	// recovery with "interrupted mid-execution", which tells the author
	// nothing about what actually went wrong.
	budget := datasetMemoryBudget()
	var sampledBytes int64
	var maxRows int

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

		if budget > 0 {
			// Size the first rows, then compare a count — estimating
			// every row would cost more than the guard is worth.
			if len(dataRows) <= datasetSampleRows {
				sampledBytes += estimateRowBytes(row)
				if len(dataRows) == datasetSampleRows {
					avg := sampledBytes / int64(datasetSampleRows)
					if avg < 1 {
						avg = 1
					}
					maxRows = int(budget / avg)
				}
			} else if maxRows > 0 && len(dataRows) > maxRows {
				avg := sampledBytes / int64(datasetSampleRows)
				return nil, fmt.Errorf(
					"query result is too large to hold in memory: stopped after %d rows at about %s (budget %s, roughly %d bytes per row). "+
						"Narrow the query with a WHERE clause or LIMIT, split it across runs, or give the worker more memory "+
						"(BROKOLI_DATASET_MEMORY_BUDGET overrides the budget)",
					len(dataRows), humanBytes(int64(len(dataRows))*avg), humanBytes(budget), avg)
			}
		}
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

const (
	// datasetSampleRows is how many rows are measured to derive an
	// average row size. Large enough to be representative of a result
	// set, small enough that the measuring itself is free.
	datasetSampleRows = 200

	// datasetBudgetFraction is the share of the process memory limit a
	// single materialised dataset may occupy. Deliberately well under
	// half: a node holds its input while building its output, the sink
	// encodes another copy on the way out, and the Go allocator does not
	// hand memory back promptly. A third leaves room for all three.
	datasetBudgetFraction = 0.30
)

// datasetMemoryBudget reports how many bytes one materialised dataset may
// occupy, or 0 when no limit is known — on a bare host with no memory
// limit set, behaviour is exactly as it was before this guard existed.
func datasetMemoryBudget() int64 {
	if v := os.Getenv("BROKOLI_DATASET_MEMORY_BUDGET"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	limit := debug.SetMemoryLimit(-1)
	if limit <= 0 || limit == math.MaxInt64 {
		return 0
	}
	return int64(float64(limit) * datasetBudgetFraction)
}

// estimateRowBytes approximates a row's resident size: the data itself
// plus the per-entry cost of holding it in a map of interfaces, which for
// narrow rows dominates the values.
func estimateRowBytes(row common.DataRow) int64 {
	const perEntryOverhead = 48 // map bucket slot + interface header + key header
	var n int64
	for k, v := range row {
		n += int64(len(k)) + perEntryOverhead
		switch t := v.(type) {
		case string:
			n += int64(len(t))
		case []byte:
			n += int64(len(t))
		case nil:
			// nothing beyond the entry itself
		default:
			n += 16
		}
	}
	return n
}

// humanBytes renders a byte count the way an operator reads it.
func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/float64(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MiB", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KiB", float64(n)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
