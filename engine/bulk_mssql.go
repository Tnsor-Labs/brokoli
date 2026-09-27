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

	stmt, err := tx.PrepareContext(ctx, mssql.CopyIn(cfg.Table, mssql.BulkOptions{
		KeepNulls:    true,
		RowsPerBatch: 5000,
		Tablock:      true,
	}, columns...))
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
