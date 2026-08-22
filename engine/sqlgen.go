package engine

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// Write modes for GenerateSQL.
const (
	ModeAppend    = "append"    // add rows to whatever is already in the table
	ModeOverwrite = "overwrite" // clear the table first, then add the rows
	ModeUpsert    = "upsert"    // insert, updating rows that collide on KeyColumns
)

// SQLGenConfig holds settings for SQL generation.
type SQLGenConfig struct {
	Dialect     string   `json:"dialect"`
	Table       string   `json:"table"`
	BatchSize   int      `json:"batch_size"`
	CreateTable bool     `json:"create_table"`
	Mode        string   `json:"mode"`        // append (default), overwrite, upsert
	KeyColumns  []string `json:"key_columns"` // conflict target for upsert

	// EmptyStringAsNull writes "" as NULL instead of an empty literal.
	// Off by default: a database source distinguishes the two already,
	// and collapsing them loses information the source took care to
	// carry. It exists for file sources, where an empty field is
	// genuinely ambiguous and a numeric target column cannot accept ''.
	EmptyStringAsNull bool `json:"empty_string_as_null"`
}

// GenerateSQL produces SQL statements from a DataSet. The statements are
// meant to run as one transaction (ExecuteSQL splits on ";"), so overwrite's
// clear-then-insert is atomic.
func GenerateSQL(cfg SQLGenConfig, ds *common.DataSet) (string, error) {
	if len(ds.Columns) == 0 {
		return "", fmt.Errorf("no columns in dataset")
	}

	if cfg.Table == "" {
		cfg.Table = "data"
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 100
	}
	if cfg.Dialect == "" {
		cfg.Dialect = "generic"
	}

	// Every identifier that will reach quoteIdent -- the table name, every
	// column, and (for upsert) the conflict-target key columns -- is
	// validated up front, in the one place all of them pass through,
	// rather than threading error returns through quoteIdent and every
	// dialect method that calls it (createTable, insertBatch,
	// upsertBatch). quoteIdent wraps an identifier in the dialect's quote
	// character but never escapes an embedded occurrence of it, so an
	// identifier containing that character breaks out of the quoted
	// token into the surrounding SQL text. Column names in particular are
	// not operator-authored config -- they come from ds.Columns, which
	// for a CSV/Excel source is the file's own header row -- so this is a
	// second-order injection: a pipeline builder who wrote a completely
	// safe sink_db config is still exposed if the upstream file's headers
	// aren't. Rejecting outright (not attempting a per-dialect escape) is
	// deliberate: dialect-specific identifier escaping is easy to get
	// subtly wrong, and no legitimate identifier needs a quote character
	// or a statement terminator in it.
	for _, id := range append([]string{cfg.Table}, ds.Columns...) {
		if err := validateIdentifier(id); err != nil {
			return "", fmt.Errorf("invalid identifier %q: %w", id, err)
		}
	}
	for _, id := range cfg.KeyColumns {
		if err := validateIdentifier(id); err != nil {
			return "", fmt.Errorf("invalid key column %q: %w", id, err)
		}
	}

	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))
	switch mode {
	case "", ModeAppend, "replace", ModeOverwrite, ModeUpsert:
	default:
		return "", fmt.Errorf("unsupported write mode %q: expected append, overwrite, or upsert", cfg.Mode)
	}

	d := getDialect(cfg.Dialect)
	d.emptyStringAsNull = cfg.EmptyStringAsNull
	var sb strings.Builder

	if cfg.CreateTable {
		colTypes := inferTypes(ds.Columns, ds.Rows)
		sb.WriteString(d.createTable(cfg.Table, ds.Columns, colTypes))
		sb.WriteString("\n\n")
	}

	// Overwrite clears the table before the inserts, in the same transaction.
	if mode == ModeOverwrite || mode == "replace" {
		sb.WriteString("DELETE FROM " + d.quoteIdent(cfg.Table) + d.terminator + "\n")
	}

	// Generate INSERT (or upsert) statements in batches.
	for i := 0; i < len(ds.Rows); i += cfg.BatchSize {
		end := i + cfg.BatchSize
		if end > len(ds.Rows) {
			end = len(ds.Rows)
		}
		batch := ds.Rows[i:end]
		if mode == ModeUpsert {
			stmt, err := d.upsertBatch(cfg.Table, ds.Columns, cfg.KeyColumns, batch)
			if err != nil {
				return "", err
			}
			sb.WriteString(stmt)
		} else {
			sb.WriteString(d.insertBatch(cfg.Table, ds.Columns, batch))
		}
		sb.WriteString("\n")
	}

	return sb.String(), nil
}

