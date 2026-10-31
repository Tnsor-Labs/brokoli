package engine

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"math"
	"strconv"
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
	names := make([]string, len(destination))
	for i, c := range destination {
		names[i] = c.name
	}
	stmt, err := tx.PrepareContext(ctx, mssql.CopyIn(cfg.Table, mssql.BulkOptions{
		KeepNulls:    true,
		RowsPerBatch: 5000,
		Tablock:      true,
	}, names...))
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
				v, err := sqlServerBulkValue(row[column], destination[i].typ)
				if err != nil {
					return 0, fmt.Errorf("write SQL Server bulk row %d, column %s: %w", affected, column, err)
				}
				values[i] = v
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

// sqlServerColumn is a destination column as the server declares it: its
// own name and its type's name.
type sqlServerColumn struct{ name, typ string }

// sqlServerDestinationColumns returns the destination table's own name for
// each column, in the order given, with the column's type.
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
func sqlServerDestinationColumns(ctx context.Context, tx *sql.Tx, table string, columns []string) ([]sqlServerColumn, error) {
	out := make([]sqlServerColumn, len(columns))
	for i, column := range columns {
		var c sqlServerColumn
		err := tx.QueryRowContext(ctx,
			"SELECT c.name, t.name FROM sys.columns c JOIN sys.types t ON t.user_type_id = c.system_type_id "+
				"WHERE c.object_id = OBJECT_ID(@p1) AND c.name = @p2",
			table, column).Scan(&c.name, &c.typ)
		switch {
		case err == sql.ErrNoRows:
			c = sqlServerColumn{name: column}
		case err != nil:
			return nil, fmt.Errorf("look up column %q of %s: %w", column, table, err)
		}
		out[i] = c
	}
	return out, nil
}

// sqlServerBulkValue makes a value fit for its destination column the way
// the statement path's literal would be made to fit by the server.
//
// Two gaps in go-mssqldb's bulk copy make this necessary. It refuses a
// string for integer, float and bit columns (date, decimal and character
// columns take one), and a CSV source hands every value over as a string:
// a table created from that CSV gets INT and FLOAT columns, inferred from
// the values, so a create-and-load from CSV failed on its first row with
// "mssql: invalid type for int column: string". And it writes an integer
// into a narrower column by truncating its bytes, so 300 bound for a
// tinyint was stored as 44, and a value past an INT's range wrapped,
// where the statement path's literal is refused with "overflowed".
//
// The rules follow SQL Server's own conversion, which the differential
// test against a live server holds this to. Text: surrounding spaces are
// ignored and an empty string is 0. Integer columns refuse a value outside
// their range. A bit takes true or false in any case, or any whole number,
// non-zero meaning 1. Anything else is refused, as the server refuses it.
func sqlServerBulkValue(v any, typ string) (any, error) {
	if lo, hi, ok := sqlServerIntRange(typ); ok {
		n, isNum, err := sqlServerWholeNumber(v)
		if err != nil || !isNum {
			return v, err
		}
		if n < lo || n > hi {
			return nil, fmt.Errorf("%v overflows a %s column", v, typ)
		}
		return n, nil
	}
	s, ok := v.(string)
	if !ok {
		return v, nil
	}
	t := strings.TrimSpace(s)
	switch typ {
	case "real", "float":
		if t == "" {
			return float64(0), nil
		}
		f, err := strconv.ParseFloat(t, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("%q is not a number for a %s column", s, typ)
		}
		return f, nil
	case "bit":
		switch strings.ToLower(t) {
		case "true":
			return true, nil
		case "false", "":
			return false, nil
		}
		n, err := strconv.ParseInt(t, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%q is not a whole number, true or false for a bit column", s)
		}
		return n != 0, nil
	}
	return v, nil
}

// sqlServerIntRange is the range of an integer column type.
func sqlServerIntRange(typ string) (lo, hi int64, ok bool) {
	switch typ {
	case "tinyint":
		return 0, math.MaxUint8, true
	case "smallint":
		return math.MinInt16, math.MaxInt16, true
	case "int":
		return math.MinInt32, math.MaxInt32, true
	case "bigint":
		return math.MinInt64, math.MaxInt64, true
	}
	return 0, 0, false
}

// sqlServerWholeNumber reads v as the whole number an integer column would
// store. isNum is false for a value that is not a number or numeric text
// (a bool, say), which the driver then accepts or refuses on its own. A
// float is truncated toward zero, as the server converts one.
func sqlServerWholeNumber(v any) (n int64, isNum bool, err error) {
	switch t := v.(type) {
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return 0, true, nil
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			if ne, ok := err.(*strconv.NumError); ok && ne.Err == strconv.ErrRange {
				return 0, false, fmt.Errorf("%q overflows a 64-bit integer", t)
			}
			return 0, false, fmt.Errorf("%q is not a whole number for an integer column", t)
		}
		return n, true, nil
	case int:
		return int64(t), true, nil
	case int8:
		return int64(t), true, nil
	case int16:
		return int64(t), true, nil
	case int32:
		return int64(t), true, nil
	case int64:
		return t, true, nil
	case uint8:
		return int64(t), true, nil
	case uint16:
		return int64(t), true, nil
	case uint32:
		return int64(t), true, nil
	case uint:
		if uint64(t) > math.MaxInt64 {
			return 0, false, fmt.Errorf("%d overflows a 64-bit integer", t)
		}
		return int64(t), true, nil
	case uint64:
		if t > math.MaxInt64 {
			return 0, false, fmt.Errorf("%d overflows a 64-bit integer", t)
		}
		return int64(t), true, nil
	case float32:
		return sqlServerWholeNumber(float64(t))
	case float64:
		if math.IsNaN(t) || t >= 0x1p63 || t < -0x1p63 {
			return 0, false, fmt.Errorf("%v overflows a 64-bit integer", t)
		}
		return int64(t), true, nil
	}
	return 0, false, nil
}
