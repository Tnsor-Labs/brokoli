package engine

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// The MySQL bulk write path: LOAD DATA LOCAL INFILE fed from an in-process
// reader, the driver's registered-handler mechanism, so the rows travel as
// a data stream instead of being rendered into SQL text for the server to
// parse back.
//
// LOAD DATA is DML -- it does not implicitly commit the way TRUNCATE and
// CREATE TABLE do -- so the overwrite's DELETE, the load, and the commit
// share one transaction and the atomicity contract Phase 2 restored holds
// here too: a failure anywhere leaves the previous contents in place.
//
// The server-side switch local_infile is off by default in MySQL 8. When
// the server refuses, the write degrades to per-batch INSERT statements
// inside the same transaction shape -- still bounded by one batch, still
// atomic, just slower.
//
// Measured at 1M rows (two columns, ~40-byte values, local MySQL 8.4,
// BenchmarkMySQLWritePaths, 2026-08-24):
//
//	LOAD DATA          7.0s   ~143k rows/s   O(batch) memory
//	INSERT fallback   11.5s    ~87k rows/s   O(batch) memory
//	statement path    11.0s    ~91k rows/s   O(dataset) memory + the SQL text
//
// The statement path's number flatters it: it also materializes the whole
// dataset and renders a ~60MB SQL string, which is the memory ceiling this
// path exists to remove. The fallback matching it in wall time while
// holding one batch is the point of the fallback.

// mysqlBulkSeq makes each registered reader name unique: the driver's
// handler registry is process-global, and two concurrent writes must not
// see each other's rows.
var mysqlBulkSeq atomic.Int64

// loadBatchesToMySQL writes rows pulled from next, which returns batches
// until io.EOF. The counterpart of copyBatchesToPostgres.
func loadBatchesToMySQL(ctx context.Context, uri string, cfg SQLGenConfig, columns []string, next func() (*common.DataSet, error)) (int64, error) {
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

	// One pinned session: the advisory lock below is session-scoped, so the
	// lock, the transaction and the load must share a connection.
	conn, err := db.Conn(ctx)
	if err != nil {
		return 0, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Close()

	// The server decides whether LOAD DATA LOCAL is allowed at all. Probing
	// up front keeps the control flow straight: choose a path once, rather
	// than starting a load, failing, and reasoning about whether any rows
	// were consumed before retrying. An unreadable variable is treated as
	// disabled -- the statement fallback is always correct, only slower.
	useLoadData := true
	var localInfile int
	if err := conn.QueryRowContext(ctx, "SELECT @@GLOBAL.local_infile").Scan(&localInfile); err != nil || localInfile != 1 {
		useLoadData = false
	}

	d := getDialect("mysql")
	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			tx.Rollback() //nolint:errcheck // best-effort on the error path
		}
	}()

	if mode == ModeOverwrite || mode == "replace" {
		// Serialize concurrent overwrites of the same table, as the
		// Postgres path does with LOCK TABLE. MySQL's LOCK TABLES
		// implicitly commits and is unusable inside the transaction, so
		// this is GET_LOCK: an advisory session lock that readers never
		// see and that dies with the connection if the worker does.
		// The name is hashed because GET_LOCK caps names at 64 characters.
		lockName := fmt.Sprintf("brokoli_overwrite_%x", sha256.Sum256([]byte(cfg.Table)))[:64]
		var got sql.NullInt64
		if err := tx.QueryRowContext(ctx, "SELECT GET_LOCK(?, 1800)", lockName).Scan(&got); err != nil {
			return 0, fmt.Errorf("acquire overwrite lock: %w", err)
		}
		if !got.Valid || got.Int64 != 1 {
			return 0, fmt.Errorf("overwrite of %s is already in progress elsewhere (advisory lock not acquired)", cfg.Table)
		}
		defer func() {
			// Session-scoped, so release explicitly; conn.Close is the
			// backstop if this errors.
			conn.ExecContext(ctx, "SELECT RELEASE_LOCK(?)", lockName) //nolint:errcheck
		}()
		if _, err := tx.ExecContext(ctx, d.clearTable(cfg.Table, cfg.Truncate)); err != nil {
			return 0, fmt.Errorf("clear table: %w", err)
		}
	}

	var affected int64
	if useLoadData {
		affected, err = execLoadData(ctx, tx, d, cfg.Table, columns, next)
	} else {
		affected, err = execInsertBatches(ctx, tx, d, cfg.Table, columns, next)
	}
	if err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	committed = true
	return affected, nil
}

