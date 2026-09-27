package engine

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/microsoft/go-mssqldb"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// copyBatchesToSQLServer writes rows through go-mssqldb's TDS bulk-copy
// protocol. Unlike the statement path, values are not rendered into SQL text
// and parsed again by the server. The transaction also contains overwrite's
// DELETE and an optional create-table DDL, preserving the write-path atomicity
// contract while keeping resident memory bounded to the incoming batch.
func copyBatchesToSQLServer(ctx context.Context, uri string, cfg SQLGenConfig, columns []string, next func() (*common.DataSet, error)) (int64, error) {
	for _, id := range append([]string{cfg.Table}, columns...) {
		if err := validateIdentifier(id); err != nil {
			return 0, fmt.Errorf("invalid identifier %q: %w", id, err)
		}
	}

	driver, dsn, err := DetectDriver(uri)
	if err != nil {
		return 0, err
	}
	if driver != "sqlserver" {
		return 0, fmt.Errorf("SQL Server bulk writer requires the sqlserver driver, got %q", driver)
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", driver, err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	conn, err := db.Conn(ctx)
	if err != nil {
		return 0, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Close()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin SQL Server bulk write: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if cfg.CreateDDL != "" {
		if _, err := tx.ExecContext(ctx, cfg.CreateDDL); err != nil {
			return 0, fmt.Errorf("create %s: %w", cfg.Table, err)
		}
	}
	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))
	if mode == ModeOverwrite || mode == "replace" {
		if _, err := tx.ExecContext(ctx, getDialect("sqlserver").clearTable(cfg.Table, cfg.Truncate)); err != nil {
			return 0, fmt.Errorf("clear %s: %w", cfg.Table, err)
		}
	}

	destination, err := sqlServerDestinationColumns(ctx, tx, cfg.Table, columns)
	if err != nil {
		return 0, err
	}
	stmt, err := tx.PrepareContext(ctx, mssql.CopyIn(cfg.Table, mssql.BulkOptions{
		KeepNulls:    true,
		RowsPerBatch: 5000,
		Tablock:      true,
	}, destination...))
	if err != nil {
		return 0, fmt.Errorf("prepare SQL Server bulk copy: %w", err)
	}
	defer stmt.Close()

	var affected int64
	for {
		batch, err := next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, fmt.Errorf("read SQL Server bulk batch: %w", err)
		}
		if batch == nil {
			continue
		}
		for _, row := range batch.Rows {
			values := make([]interface{}, len(columns))
			for i, column := range columns {
				values[i] = row[column]
			}
			if _, err := stmt.ExecContext(ctx, values...); err != nil {
				return 0, fmt.Errorf("write SQL Server bulk row %d: %w", affected, err)
			}
			affected++
		}
	}
	if _, err := stmt.ExecContext(ctx); err != nil {
		return 0, fmt.Errorf("finish SQL Server bulk copy: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit SQL Server bulk write: %w", err)
	}
	committed = true
	return affected, nil
}

// sqlServerDestinationColumns returns the destination table's own name for
// each column, in the order given.
//
// go-mssqldb matches bulk-copy columns to the table byte for byte. SQL
// Server resolves a column name under the database's collation, which is
// case-insensitive by default, so the statement path wrote a dataset
// column "ID" into a table column "id", and the bulk path refused it
// ("column ID does not exist in destination table"). Asking the server
// with name = @p2 applies exactly the collation the statement path got:
// a case-insensitive database matches regardless of case, a case-sensitive
// one does not. A column the server does not find keeps its given name, so
// the driver's own error names it.
func sqlServerDestinationColumns(ctx context.Context, tx *sql.Tx, table string, columns []string) ([]string, error) {
	out := make([]string, len(columns))
	for i, column := range columns {
		var name string
		err := tx.QueryRowContext(ctx,
			"SELECT name FROM sys.columns WHERE object_id = OBJECT_ID(@p1) AND name = @p2",
			table, column).Scan(&name)
		switch {
		case err == sql.ErrNoRows:
			name = column
		case err != nil:
			return nil, fmt.Errorf("look up column %q of %s: %w", column, table, err)
		}
		out[i] = name
	}
	return out, nil
}
