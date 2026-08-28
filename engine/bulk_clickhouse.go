package engine

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/dbdialect"
)

// The ClickHouse append writer (ADR-027 phase 2, #382).
//
// clickhouse-go's database/sql face batches through the transaction
// vocabulary: Begin opens a client-side batch buffer, Prepare declares the
// insert's columns, each Exec appends one row, and Commit sends the block.
// The words are a bow to database/sql, not a transaction -- ClickHouse has
// none, and this file must not pretend otherwise:
//
//   - Each committed batch is atomic (an insert block into one partition);
//     nothing larger is. A failure between batches leaves the earlier
//     batches durably written. The run fails rather than reporting what it
//     attempted as success, and SQLGenConfig.CreateTable documents this as
//     the third atomicity story.
//   - CreateDDL executes as a plain statement before the load -- there is
//     no transaction to put it in, so like MySQL a failed load leaves the
//     empty table behind, and unlike MySQL that is not an implicit-commit
//     quirk but the language.
//
// The count is the rows appended across successfully committed batches.
// ClickHouse reports no server-side affected count for inserts; the client
// count is honest because every counted batch was acknowledged, and its
// weakness is the atomicity above: on a mid-write failure the run fails,
// so the count is never reported as a success's.
func appendBatchesToClickHouse(ctx context.Context, uri string, cfg SQLGenConfig, columns []string, next func() (*common.DataSet, error)) (int64, error) {
	for _, id := range append([]string{cfg.Table}, columns...) {
		if err := validateIdentifier(id); err != nil {
			return 0, fmt.Errorf("invalid identifier %q: %w", id, err)
		}
	}

	driver, dsn, err := DetectDriver(uri)
	if err != nil {
		return 0, err
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", driver, err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	d := getDialect("clickhouse")
	if cfg.CreateDDL != "" {
		if _, err := db.ExecContext(ctx, cfg.CreateDDL); err != nil {
			return 0, fmt.Errorf("create %s: %w", cfg.Table, err)
		}
	}

	// Overwrite (ADR-027 phase 3) is TRUNCATE then append, with both of
	// its weaknesses stated rather than smoothed over. There is no
	// transaction, so readers see the empty table the moment the truncate
	// lands -- not the previous contents through MVCC, which is what the
	// other backends give -- and a failure after it leaves whatever
	// batches had committed, not the old rows. And there is no advisory
	// lock to take: ClickHouse has no session-lock primitive, so two
	// concurrent overwrites of one table interleave undefined, exactly the
	// hazard the Postgres and MySQL writers lock against. An operator who
	// schedules overlapping overwrites of one ClickHouse table has no
	// guard here, and the docs say so.
	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))
	if mode == ModeOverwrite || mode == "replace" {
		if _, err := db.ExecContext(ctx, d.clearTable(cfg.Table, cfg.Truncate)); err != nil {
			return 0, fmt.Errorf("clear %s: %w", cfg.Table, err)
		}
	}

	// #392: the target's column types, probed once, so string values can
	// be coerced the way the statement path's literals are parsed by the
	// server. Probed AFTER CreateDDL, so a table this write creates is
	// describable too. A probe that fails degrades to no coercion -- the
	// driver's own refusal then names the mismatch, which is where this
	// path stood before the probe existed.
	coerce := clickhouseCoercers(ctx, db, d, cfg.Table, columns)

	quoted := make([]string, len(columns))
	for i, c := range columns {
		quoted[i] = d.quoteIdent(c)
	}
	// #nosec G202 -- the concatenated parts are the table and column
	// names, each validated by validateIdentifier above and quoted by the
	// dialect; the VALUES travel as batch arguments over the native
	// protocol, never as text.
	insert := "INSERT INTO " + d.quoteIdent(cfg.Table) + " (" + strings.Join(quoted, ", ") + ")"

	var appended int64
	for {
		batch, err := next()
		if err == io.EOF {
			return appended, nil
		}
		if err != nil {
			return 0, err
		}
		if len(batch.Rows) == 0 {
			continue
		}

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return 0, fmt.Errorf("open batch: %w", err)
		}
		stmt, err := tx.PrepareContext(ctx, insert)
		if err != nil {
			_ = tx.Rollback()
			return 0, fmt.Errorf("prepare batch insert: %w", err)
		}
		for _, row := range batch.Rows {
			vals := make([]interface{}, len(columns))
			for i, col := range columns {
				v, err := clickhouseValue(row[col], col, coerce)
				if err != nil {
					_ = stmt.Close()
					_ = tx.Rollback()
					return 0, err
				}
				vals[i] = v
			}
			if _, err := stmt.ExecContext(ctx, vals...); err != nil {
				_ = stmt.Close()
				_ = tx.Rollback()
				return 0, fmt.Errorf("append row to batch: %w", err)
			}
		}
		if err := stmt.Close(); err != nil {
			_ = tx.Rollback()
			return 0, err
		}
		if err := tx.Commit(); err != nil {
			return 0, fmt.Errorf("send batch to %s: %w", cfg.Table, err)
		}
		appended += int64(len(batch.Rows))
	}
}

