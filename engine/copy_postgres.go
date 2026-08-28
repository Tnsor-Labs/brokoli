package engine

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// Writing rows to Postgres with COPY instead of INSERT statements.
//
// The statement path renders every value into SQL text: 25,000 rows of
// five columns became 1.9 MB of literals that had to be shipped and then
// parsed a statement at a time. COPY carries the same rows as data
// rather than as SQL, and the server absorbs them in one command.
//
// The TEXT format is the one used here, not the binary one, because the
// server parses a text field exactly as it parses a literal — so a
// string "12.50" still lands in a numeric column and
// "2026-08-23T10:00:00Z" still lands in a timestamptz, which is what
// every file and API source produces. Binary COPY refuses those
// outright: "unable to encode ... into binary format for timestamptz".
//
// Reading the destination types from pg_attribute and coercing values in
// Go first would make binary COPY viable, and it was measured rather
// than assumed. Into a real table — primary key, numeric, timestamptz —
// it loses:
//
//	COPY, text format                        155ms   <- this
//	catalog lookup                             7ms
//	coercing 125k values in Go                29ms
//	COPY, binary format                      204ms
//	                          typed total    240ms
//
// Binary wins only on unindexed tables of already-typed values; once a
// btree and a numeric column are involved the server spends its time on
// index maintenance and WAL, not on parsing, and pgx's binary encoding
// of numeric costs more than handing Postgres the string. So the typed
// path would buy a hand-written re-implementation of Postgres's own
// text parsing — every timestamp format, numeric precision, array, enum
// — for a 1.5x slowdown. Postgres's parser is the reference
// implementation and text COPY gets it for free.
//
// End to end in the cluster, per sink node:
//
//	           statement path      COPY
//	 25k rows          816ms      366ms   2.2x
//	100k rows         2877ms      832ms   3.5x
//	                34.8k r/s   120k r/s
//
// Append and overwrite COPY straight into the target. Upsert stages
// (#377): COPY into a session temp table, then one merge statement. An
// earlier note here rejected staging as "measured slower (660ms)" -- that
// number was against COPY-append at 25k rows (190ms), a comparison staging
// can only lose, and was never made against the statement-path upsert it
// would actually replace (~1s at that size). BenchmarkUpsertWrite holds
// the honest comparison.

// copyFastPathDisabled lets an operator fall back to the statement path
// without redeploying a different build, in case a workload meets
// something this path handles differently.
// copyStreamBufferSize is the chunk size handed across the io.Pipe.
const copyStreamBufferSize = 256 << 10

func copyFastPathDisabled() bool {
	v := os.Getenv("BROKOLI_SINK_COPY")
	if v == "" {
		return false
	}
	off, err := strconv.ParseBool(v)
	return err == nil && !off
}