// --- Type inference ---

func inferTypes(columns []string, rows []common.DataRow) map[string]string {
	types := make(map[string]string, len(columns))
	for _, col := range columns {
		types[col] = inferColumnType(col, rows)
	}
	return types
}

func inferColumnType(col string, rows []common.DataRow) string {
	var intCount, floatCount, boolCount, dateCount, total int

	for _, row := range rows {
		val, ok := row[col]
		if !ok || val == nil {
			continue
		}
		total++
		s := fmt.Sprintf("%v", val)
		if s == "" {
			continue
		}

		if _, err := strconv.ParseInt(s, 10, 64); err == nil {
			intCount++
			continue
		}
		if _, err := strconv.ParseFloat(s, 64); err == nil {
			floatCount++
			continue
		}
		lower := strings.ToLower(s)
		if lower == "true" || lower == "false" {
			boolCount++
			continue
		}
		if isDate(s) {
			dateCount++
		}
	}

	if total == 0 {
		return "TEXT"
	}

	threshold := int(float64(total) * 0.8)
	if intCount >= threshold {
		return "INTEGER"
	}
	if floatCount+intCount >= threshold {
		return "FLOAT"
	}
	if boolCount >= threshold {
		return "BOOLEAN"
	}
	if dateCount >= threshold {
		return "TIMESTAMP"
	}
	return "TEXT"
}

var dateFormats = []string{
	time.RFC3339,
	"2006-01-02",
	"2006-01-02 15:04:05",
	"01/02/2006",
	"02-01-2006",
}

func isDate(s string) bool {
	for _, f := range dateFormats {
		if _, err := time.Parse(f, s); err == nil {
			return true
		}
	}
	return false
}

// --- Dialects ---

type dialect struct {
	name       string
	quoteChar  string
	strQuote   string
	terminator string
	boolTrue   string
	boolFalse  string
	typeMap    map[string]string

	// tsLayout is how a time.Time is rendered for this dialect, and
	// tsUTC says the dialect's literal carries no zone — those are
	// converted to UTC first so the instant survives instead of being
	// silently reinterpreted in the session's timezone.
	tsLayout string
	tsUTC    bool

	// emptyStringAsNull mirrors SQLGenConfig.EmptyStringAsNull; set by
	// GenerateSQL rather than getDialect, since it is a per-write choice
	// and not a property of the dialect.
	emptyStringAsNull bool
}

func getDialect(name string) dialect {
	switch strings.ToLower(name) {
	case "postgres", "postgresql":
		return dialect{
			name: "postgres", quoteChar: `"`, strQuote: "'", terminator: ";",
			boolTrue: "TRUE", boolFalse: "FALSE",
			typeMap:  map[string]string{"INTEGER": "INTEGER", "FLOAT": "DOUBLE PRECISION", "BOOLEAN": "BOOLEAN", "TEXT": "TEXT", "TIMESTAMP": "TIMESTAMP"},
			tsLayout: "2006-01-02 15:04:05.999999-07:00", tsUTC: false,
		}
	case "mysql":
		return dialect{
			name: "mysql", quoteChar: "`", strQuote: "'", terminator: ";",
			boolTrue: "TRUE", boolFalse: "FALSE",
			typeMap:  map[string]string{"INTEGER": "INT", "FLOAT": "DOUBLE", "BOOLEAN": "BOOLEAN", "TEXT": "TEXT", "TIMESTAMP": "DATETIME"},
			tsLayout: "2006-01-02 15:04:05.999999", tsUTC: true,
		}
	case "sqlite":
		return dialect{
			name: "sqlite", quoteChar: `"`, strQuote: "'", terminator: ";",
			boolTrue: "1", boolFalse: "0",
			typeMap:  map[string]string{"INTEGER": "INTEGER", "FLOAT": "REAL", "BOOLEAN": "INTEGER", "TEXT": "TEXT", "TIMESTAMP": "TEXT"},
			tsLayout: "2006-01-02T15:04:05.999999Z07:00", tsUTC: false,
		}
	case "sqlserver", "mssql":
		return dialect{
			name: "sqlserver", quoteChar: "[", strQuote: "'", terminator: ";",
			boolTrue: "1", boolFalse: "0",
			typeMap:  map[string]string{"INTEGER": "INT", "FLOAT": "FLOAT", "BOOLEAN": "BIT", "TEXT": "NVARCHAR(MAX)", "TIMESTAMP": "DATETIME2"},
			tsLayout: "2006-01-02 15:04:05.9999999", tsUTC: true,
		}
	default:
		return dialect{
			name: "generic", quoteChar: `"`, strQuote: "'", terminator: ";",
			boolTrue: "TRUE", boolFalse: "FALSE",
			typeMap:  map[string]string{"INTEGER": "INTEGER", "FLOAT": "FLOAT", "BOOLEAN": "BOOLEAN", "TEXT": "TEXT", "TIMESTAMP": "TIMESTAMP"},
			tsLayout: "2006-01-02 15:04:05.999999", tsUTC: true,
		}
	}
}

