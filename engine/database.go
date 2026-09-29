package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Tnsor-Labs/brokoli/pkg/dbdialect"
	"math"
	"net/url"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/common"

	// Registers "clickhouse" with database/sql (ADR-027 phase 0). No URI
	// scheme routes to it yet -- that is phase 1's DetectDriver mapping --
	// so nothing reaches this driver until the dialect exists; compiling
	// it in is what lets the phase-0 smoke test prove the driver, the
	// container and the env-gate agree before any dialect work starts.
	_ "github.com/ClickHouse/clickhouse-go/v2"
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/microsoft/go-mssqldb"
	_ "github.com/sijms/go-ora/v2"
	_ "modernc.org/sqlite"
)

// dialectForURI maps a connection URI to the SQL dialect name GenerateSQL
// understands, via the same scheme detection DetectDriver uses. Anything
// unrecognized falls back to "generic" — append/overwrite work
// there, and upsert (which has no portable form) errors with a name.
func dialectForURI(uri string) string {
	// Pure scheme detection: the dialect GenerateSQL should target is a
	// property of the URI, not of which drivers this build happens to compile
	// in. Whether the connection can be opened is DetectDriver's business.
	// The claims are the registry's own (#361): each backend states the
	// schemes it speaks for next to the rest of its vocabulary.
	for _, c := range dbdialect.AllURIClaims() {
		if strings.HasPrefix(uri, c.Scheme+"://") {
			return c.Dialect
		}
	}
	// The adapter's recorded leftovers, matching detectDriver's: sqlite
	// filename suffixes, and the schemeless Postgres default. Refused
	// schemes have no registered dialect and fall to generic, exactly as
	// the driver switch used to send them.
	if strings.HasSuffix(uri, ".db") || strings.HasSuffix(uri, ".sqlite") {
		return "sqlite"
	}
	if !strings.Contains(uri, "://") {
		return "postgres"
	}
	return "generic"
}

// DetectDriver returns the Go sql driver name and DSN for a connection URI.
//
// A scheme this recognizes is not the same as a scheme this build can open:
// the connection catalog can offer database types this build has no driver
// for, while pgx, mysql, sqlite, ClickHouse, SQL Server, Oracle, Snowflake
// (engine/snowflake.go) and Databricks (engine/databricks.go) are registered
// here. Naming a driver that was never registered gets database/sql's
// "unknown driver (forgotten import?)", which reads like a build defect
// rather than an unsupported connection type, so check first and say which
// it is.
func DetectDriver(uri string) (string, string, error) {
	if err := refuseNativeDatabase(uri, "database/sql driver detection"); err != nil {
		return "", "", err
	}
	driver, dsn, err := detectDriver(uri)
	if err != nil {
		return "", "", err
	}
	if !driverRegistered(driver) {
		return "", "", fmt.Errorf(
			"connection type %q is not supported by this build: no %q driver is compiled in (available: %s)",
			schemeOf(uri), driver, strings.Join(sql.Drivers(), ", "))
	}
	return driver, dsn, nil
}

// driverRegistered reports whether database/sql can open this driver name.
func driverRegistered(name string) bool {
	for _, d := range sql.Drivers() {
		if d == name {
			return true
		}
	}
	return false
}

// schemeOf names the connection type in an error message without echoing the
// URI, which carries the password.
func schemeOf(uri string) string {
	if i := strings.Index(uri, "://"); i > 0 {
		return uri[:i]
	}
	return "unknown"
}