// copyBatchesToPostgres writes rows pulled from next, which returns batches
// until io.EOF. Nothing larger than one batch is ever held, so this is what
// lets a sink write a table that does not fit in worker memory.
func copyBatchesToPostgres(ctx context.Context, uri string, cfg SQLGenConfig, columns []string, next func() (*common.DataSet, error)) (int64, error) {
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

	conn, err := db.Conn(ctx)
	if err != nil {
		return 0, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Close()

	d := getDialect("postgres")
	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))
	upsert := mode == ModeUpsert
	if upsert {
		for _, k := range cfg.KeyColumns {
			if err := validateIdentifier(k); err != nil {
				return 0, fmt.Errorf("invalid key column %q: %w", k, err)
			}
		}
	}
	quotedCols := make([]string, len(columns))
	for i, c := range columns {
		quotedCols[i] = d.quoteIdent(c)
	}
	// An upsert stages: COPY lands in the session temp table and one merge
	// moves the rows on (#377). Append and overwrite COPY straight in.
	loadTarget := cfg.Table
	if upsert {
		loadTarget = upsertStageName
	}
	copyStmt := fmt.Sprintf("COPY %s (%s) FROM STDIN", d.quoteIdent(loadTarget), strings.Join(quotedCols, ", "))

	var affected int64
	rawErr := conn.Raw(func(dc any) error {
		pc, ok := dc.(*stdlib.Conn)
		if !ok {
			return fmt.Errorf("connection is not a pgx connection")
		}
		pgxConn := pc.Conn()

		tx, err := pgxConn.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin: %w", err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck // no-op once committed

		// #376: creating the table happens here, inside the load's own
		// transaction, rather than as a separate statement before it. That
		// is what lets a create-and-load use COPY while keeping the
		// property the statement path had -- a failed load rolls the
		// creation back too, so a failure leaves nothing behind.
		if cfg.CreateDDL != "" {
			if _, err := tx.Exec(ctx, cfg.CreateDDL); err != nil {
				return fmt.Errorf("create %s: %w", cfg.Table, err)
			}
		}

		if mode == ModeOverwrite || mode == "replace" {
			// Serialize concurrent overwrites of the same table.
			//
			// An overwrite says "this table's contents become exactly
			// these rows", and two of those running at once have no
			// defined outcome. Without a lock they interleave and fail in
			// two different ways, both observed on the lab cluster with
			// four pipelines writing one table:
			//
			//   ERROR: deadlock detected (SQLSTATE 40P01)
			//   ERROR: duplicate key value violates unique constraint
			//
			// the second because one transaction's DELETE cannot see the
			// other's uncommitted rows, so both insert the same keys.
			// Neither is a problem the pipeline author can fix, and both
			// present as a hard run failure.
			//
			// EXCLUSIVE rather than ACCESS EXCLUSIVE: writers wait for
			// each other, readers keep seeing the previous contents
			// through MVCC until the winner commits, which is what an
			// overwrite should look like from outside. The lock is
			// released with the transaction.
			if _, err := tx.Exec(ctx, "LOCK TABLE "+d.quoteIdent(cfg.Table)+" IN EXCLUSIVE MODE"); err != nil {
				return fmt.Errorf("lock table for overwrite: %w", err)
			}
			if _, err := tx.Exec(ctx, d.clearTable(cfg.Table, cfg.Truncate)); err != nil {
				return fmt.Errorf("clear table: %w", err)
			}
		}

		if upsert {
			// The same EXCLUSIVE lock the overwrite branch takes, for a
			// harder reason: the merge below is two statements, and a
			// concurrent insert between them would turn the anti-join's
			// "not present" answer stale and fail on a duplicate key.
			// Writers wait for each other; readers keep reading through
			// MVCC. Concurrent upserts of one table serialize, which the
			// statement path did not force -- traded for the merge form
			// that is 3-6x faster than ON CONFLICT (see upsertMergeSQL).
			if _, err := tx.Exec(ctx, "LOCK TABLE "+d.quoteIdent(cfg.Table)+" IN EXCLUSIVE MODE"); err != nil {
				return fmt.Errorf("lock table for upsert: %w", err)
			}
			// The merge updates whatever the key columns match, so they
			// must be a unique index -- an UPDATE joined on a non-unique
			// key would touch every matching row where the statement path
			// refuses. Checked here by name, before any rows move, the
			// way validateMySQLUpsertKey does for MySQL.
			if err := validatePostgresUpsertKey(ctx, tx, cfg.Table, cfg.KeyColumns); err != nil {
				return err
			}
			// The stage lives and dies with this transaction (ON COMMIT
			// DROP), so a failed load or merge leaves nothing behind --
			// neither staged rows nor, with CreateDDL, a half-made target.
			for _, ddl := range d.upsertStageDDL(cfg.Table) {
				if _, err := tx.Exec(ctx, ddl); err != nil {
					return fmt.Errorf("create upsert stage for %s: %w", cfg.Table, err)
				}
			}
		}

		tag, err := pgxConn.PgConn().CopyFrom(ctx, copyReader(columns, next), copyStmt)
		if err != nil {
			return fmt.Errorf("copy: %w", err)
		}
		affected = tag.RowsAffected()

		if upsert {
			// Last-write-wins: drop every staged row that shares its key
			// with a later one, then merge what is left -- one UPDATE for
			// the rows that exist, one anti-join INSERT for the rest.
			if _, err := tx.Exec(ctx, d.upsertDedupSQL(cfg.KeyColumns)); err != nil {
				return fmt.Errorf("dedup upsert stage for %s: %w", cfg.Table, err)
			}
			merge, err := d.upsertMergeSQL(cfg.Table, columns, cfg.KeyColumns)
			if err != nil {
				return err
			}
			// The merge's count is the honest one: distinct rows written
			// to the target. The COPY count above includes staged rows a
			// later duplicate superseded.
			affected = 0
			for _, stmt := range merge {
				tag, err := tx.Exec(ctx, stmt)
				if err != nil {
					return fmt.Errorf("merge upsert stage into %s: %w", cfg.Table, err)
				}
				affected += tag.RowsAffected()
			}
		}

		return tx.Commit(ctx)
	})
	if rawErr != nil {
		return 0, rawErr
	}
	return affected, nil
}

// copyReader streams the dataset in COPY's text format so the rows are
// not all materialised as one string on top of the dataset already in
// memory.
// copyReader streams rows pulled from next as COPY text, so they never
// exist as one big buffer — a 10M-row write costs the same memory as a
// 10-row one, whether next yields one materialized dataset or a thousand
// batches read off the blob store.
//
// The bufio.Writer is not an optimisation detail, it is the difference
// between a fast path and a slow one. io.Pipe hands every Write directly
// to the reader goroutine and blocks until it is consumed, so writing
// row-by-row costs one scheduler round-trip per row: 25k rows spent
// ~490ms in handoffs alone, which swamped the COPY it was meant to
// accelerate. Batching into 256 KiB chunks turns 25k handoffs into ~8.
func copyReader(columns []string, next func() (*common.DataSet, error)) io.Reader {
	pr, pw := io.Pipe()
	go func() {
		// bufio.Writer keeps the first write error and returns it from
		// every later call including Flush, so the per-field writes are
		// deliberately unchecked and the row terminator and Flush carry
		// the check for the whole row.
		buf := bufio.NewWriterSize(pw, copyStreamBufferSize)
		for {
			batch, err := next()
			if err == io.EOF {
				break
			}
			if err != nil {
				pw.CloseWithError(err)
				return
			}
			for _, row := range batch.Rows {
				for i, col := range columns {
					if i > 0 {
						_ = buf.WriteByte('\t')
					}
					_, _ = buf.WriteString(copyEscape(row[col]))
				}
				if err := buf.WriteByte('\n'); err != nil {
					pw.CloseWithError(err)
					return
				}
			}
		}
		if err := buf.Flush(); err != nil {
			pw.CloseWithError(err)
			return
		}
		_ = pw.Close()
	}()
	return pr
}