func (d dialect) quoteIdent(s string) string {
	// A dotted name is schema-qualified ("analytics.daily_revenue") and
	// has to be quoted part by part. Wrapping the whole string in one
	// pair of quotes asks the database for a table whose *name* contains
	// a dot, in the default schema — so a sink pointed at another schema
	// failed with "relation does not exist" while that relation plainly
	// existed. Each part still goes through validateIdentifier upstream,
	// which is what keeps the split safe: no part can carry a quote
	// character or terminator.
	parts := strings.Split(s, ".")
	for i, part := range parts {
		if d.quoteChar == "[" {
			parts[i] = "[" + part + "]"
		} else {
			parts[i] = d.quoteChar + part + d.quoteChar
		}
	}
	return strings.Join(parts, ".")
}

// identifierBreakoutChars are the characters that let an identifier break
// out of quoteIdent's wrapping into the surrounding SQL text: every
// dialect's own quote character (so this list is dialect-independent --
// an identifier destined for one dialect can't smuggle another dialect's
// quote char through either), plus the statement terminator ExecuteSQL
// splits generated SQL text on (engine/database.go), which a naive split
// doesn't understand quoting well enough to skip over even inside an
// otherwise-safely-quoted identifier.
const identifierBreakoutChars = "\"`[];\x00"

// validateIdentifier rejects a table/column name that could break out of
// quoteIdent's wrapping, or that's empty. Called once, up front in
// GenerateSQL, for every identifier before any of it reaches a dialect
// method -- see GenerateSQL's call site for why this is checked there
// instead of inside quoteIdent itself.
func validateIdentifier(s string) error {
	if s == "" {
		return fmt.Errorf("identifier cannot be empty")
	}
	if strings.ContainsAny(s, identifierBreakoutChars) {
		return fmt.Errorf("identifier contains a character that cannot be used in a quoted SQL identifier")
	}
	// A dotted identifier is schema-qualified and quoteIdent will quote
	// each part separately, so every part has to stand on its own: an
	// empty one ("a..b", ".t", "t.") would produce an empty quoted
	// token rather than a name.
	if strings.Contains(s, ".") {
		for _, part := range strings.Split(s, ".") {
			if part == "" {
				return fmt.Errorf("qualified identifier has an empty part")
			}
		}
	}
	return nil
}

// formatValue renders one value as a SQL literal.
//
// The decision is driven by the value's Go type, not by re-parsing its
// rendered text. Guessing from text silently corrupted ordinary data,
// because a string that happens to look like something else was emitted
// as that something else:
//
//	"00123"            -> 00123     a zip code or account number,
//	                                stored as 123
//	"4111111111111111" -> unquoted  a card number, stored as a numeric
//	"1.50"             -> 1.50      stored as 1.5
//	"true"             -> TRUE      a text column handed a boolean,
//	                                which Postgres rejects outright
//	""                 -> NULL      empty and unknown collapsed together
//
// Quoting a string is safe for non-text targets: every dialect here
// coerces a quoted literal into a numeric, boolean or date column. The
// reverse — emitting a bare token into a text column — is what breaks.
func (d dialect) formatValue(v interface{}) string {
	switch t := v.(type) {
	case nil:
		return "NULL"
	case time.Time:
		return d.formatTime(t)
	case *time.Time:
		if t == nil {
			return "NULL"
		}
		return d.formatTime(*t)
	case bool:
		if t {
			return d.boolTrue
		}
		return d.boolFalse
	case []byte:
		return d.quoteString(string(t))
	case string:
		if t == "" && d.emptyStringAsNull {
			return "NULL"
		}
		return d.quoteString(t)
	case int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return fmt.Sprintf("%v", t)
	case json.Number:
		return t.String()
	default:
		// Anything else (a struct a driver handed back, say) is rendered
		// and quoted rather than emitted bare.
		return d.quoteString(fmt.Sprintf("%v", t))
	}
}