func detectDriver(uri string) (string, string, error) {
	// Scheme-owned URIs: the registry's own claims (#361). A backend
	// states its schemes, driver, and DSN shape next to the rest of its
	// vocabulary, so adding one means implementing URIClaims, not editing
	// this function.
	for _, c := range dbdialect.AllURIClaims() {
		if strings.HasPrefix(uri, c.Scheme+"://") {
			dsn := uri
			if c.DSN != nil {
				dsn = c.DSN(uri)
			}
			return c.Driver, dsn, nil
		}
	}

	// The adapter's recorded leftovers -- each here because it has no
	// claim to make, not because folding was forgotten:
	switch {
	case strings.HasSuffix(uri, ".db") || strings.HasSuffix(uri, ".sqlite"):
		// A filename, not a scheme: nothing to claim.
		return "sqlite", uri, nil
	default:
		// A scheme nothing claims is refused by that name (#383). It
		// used to fall through to pgx, so bigquery://
		// produced a Postgres driver error about the wrong backend,
		// named confidently -- the failure ADR-024's survey called "a
		// default that guesses". Schemeless strings keep the pgx
		// default deliberately: libpq keyword DSNs and bare
		// host:port/db strings are historically Postgres, and the
		// mapping test pins that.
		if strings.Contains(uri, "://") {
			schemes := make([]string, 0, 12)
			for _, c := range dbdialect.AllURIClaims() {
				schemes = append(schemes, c.Scheme)
			}
			return "", "", fmt.Errorf(
				"connection scheme %q has no driver in this build (supported: %s)",
				schemeOf(uri), strings.Join(schemes, ", "))
		}
		return "pgx", uri, nil
	}
}

func parseJSON(s string, v interface{}) error {
	return json.Unmarshal([]byte(s), v)
}

func refuseNativeDatabase(uri, operation string) error {
	claim, ok := dbdialect.NativeURI(uri)
	if !ok {
		return nil
	}
	name := claim.Scheme
	if claim.Scheme == "bigquery" {
		name = "BigQuery"
	}
	return fmt.Errorf("%s does not support %s in this build", name, operation)
}

