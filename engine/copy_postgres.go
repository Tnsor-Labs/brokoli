package engine

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

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
// Only append and overwrite use it. Upsert needs ON CONFLICT, which COPY
// has no equivalent for; staging into a temp table and merging measured
// slower than the statement path at this size (660ms), so it stays as
// it is.

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

// canCopyInsert reports whether a sink write can go through COPY.
func canCopyInsert(cfg SQLGenConfig) bool {
	if copyFastPathDisabled() {
		return false
	}
	if cfg.Dialect != "postgres" {
		return false
	}
	// CreateTable emits DDL that belongs on the statement path, and
	// upsert has no COPY equivalent.
	if cfg.CreateTable {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Mode)) {
	case "", ModeAppend, ModeOverwrite, "replace":
		return true
	default:
		return false
	}
}

// copyRowsToPostgres writes a dataset with COPY, clearing the table
// first for overwrite. The delete and the copy share one transaction, so
// overwrite stays atomic exactly as the statement path made it.
// copyRowsToPostgres writes a materialized dataset. It is the batch-path
// entry point; copyBatchesToPostgres is the streaming one, and both share
// the implementation below so the two cannot drift.
func copyRowsToPostgres(uri string, cfg SQLGenConfig, ds *common.DataSet) (int64, error) {
	sent := false
	return copyBatchesToPostgres(context.Background(), uri, cfg, ds.Columns, func() (*common.DataSet, error) {
		if sent {
			return nil, io.EOF
		}
		sent = true
		return ds, nil
	})
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
	quotedCols := make([]string, len(columns))
	for i, c := range columns {
		quotedCols[i] = d.quoteIdent(c)
	}
	copyStmt := fmt.Sprintf("COPY %s (%s) FROM STDIN", d.quoteIdent(cfg.Table), strings.Join(quotedCols, ", "))

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
			if _, err := tx.Exec(ctx, "DELETE FROM "+d.quoteIdent(cfg.Table)); err != nil {
				return fmt.Errorf("clear table: %w", err)
			}
		}

		tag, err := pgxConn.PgConn().CopyFrom(ctx, copyReader(columns, next), copyStmt)
		if err != nil {
			return fmt.Errorf("copy: %w", err)
		}
		affected = tag.RowsAffected()

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
	default:
		s = fmt.Sprintf("%v", t)
	}

	if !strings.ContainsAny(s, "\\\t\n\r") {
		return s
	}
	r := strings.NewReplacer(`\`, `\\`, "\t", `\t`, "\n", `\n`, "\r", `\r`)
	return r.Replace(s)
}
