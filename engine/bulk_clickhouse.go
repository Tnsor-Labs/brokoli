package engine

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
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
				vals[i] = clickhouseValue(row[col])
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
// appenders accept. The driver is strict about Go types per column and does
// its own converting where the pair is sensible (int64 into UInt32, string
// into Decimal); the two adaptations here are for values the engine itself
// produces:
//
//   - json.Number-ish and typed nils flatten to nil, which the driver maps
//     to NULL or refuses per the column's nullability -- refusal being the
//     correct answer for a NULL headed at a non-Nullable column.
//   - time.Time passes through untouched; the driver owns the conversion
//     to DateTime64, which is what keeps the instant an instant.
//
// Everything else passes through and the driver's own error names the
// column and the offending Go type -- better than any translation here.
func clickhouseValue(v interface{}) interface{} {
	switch t := v.(type) {
	case nil:
		return nil
	case *time.Time:
		if t == nil {
			return nil
		}
		return *t
	default:
		return v
	}
}