// quoteString wraps a value in the dialect's string quote, doubling any
// embedded quote character.
func (d dialect) quoteString(s string) string {
	return d.strQuote + strings.ReplaceAll(s, d.strQuote, d.strQuote+d.strQuote) + d.strQuote
}

// formatTime renders an instant as a quoted literal for this dialect.
func (d dialect) formatTime(t time.Time) string {
	layout := d.tsLayout
	if layout == "" {
		layout = "2006-01-02 15:04:05.999999"
	}
	if d.tsUTC {
		t = t.UTC()
	}
	return d.strQuote + t.Format(layout) + d.strQuote
}

func (d dialect) createTable(table string, columns []string, types map[string]string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("CREATE TABLE %s (\n", d.quoteIdent(table)))
	for i, col := range columns {
		sqlType := d.typeMap[types[col]]
		if sqlType == "" {
			sqlType = "TEXT"
		}
		sb.WriteString(fmt.Sprintf("  %s %s", d.quoteIdent(col), sqlType))
		if i < len(columns)-1 {
			sb.WriteString(",")
		}
		sb.WriteString("\n")
	}
	sb.WriteString(")" + d.terminator)
	return sb.String()
}

func (d dialect) insertBatch(table string, columns []string, rows []common.DataRow) string {
	if len(rows) == 0 {
		return ""
	}

	quotedCols := make([]string, len(columns))
	for i, c := range columns {
		quotedCols[i] = d.quoteIdent(c)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("INSERT INTO %s (%s) VALUES\n",
		d.quoteIdent(table), strings.Join(quotedCols, ", ")))

	for i, row := range rows {
		vals := make([]string, len(columns))
		for j, col := range columns {
			vals[j] = d.formatValue(row[col])
		}
		sb.WriteString("  (" + strings.Join(vals, ", ") + ")")
		if i < len(rows)-1 {
			sb.WriteString(",\n")
		}
	}
	sb.WriteString(d.terminator)
	return sb.String()
}

// upsertBatch builds an INSERT that updates rows colliding on keyCols. The
// conflict clause hangs off a normal multi-row INSERT, so all rows in the
// batch share it. Postgres and SQLite need the explicit key columns
// (ON CONFLICT); MySQL keys off the table's own unique indexes
// (ON DUPLICATE KEY). Other dialects have no portable form and error, so an
// unsupported upsert fails with a name instead of silently inserting.
func (d dialect) upsertBatch(table string, columns, keyCols []string, rows []common.DataRow) (string, error) {
	insert := strings.TrimSuffix(d.insertBatch(table, columns, rows), d.terminator)

	inKeys := make(map[string]bool, len(keyCols))
	for _, k := range keyCols {
		inKeys[k] = true
	}

	switch d.name {
	case "postgres", "sqlite":
		if len(keyCols) == 0 {
			return "", fmt.Errorf("upsert requires key_columns for %s (the conflict target)", d.name)
		}
		quotedKeys := make([]string, len(keyCols))
		for i, k := range keyCols {
			quotedKeys[i] = d.quoteIdent(k)
		}
		var sets []string
		for _, col := range columns {
			if inKeys[col] {
				continue
			}
			sets = append(sets, d.quoteIdent(col)+" = EXCLUDED."+d.quoteIdent(col))
		}
		if len(sets) == 0 {
			// Every column is part of the key — nothing to update.
			return insert + " ON CONFLICT (" + strings.Join(quotedKeys, ", ") + ") DO NOTHING" + d.terminator, nil
		}
		return insert + " ON CONFLICT (" + strings.Join(quotedKeys, ", ") + ") DO UPDATE SET " + strings.Join(sets, ", ") + d.terminator, nil
	case "mysql":
		var sets []string
		for _, col := range columns {
			if inKeys[col] {
				continue
			}
			sets = append(sets, d.quoteIdent(col)+" = VALUES("+d.quoteIdent(col)+")")
		}
		if len(sets) == 0 {
			for _, col := range columns {
				sets = append(sets, d.quoteIdent(col)+" = VALUES("+d.quoteIdent(col)+")")
			}
		}
		return insert + " ON DUPLICATE KEY UPDATE " + strings.Join(sets, ", ") + d.terminator, nil
	default:
		return "", fmt.Errorf("upsert is not supported for dialect %q", d.name)
	}
}