// QueryDatabase opens a connection, runs a query, and returns a DataSet.
func QueryDatabase(uri, query string) (*common.DataSet, error) {
	if err := refuseNativeDatabase(uri, "query"); err != nil {
		return nil, err
	}
	driver, dsn, err := DetectDriver(uri)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", driver, RedactDSNError(err, dsn))
	}
	defer db.Close()

	// 5 minute query timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("ping %s: %w", driver, RedactDSNError(err, dsn))
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
	scan, err := newRowScannerFor(driver, rows, columns)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		row, err := scan.next(rows)
		if err != nil {
			return nil, err
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
	if err := refuseNativeDatabase(uri, "SQL execution"); err != nil {
		return 0, err
	}
	if err := refuseDatabricksWrite(uri); err != nil {
		return 0, err
	}
	driver, dsn, err := DetectDriver(uri)
	if err != nil {
		return 0, err
	}

	db, err := sql.Open(driver, dsn)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", driver, RedactDSNError(err, dsn))
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		return 0, fmt.Errorf("ping %s: %w", driver, RedactDSNError(err, dsn))
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

// validateMySQLUpsertKey checks that key_columns names a unique index of the
// target table before an upsert runs.
//
// MySQL's ON DUPLICATE KEY UPDATE merges on whichever unique index the
// inserted row collides with; the statement cannot name one. So key_columns
// is an assertion about the table, and this is where it is checked: the named
// columns must be exactly the column set of some unique index (PRIMARY
// included, order and case ignored, as ON CONFLICT ignores them too).
// Refusing costs the run an error message; not checking lets rows merge on a
// key the user never configured, which rewrites data silently.
//
// The returned list names the table's other unique indexes, if any. They are
// not an error -- the configured key is real -- but a row can still collide
// on one of them and merge there instead, so the caller surfaces them as a
// warning rather than this function pretending the check makes that
// impossible.
func validateMySQLUpsertKey(ctx context.Context, uri, table string, keyCols []string) ([]string, error) {
	if len(keyCols) == 0 {
		return nil, fmt.Errorf("upsert requires key_columns for mysql (the unique index the merge keys on)")
	}
	driver, dsn, err := DetectDriver(uri)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", driver, RedactDSNError(err, dsn))
	}
	defer db.Close()

	// The table may arrive schema-qualified; otherwise it lives in the
	// connection's default schema.
	schema, tbl := "", table
	if i := strings.IndexByte(table, '.'); i >= 0 {
		schema, tbl = table[:i], table[i+1:]
	}
	query := `SELECT index_name, column_name
		FROM information_schema.statistics
		WHERE table_schema = DATABASE() AND table_name = ? AND non_unique = 0
		ORDER BY index_name, seq_in_index`
	args := []interface{}{tbl}
	if schema != "" {
		query = strings.Replace(query, "DATABASE()", "?", 1)
		args = []interface{}{schema, tbl}
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("inspect unique indexes on %s: %w", table, err)
	}
	defer rows.Close()

	indexCols := map[string][]string{}
	var indexOrder []string
	for rows.Next() {
		var idx, col string
		if err := rows.Scan(&idx, &col); err != nil {
			return nil, err
		}
		if _, seen := indexCols[idx]; !seen {
			indexOrder = append(indexOrder, idx)
		}
		indexCols[idx] = append(indexCols[idx], col)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(indexCols) == 0 {
		return nil, fmt.Errorf(
			"upsert into %s: the table has no unique index, so MySQL's ON DUPLICATE KEY UPDATE can never merge -- every row would insert",
			table)
	}

	want := map[string]bool{}
	for _, k := range keyCols {
		want[strings.ToLower(k)] = true
	}
	sameSet := func(cols []string) bool {
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

	matched := ""
	var others []string
	for _, idx := range indexOrder {
		if matched == "" && sameSet(indexCols[idx]) {
			matched = idx
			continue
		}
		others = append(others, fmt.Sprintf("%s (%s)", idx, strings.Join(indexCols[idx], ", ")))
	}
	if matched == "" {
		return nil, fmt.Errorf(
			"upsert into %s: key_columns (%s) does not match any unique index; the table's unique indexes are: %s",
			table, strings.Join(keyCols, ", "), strings.Join(others, "; "))
	}
	return others, nil
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

// rowScanner turns *sql.Rows into DataRows. It exists so the materializing
// and streaming query paths cannot drift apart on how a value is converted
// — a difference there would mean the same query produced different data
// depending on which path the planner chose, which is the kind of bug that
// is invisible until someone diffs two loads of the same table.
type rowScanner struct {
	columns []string
	values  []interface{}
	ptrs    []interface{}
	// decode, when set, converts a column's scanned value; nil entries
	// keep the value as scanned.
	decode []func(interface{}) interface{}
}

// newRowScannerFor is newRowScanner plus the conversions one driver needs.
//
// go-ora scans every Oracle NUMBER -- which is how Oracle stores integers
// too -- as its decimal text, so without this an ID column arrives as the
// string "42" and compares, sorts and loads as text. Each NUMBER value is
// decoded as exactly as its text allows: see decodeOracleNumber.
func newRowScannerFor(driver string, rows *sql.Rows, columns []string) (*rowScanner, error) {
	s := newRowScanner(columns)
	if driver != "oracle" {
		return s, nil
	}
	types, err := rows.ColumnTypes()
	if err != nil {
		return nil, fmt.Errorf("column types: %w", err)
	}
	for i, ct := range types {
		if i < len(columns) && ct.DatabaseTypeName() == "NUMBER" {
			if s.decode == nil {
				s.decode = make([]func(interface{}) interface{}, len(columns))
			}
			s.decode[i] = decodeOracleNumber
		}
	}
	return s, nil
}

// decodeOracleNumber turns the decimal text of an Oracle NUMBER into the
// value it is, without losing anything:
//
//   - a whole number that fits in 64 bits becomes int64, exactly, all the
//     way to 9223372036854775807 (a float64 would round anything past 2^53);
//   - a number with at most 15 significant digits becomes float64, which
//     round-trips any 15-digit decimal to the same text;
//   - anything longer (NUMBER holds 38 digits) stays text, the one form that
//     keeps every digit.
func decodeOracleNumber(v interface{}) interface{} {
	text, ok := v.(string)
	if !ok {
		return v
	}
	if n, err := strconv.ParseInt(text, 10, 64); err == nil {
		return n
	}
	if significantDigits(text) > 15 {
		return text
	}
	if f, err := strconv.ParseFloat(text, 64); err == nil {
		return f
	}
	return text
}

// significantDigits counts the significant digits of a decimal's text,
// ignoring sign, point, exponent, and leading zeros -- and trailing zeros
// after the point, which carry no value. It errs high, never low.
func significantDigits(text string) int {
	mantissa := strings.TrimLeft(text, "+-")
	if i := strings.IndexAny(mantissa, "eE"); i >= 0 {
		mantissa = mantissa[:i]
	}
	if strings.Contains(mantissa, ".") {
		mantissa = strings.TrimRight(mantissa, "0")
	}
	digits := strings.TrimLeft(strings.ReplaceAll(mantissa, ".", ""), "0")
	return len(digits)
}

// RedactDSNError removes the password in dsn from err's text. Some drivers
// quote the whole DSN when they cannot parse it -- go-ora reports a bad host
// as `parse "oracle://user:secret@bad host:1521/x": invalid character` --
// and the error reaches the run log. Both the URL-encoded and the decoded
// password are removed, and dsn does not have to parse: a DSN that fails to
// parse is exactly the case that leaks.
func RedactDSNError(err error, dsn string) error {
	if err == nil {
		return nil
	}
	rest := dsn
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	at := strings.LastIndex(rest, "@")
	if at < 0 {
		return err
	}
	colon := strings.IndexByte(rest[:at], ':')
	if colon < 0 || colon == at-1 {
		return err
	}
	encoded := rest[colon+1 : at]
	msg := err.Error()
	redacted := strings.ReplaceAll(msg, encoded, "xxxxx")
	if decoded, derr := url.PathUnescape(encoded); derr == nil && decoded != "" {
		redacted = strings.ReplaceAll(redacted, decoded, "xxxxx")
	}
	if redacted == msg {
		return err
	}
	return errors.New(redacted)
}

func newRowScanner(columns []string) *rowScanner {
	s := &rowScanner{
		columns: columns,
		values:  make([]interface{}, len(columns)),
		ptrs:    make([]interface{}, len(columns)),
	}
	for i := range s.values {
		s.ptrs[i] = &s.values[i]
	}
	return s
}

func (s *rowScanner) next(rows *sql.Rows) (common.DataRow, error) {
	if err := rows.Scan(s.ptrs...); err != nil {
		return nil, fmt.Errorf("scan row: %w", err)
	}
	row := make(common.DataRow, len(s.columns))
	for i, col := range s.columns {
		v := s.values[i]
		// Convert []byte to string for readability
		if b, ok := v.([]byte); ok {
			v = string(b)
		}
		if s.decode != nil && s.decode[i] != nil {
			v = s.decode[i](v)
		}
		row[col] = v
	}
	return row, nil
}

// StreamQueryDatabase runs a query and hands the rows to emit in batches,
// never holding more than one batch.
//
// This is the counterpart of QueryDatabase for results that do not fit in
// memory. QueryDatabase has to guess a ceiling and refuse anything above it
// (see datasetMemoryBudget) because the whole result becomes one slice;
// here there is no ceiling to enforce, since the resident set is one batch
// regardless of how many rows the query returns. That is the point:
// dataset size stops being bounded by worker memory.
//
// emit must not retain the batch after returning — the rows behind it are
// reused. ctx governs the whole scan, so a cancelled run stops mid-result
// instead of reading to the end of a large table first.
func StreamQueryDatabase(ctx context.Context, uri, query string, batchSize int, emit func(*common.DataSet) error) ([]string, int64, error) {
	if err := refuseNativeDatabase(uri, "streaming query"); err != nil {
		return nil, 0, err
	}
	if batchSize <= 0 {
		batchSize = streamBatchRows
	}
	driver, dsn, err := DetectDriver(uri)
	if err != nil {
		return nil, 0, err
	}

	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, 0, fmt.Errorf("open %s: %w", driver, RedactDSNError(err, dsn))
	}
	defer db.Close()

	if err := db.PingContext(ctx); err != nil {
		return nil, 0, fmt.Errorf("ping %s: %w", driver, RedactDSNError(err, dsn))
	}

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, 0, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, 0, fmt.Errorf("columns: %w", err)
	}

	scan, err := newRowScannerFor(driver, rows, columns)
	if err != nil {
		return nil, 0, err
	}
	batch := &common.DataSet{Columns: columns, Rows: make([]common.DataRow, 0, batchSize)}
	total := int64(0)
	for rows.Next() {
		// Checked per row rather than per batch: a cancelled run should
		// stop at the next row, not at the next thousand.
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		row, err := scan.next(rows)
		if err != nil {
			return nil, 0, err
		}
		batch.Rows = append(batch.Rows, row)
		if len(batch.Rows) >= batchSize {
			total += int64(len(batch.Rows))
			if err := emit(batch); err != nil {
				return nil, 0, err
			}
			batch.Rows = batch.Rows[:0]
		}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("rows iteration: %w", err)
	}
	if len(batch.Rows) > 0 {
		total += int64(len(batch.Rows))
		if err := emit(batch); err != nil {
			return nil, 0, err
		}
	}
	return columns, total, nil
}

// refuseUnearnedWrite refuses a write shape its backend has not earned
// (ADR-027, ADR-024's rule that a capability is claimed only with proof
// behind it).
//
// ClickHouse earned append in phase 2 -- the native-batch writer with the
// equivalence corpus behind it. Overwrite waits for phase 3, where its
// weaker semantics (TRUNCATE with no transaction to roll back into) are
// documented and observed by tests rather than discovered. Upsert is
// refused for good: ClickHouse has no synchronous merge, and mapping
// mode: upsert onto ReplacingMergeTree would deduplicate eventually --
// readers seeing duplicates until an unscheduled merge -- which is not
// what upsert means on the other backends, so the equivalence test such a
// claim would need can never pass. ReplacingMergeTree remains available to
// users who want eventual dedup and know it, by creating the table
// themselves and appending.
//
// Snowflake has earned no write at all yet, and every mode is refused by
// name (errSnowflakeWrite says why).
//
// Oracle has earned reads only, so every write to it is refused by name,
// whatever the mode. The shared statement writer would produce SQL Oracle
// rejects or misreads: multi-row VALUES (only 23ai accepts it), TEXT and
// BOOLEAN columns (only 23ai has BOOLEAN), and unquoted-versus-quoted case
// folding that sends a quoted lowercase name to a different table than the
// one an Oracle user sees. Overwrite would be worse than refused: TRUNCATE
// commits in Oracle, so a failed load would leave the table emptied.
func refuseUnearnedWrite(uri, mode string) error {
	if err := refuseDatabricksWrite(uri); err != nil {
		return err
	}
	switch dialectForURI(uri) {
	case "clickhouse":
	case "snowflake":
		return errSnowflakeWrite
	case "oracle":
		return fmt.Errorf("Oracle connections are read-only in this build: " +
			"source_db and migrate sources can read from Oracle, but sink_db and migrate cannot write to it yet")
	default:
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", ModeAppend, ModeOverwrite, "replace":
		return nil
	case ModeUpsert:
		return fmt.Errorf(
			"ClickHouse has no upsert: there is no synchronous merge to map mode: upsert onto -- " +
				"ReplacingMergeTree deduplicates eventually, at merge time, which is a different promise. " +
				"Append instead, into a ReplacingMergeTree table you create, if eventual dedup is what you want")
	default:
		return fmt.Errorf("ClickHouse write mode %q is not supported", mode)
	}
}