// execLoadData streams the batches through LOAD DATA LOCAL INFILE. The
// format clause is explicit rather than relying on server defaults, so the
// escape below and the server's parser agree by construction.
func execLoadData(ctx context.Context, tx *sql.Tx, d dialect, table string, columns []string, next func() (*common.DataSet, error)) (int64, error) {
	name := fmt.Sprintf("brokoli-bulk-%d", mysqlBulkSeq.Add(1))
	mysql.RegisterReaderHandler(name, func() io.Reader {
		return mysqlLoadReader(columns, next)
	})
	defer mysql.DeregisterReaderHandler(name)

	quotedCols := make([]string, len(columns))
	for i, c := range columns {
		quotedCols[i] = d.quoteIdent(c)
	}
	stmt := fmt.Sprintf(
		"LOAD DATA LOCAL INFILE 'Reader::%s' INTO TABLE %s CHARACTER SET utf8mb4 FIELDS TERMINATED BY '\\t' ESCAPED BY '\\\\' LINES TERMINATED BY '\\n' (%s)",
		name, d.quoteIdent(table), strings.Join(quotedCols, ", "))

	res, err := tx.ExecContext(ctx, stmt)
	if err != nil {
		return 0, fmt.Errorf("load data: %w", err)
	}
	return res.RowsAffected()
}

// execInsertBatches is the fallback when the server disallows LOAD DATA
// LOCAL: each batch becomes one multi-row INSERT inside the transaction.
// Memory stays bounded by a batch; only the round trips multiply.
func execInsertBatches(ctx context.Context, tx *sql.Tx, d dialect, table string, columns []string, next func() (*common.DataSet, error)) (int64, error) {
	var affected int64
	for {
		batch, err := next()
		if err == io.EOF {
			return affected, nil
		}
		if err != nil {
			return 0, err
		}
		if len(batch.Rows) == 0 {
			continue
		}
		stmt := strings.TrimSuffix(d.insertBatch(table, columns, batch.Rows), d.terminator)
		res, err := tx.ExecContext(ctx, stmt)
		if err != nil {
			return 0, fmt.Errorf("insert batch: %w", err)
		}
		n, _ := res.RowsAffected()
		affected += n
	}
}

// mysqlLoadReader streams rows as LOAD DATA text, the same io.Pipe +
// bufio shape as copyReader and for the same measured reason: handing the
// driver row-sized writes costs a scheduler round trip per row.
func mysqlLoadReader(columns []string, next func() (*common.DataSet, error)) io.Reader {
	pr, pw := io.Pipe()
	go func() {
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
					_, _ = buf.WriteString(mysqlLoadEscape(row[col]))
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

// mysqlLoadEscape renders one value as a LOAD DATA text field, mirroring
// copyEscape: the value's Go type decides, never its printed form, and only
// the characters the format clause treats as structure are escaped.
//
// Two renderings deliberately differ from the statement path's literals:
//
//   - Booleans become 1 and 0, not the keywords. In SQL text, TRUE is a
//     keyword the server evaluates to 1; in a LOAD DATA field it would be
//     the string "TRUE", which coerces to 0 in a numeric column. 1 and 0
//     load as the same values the keywords evaluate to, in every column
//     type.
//   - Times are unquoted, because fields are not quoted here. The layout
//     and the UTC conversion match the mysql dialect's exactly, so both
//     paths store the same instant.
func mysqlLoadEscape(v any) string {
	if v == nil {
		return `\N`
	}
	var s string
	switch t := v.(type) {
	case time.Time:
		s = t.UTC().Format("2006-01-02 15:04:05.999999")
	case *time.Time:
		if t == nil {
			return `\N`
		}
		s = t.UTC().Format("2006-01-02 15:04:05.999999")
	case bool:
		if t {
			return "1"
		}
		return "0"
	case json.Number:
		s = t.String()
	case []byte:
		s = string(t)
	case string:
		s = t
	default:
		s = fmt.Sprintf("%v", t)
	}
	if !strings.ContainsAny(s, "\\\t\n\r\x00") {
		return s
	}
	var sb strings.Builder
	sb.Grow(len(s) + 8)
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			sb.WriteString(`\\`)
		case '\t':
			sb.WriteString(`\t`)
		case '\n':
			sb.WriteString(`\n`)
		case '\r':
			sb.WriteString(`\r`)
		case 0:
			sb.WriteString(`\0`)
		default:
			sb.WriteByte(s[i])
		}
	}
	return sb.String()
}
