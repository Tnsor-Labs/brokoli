package engine

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// migratePostgresSameServer stages a query inside the destination session so
// rows never cross the database client when source and destination are the
// exact same Postgres endpoint. The caller has already verified the endpoint
// identity includes credentials, so executing the source query as the writer
// cannot broaden its access.
func migratePostgresSameServer(ctx context.Context, uri, query, table string, keyColumns []string, args []interface{}, disjoint bool) (int64, error) {
	driver, dsn, err := DetectDriver(uri)
	if err != nil {
		return 0, err
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", driver, err)
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		return 0, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Close()

	var affected int64
	if err := conn.Raw(func(dc any) error {
		pc, ok := dc.(*stdlib.Conn)
		if !ok {
			return fmt.Errorf("connection is not a pgx connection")
		}
		tx, err := pc.Conn().Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin: %w", err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck // no-op once committed

		d := getDialect("postgres")
		if !disjoint {
			if _, err := tx.Exec(ctx, "LOCK TABLE "+d.quoteIdent(table)+" IN EXCLUSIVE MODE"); err != nil {
				return fmt.Errorf("lock table for upsert: %w", err)
			}
		}
		if err := validatePostgresUpsertKey(ctx, tx, table, keyColumns); err != nil {
			return err
		}
		stage := d.quoteIdent(upsertStageName)
		stageSQL := "CREATE TEMP TABLE " + stage + " ON COMMIT DROP AS SELECT * FROM (" + query + ") AS brokoli_direct_source"
		if _, err := tx.Exec(ctx, stageSQL, args...); err != nil {
			return fmt.Errorf("stage source query: %w", err)
		}
		if _, err := tx.Exec(ctx, "ALTER TABLE "+stage+" ADD COLUMN __brokoli_seq BIGSERIAL"); err != nil {
			return fmt.Errorf("sequence staged source rows: %w", err)
		}
		columns, err := postgresStageColumns(ctx, tx)
		if err != nil {
			return err
		}
		if len(columns) == 0 {
			return fmt.Errorf("source query returned no columns")
		}
		columns = columns[:len(columns)-1] // __brokoli_seq is staging metadata, not a target column.
		if _, err := tx.Exec(ctx, d.upsertDedupSQL(keyColumns)); err != nil {
			return fmt.Errorf("dedup upsert stage for %s: %w", table, err)
		}
		merge, err := d.upsertMergeSQL(table, columns, keyColumns)
		if err != nil {
			return err
		}
		for _, statement := range merge {
			tag, err := tx.Exec(ctx, statement)
			if err != nil {
				return fmt.Errorf("merge upsert stage into %s: %w", table, err)
			}
			affected += tag.RowsAffected()
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit: %w", err)
		}
		return nil
	}); err != nil {
		return 0, err
	}
	return affected, nil
}

func canMigratePostgresSameServer(sourceURI, destURI, dialect, mode string, createTable bool, query string) bool {
	return !createTable && strings.EqualFold(dialect, "postgres") && strings.EqualFold(mode, ModeUpsert) &&
		dialectForURI(sourceURI) == "postgres" && sameServer("postgres", sourceURI, destURI) && !strings.Contains(query, ";")
}

func postgresStageColumns(ctx context.Context, tx pgx.Tx) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT attname
		FROM pg_attribute
		WHERE attrelid = to_regclass('pg_temp.brokoli_upsert_stage')
		  AND attnum > 0 AND NOT attisdropped
		ORDER BY attnum`)
	if err != nil {
		return nil, fmt.Errorf("read staged source columns: %w", err)
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			return nil, err
		}
		columns = append(columns, column)
	}
	return columns, rows.Err()
}