// clickhouseValue adapts one dataset value to what clickhouse-go's column
// appenders accept. The driver is strict about Go types per column; three
// adaptations happen here:
//
//   - Typed nils flatten to nil, which the driver maps to NULL or refuses
//     per the column's nullability -- refusal being the correct answer for
//     a NULL headed at a non-Nullable column.
//   - time.Time passes through untouched; the driver owns the conversion
//     to DateTime64, which is what keeps the instant an instant.
//   - A string headed at a numeric or boolean column is parsed (#392),
//     because that is what the statement path's rendered literal gets from
//     the server -- a CSV source carries every value as a string, and
//     "same pipeline, different outcome by path" is what the equivalence
//     discipline exists to prevent. A string that does not parse is
//     refused naming the column, the value, and the way out.
//
// Everything else passes through and the driver's own error names the
// column and the offending Go type -- better than any translation here.
func clickhouseValue(v interface{}, col string, coerce map[string]func(string) (interface{}, error)) (interface{}, error) {
	switch t := v.(type) {
	case nil:
		return nil, nil
	case *time.Time:
		if t == nil {
			return nil, nil
		}
		return *t, nil
	case string:
		fn, ok := coerce[col]
		if !ok {
			return v, nil
		}
		out, err := fn(t)
		if err != nil {
			return nil, fmt.Errorf(
				"column %q: cannot convert %q for the column's type: %w -- cast it upstream, "+
					"or make the column String", col, t, err)
		}
		return out, nil
	default:
		return v, nil
	}
}

// clickhouseCoercers probes the target's column types and returns a parser
// per column whose type warrants one. Only the unambiguous classes are
// coerced -- integers, floats, booleans -- where the string's meaning is
// exactly what the server's own literal parsing would produce. Everything
// else (Decimal, DateTime, String itself) passes through: clickhouse-go
// accepts strings for those columns and owns their parsing.
//
// Deliberately no trimming: the server refuses ' 42 ' as an Int64 literal,
// and the equivalence contract is to match the server, not to be kinder
// than it -- a value the statement path would refuse must be refused here
// too, just with a better name.
//
// A probe failure returns nil, and nil coerces nothing: the write proceeds
// and the driver's own error names any mismatch, which is exactly where
// this path stood before #392.
func clickhouseCoercers(ctx context.Context, db *sql.DB, d dialect, table string, columns []string) map[string]func(string) (interface{}, error) {
	// #nosec G202 -- the only concatenated part is the table name, which
	// the caller validated with validateIdentifier before any query, and
	// which the dialect quotes here; no value reaches this string.
	rows, err := db.QueryContext(ctx, "SELECT * FROM "+d.quoteIdent(table)+" LIMIT 0")
	if err != nil {
		return nil
	}
	defer rows.Close()
	types, err := rows.ColumnTypes()
	if err != nil {
		return nil
	}

	reader, ok := chTypeReader()
	if !ok {
		return nil
	}
	out := map[string]func(string) (interface{}, error){}
	for _, ct := range types {
		canonical := reader.CanonicalType(ct.DatabaseTypeName(), 0, 0, false, 0, false, false)
		switch canonical.Class {
		case dbdialect.TypeInt:
			out[ct.Name()] = func(s string) (interface{}, error) {
				n, err := strconv.ParseInt(s, 10, 64)
				if err != nil {
					return nil, err
				}
				return n, nil
			}
		case dbdialect.TypeFloat:
			out[ct.Name()] = func(s string) (interface{}, error) {
				f, err := strconv.ParseFloat(s, 64)
				if err != nil {
					return nil, err
				}
				return f, nil
			}
		case dbdialect.TypeBool:
			out[ct.Name()] = func(s string) (interface{}, error) {
				b, err := strconv.ParseBool(s)
				if err != nil {
					return nil, err
				}
				return b, nil
			}
		}
	}
	return out
}

// chTypeReader fetches the ClickHouse canonical-type reader, in one place
// so the probe cannot silently diverge from the dialect's own vocabulary.
func chTypeReader() (dbdialect.TypeReader, bool) {
	dd, ok := dbdialect.For("clickhouse")
	if !ok {
		return nil, false
	}
	r, ok := dd.(dbdialect.TypeReader)
	return r, ok
}