// copyEscape renders one value as a COPY text field.
//
// The type switch mirrors formatValue's: the value's Go type decides,
// never its printed form. Only the four characters COPY treats as
// structure need escaping — everything else, including quotes, travels
// as-is, because this is data on a copy stream and not SQL text.
func copyEscape(v any) string {
	if v == nil {
		return `\N`
	}
	var s string
	switch t := v.(type) {
	case time.Time:
		s = t.Format("2006-01-02 15:04:05.999999-07:00")
	case *time.Time:
		if t == nil {
			return `\N`
		}
		s = t.Format("2006-01-02 15:04:05.999999-07:00")
	case string:
		s = t
	case []byte:
		s = string(t)
	case float64:
		// Never %v a float here. Go renders float64(1000000) as "1e+06",
		// which Postgres rejects for an integer column and, worse, accepts
		// for a text one (#334). Decoding normalises integers to int64 so
		// this should rarely be reached, but a renderer that can silently
		// change a value is not something to leave in place on the strength
		// of "should".
		if t == math.Trunc(t) && math.Abs(t) < 1e15 {
			s = strconv.FormatFloat(t, 'f', -1, 64)
		} else {
			s = strconv.FormatFloat(t, 'g', -1, 64)
		}
	default:
		s = fmt.Sprintf("%v", t)
	}

	if !strings.ContainsAny(s, "\\\t\n\r") {
		return s
	}
	r := strings.NewReplacer(`\`, `\\`, "\t", `\t`, "\n", `\n`, "\r", `\r`)
	return r.Replace(s)
}

// validatePostgresUpsertKey checks that key_columns name a unique index on
// the target, the way validateMySQLUpsertKey does for MySQL -- and for the
// same underlying reason stated differently. MySQL merges on any unique
// index regardless of key_columns, so there the check keeps configuration
// honest. Here the staged merge UPDATEs whatever the key columns match, so
// a non-unique key would silently update every matching row where the
// statement path's ON CONFLICT refuses with "no unique or exclusion
// constraint". The check turns that into a named refusal before any rows
// move.
//
// Partial unique indexes (WHERE clauses) do not count: they only guarantee
// uniqueness for the rows they cover, which is not the guarantee a merge
// needs. Expression-index entries resolve to no column name and disqualify
// their index the same way.
func validatePostgresUpsertKey(ctx context.Context, tx pgx.Tx, table string, keyCols []string) error {
	if len(keyCols) == 0 {
		return fmt.Errorf("upsert requires key_columns for postgres (the merge key)")
	}

	rows, err := tx.Query(ctx, `
		SELECT i.relname, a.attname
		FROM pg_index x
		JOIN pg_class i ON i.oid = x.indexrelid
		JOIN pg_attribute a ON a.attrelid = x.indrelid AND a.attnum = ANY (x.indkey)
		WHERE x.indrelid = to_regclass($1)
		  AND x.indisunique
		  AND x.indpred IS NULL
		ORDER BY i.relname, a.attnum`, table)
	if err != nil {
		return fmt.Errorf("read unique indexes of %s: %w", table, err)
	}
	defer rows.Close()

	indexes := map[string][]string{}
	var order []string
	for rows.Next() {
		var index, column string
		if err := rows.Scan(&index, &column); err != nil {
			return err
		}
		if _, seen := indexes[index]; !seen {
			order = append(order, index)
		}
		indexes[index] = append(indexes[index], column)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(indexes) == 0 {
		return fmt.Errorf(
			"cannot upsert into %s: the table has no unique index, so there is nothing for the merge to key on",
			table)
	}

	want := make(map[string]bool, len(keyCols))
	for _, k := range keyCols {
		want[strings.ToLower(k)] = true
	}
	matches := func(cols []string) bool {
		if len(cols) != len(want) {
			return false
		}
		for _, c := range cols {
			if !want[strings.ToLower(c)] {
				return false
			}
		}
		return true
	}

	var others []string
	for _, name := range order {
		if matches(indexes[name]) {
			return nil
		}
		others = append(others, fmt.Sprintf("%s (%s)", name, strings.Join(indexes[name], ", ")))
	}
	return fmt.Errorf(
		"key_columns (%s) does not match any unique index on %s; the table's unique indexes are: %s",
		strings.Join(keyCols, ", "), table, strings.Join(others, "; "))
}
