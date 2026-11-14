package engine

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/dbdialect"
)

// Reconciling an existing destination table before a migrate writes to it.
//
// create_table only ever ran CREATE TABLE IF NOT EXISTS, so a table that
// already existed was left exactly as it was. Two things then broke a
// managed sync pointed at an existing table: a source that had gained a
// column the destination lacked failed the insert, and an incremental
// (upsert) sync onto a table with no unique key on its key_columns failed
// with a bare "ON CONFLICT ... no unique or primary key" from the driver.
//
// reconcileDestSchema closes both. When create_table is set and the table
// is already there, it adds the columns the source has and the table does
// not (never dropping or retyping one), and for an upsert it makes sure a
// unique key covers key_columns -- adding one where the dialect can, and
// refusing with a clear, actionable error where it cannot. A table it
// cannot introspect is left untouched, so the create/write path behaves
// exactly as before.

// reconcileDestSchema is a no-op unless the destination table already
// exists and can be read; a missing or unintrospectable table is left to
// the create path.
func (r *Runner) reconcileDestSchema(
	ctx context.Context, node models.Node, destURI string, d dialect, table string,
	sourceColumns []string, srcTypes map[string]dbdialect.ColumnType, inferred map[string]string,
	mode string, keyColumns []string,
) error {
	destCols, ok := destTableColumns(ctx, destURI, d, table)
	if !ok {
		return nil
	}

	have := make(map[string]bool, len(destCols))
	for _, c := range destCols {
		have[strings.ToLower(c)] = true
	}
	for _, col := range sourceColumns {
		if have[strings.ToLower(col)] {
			continue
		}
		ct, hasType := srcTypes[col]
		typ := reconcileColumnType(d, ct, hasType, inferred[col])
		stmt := "ALTER TABLE " + d.quoteIdent(table) + " ADD COLUMN " + d.quoteIdent(col) + " " + typ + d.terminator
		if _, err := ExecuteSQLContext(ctx, destURI, stmt); err != nil {
			return fmt.Errorf("add column %q to %s: %w", col, table, err)
		}
		r.log(node.ID, models.LogLevelInfo, "schema: added column %q to %s (the source has it and the table did not)", col, table)
	}

	if strings.EqualFold(strings.TrimSpace(mode), ModeUpsert) && len(keyColumns) > 0 {
		return r.ensureUpsertKey(ctx, node, destURI, d, table, keyColumns)
	}
	return nil
}

// destTableColumns reads an existing table's columns with a zero-row probe
// that every SQL dialect understands. ok=false means the table is not
// there, or the backend would not answer, in which case the caller leaves
// the schema alone.
func destTableColumns(ctx context.Context, uri string, d dialect, table string) (cols []string, ok bool) {
	ds, err := QueryDatabaseContext(ctx, uri, "SELECT * FROM "+d.quoteIdent(table)+" WHERE 1=0")
	if err != nil || ds == nil || len(ds.Columns) == 0 {
		return nil, false
	}
	return ds.Columns, true
}

// reconcileColumnType renders the type of an added column: the source's
// carried type where the destination dialect can render it, the type
// inferred from values otherwise, and TEXT as the last resort -- the same
// order the create path uses, so a column added later matches one the
// table would have been created with.
func reconcileColumnType(d dialect, carried dbdialect.ColumnType, hasCarried bool, inferredType string) string {
	if dd, ok := dbdialect.For(d.name); ok && hasCarried {
		if renderer, ok := dd.(dbdialect.TypeRenderer); ok {
			if rendered, renderable := renderer.DDLType(carried); renderable {
				return rendered
			}
		}
	}
	if t := d.typeMap[inferredType]; t != "" {
		return t
	}
	return "TEXT"
}

// ensureUpsertKey makes sure the destination has a unique key over
// keyColumns, which an upsert needs to merge on. When one is already there
// it does nothing; otherwise it adds one where the dialect allows adding a
// unique key to an existing table, and returns a clear error where it does
// not or where the existing data would not permit it.
func (r *Runner) ensureUpsertKey(ctx context.Context, node models.Node, destURI string, d dialect, table string, keyColumns []string) error {
	covered, known := destHasUniqueKey(ctx, destURI, d, table, keyColumns)
	if known && covered {
		return nil
	}
	if !known {
		// The dialect's keys could not be read. Don't guess: let the write
		// proceed, and the driver reports a missing key if there is one,
		// exactly as before this reconcile existed.
		return nil
	}

	stmt, addable := addUniqueKeyStatement(d, table, keyColumns)
	if !addable {
		return fmt.Errorf(
			"the destination table %s has no primary key or unique index on (%s), which an incremental sync needs to merge on, "+
				"and %s cannot add one to an existing table; add the key yourself, or drop the table so the sync recreates it with the key",
			table, strings.Join(keyColumns, ", "), d.name)
	}
	if _, err := ExecuteSQLContext(ctx, destURI, stmt); err != nil {
		return fmt.Errorf(
			"the destination table %s has no unique key on (%s) and one could not be added (%w); "+
				"this usually means the table already holds rows that repeat those columns -- deduplicate them, or drop the table so the sync recreates it",
			table, strings.Join(keyColumns, ", "), err)
	}
	r.log(node.ID, models.LogLevelInfo, "schema: added a unique key on (%s) to %s so the sync can upsert on it", strings.Join(keyColumns, ", "), table)
	return nil
}

// addUniqueKeyStatement is the statement that gives an existing table a
// unique key over keyColumns, and whether this dialect has one. Postgres
// and MySQL add a named UNIQUE constraint; SQLite cannot alter a primary
// key in but can carry a unique index, which an upsert's ON CONFLICT reads
// the same way. A dialect with no such statement returns addable=false.
func addUniqueKeyStatement(d dialect, table string, keyColumns []string) (stmt string, addable bool) {
	cols := make([]string, len(keyColumns))
	for i, c := range keyColumns {
		cols[i] = d.quoteIdent(c)
	}
	list := strings.Join(cols, ", ")
	name := uniqueKeyName(table, keyColumns)
	switch d.name {
	case "postgres":
		return "ALTER TABLE " + d.quoteIdent(table) + " ADD CONSTRAINT " + d.quoteIdent(name) + " UNIQUE (" + list + ")" + d.terminator, true
	case "mysql":
		return "ALTER TABLE " + d.quoteIdent(table) + " ADD CONSTRAINT " + d.quoteIdent(name) + " UNIQUE (" + list + ")" + d.terminator, true
	case "sqlite":
		return "CREATE UNIQUE INDEX IF NOT EXISTS " + d.quoteIdent(name) + " ON " + d.quoteIdent(table) + " (" + list + ")" + d.terminator, true
	default:
		return "", false
	}
}

// uniqueKeyName is a deterministic, dialect-safe name for a key the
// reconcile adds, so a second run finds its own key rather than adding
// another. It is derived from the table and columns and kept short.
func uniqueKeyName(table string, keyColumns []string) string {
	parts := []string{"brk_uk"}
	parts = append(parts, sanitizeIdentPart(lastSegment(table)))
	for _, c := range keyColumns {
		parts = append(parts, sanitizeIdentPart(c))
	}
	name := strings.Join(parts, "_")
	if len(name) > 60 {
		name = name[:60]
	}
	return name
}

func lastSegment(table string) string {
	seg := strings.Split(table, ".")
	return strings.Trim(seg[len(seg)-1], `"`)
}

func sanitizeIdentPart(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// destHasUniqueKey reports whether a primary key or unique index/constraint
// over exactly keyColumns already exists on the table. known=false means
// the dialect's keys could not be read, so the caller must not assume
// either way.
func destHasUniqueKey(ctx context.Context, uri string, d dialect, table string, keyColumns []string) (covered bool, known bool) {
	var sets [][]string
	var err error
	switch d.name {
	case "sqlite":
		sets, err = sqliteUniqueKeySets(ctx, uri, d, table)
	case "postgres", "mysql":
		sets, err = infoSchemaUniqueKeySets(ctx, uri, table)
	default:
		return false, false
	}
	if err != nil {
		return false, false
	}
	want := lowerColumnSet(keyColumns)
	for _, s := range sets {
		if sameColumnSet(lowerColumnSet(s), want) {
			return true, true
		}
	}
	return false, true
}

// sqliteUniqueKeySets reads SQLite's primary key and unique indexes through
// its PRAGMAs.
func sqliteUniqueKeySets(ctx context.Context, uri string, d dialect, table string) ([][]string, error) {
	t := d.quoteIdent(lastSegment(table))
	var sets [][]string

	ti, err := QueryDatabaseContext(ctx, uri, "PRAGMA table_info("+t+")")
	if err != nil {
		return nil, err
	}
	type pkCol struct {
		name string
		pos  int64
	}
	var pk []pkCol
	for _, row := range ti.Rows {
		if p := asInt64(row["pk"]); p > 0 {
			pk = append(pk, pkCol{asString(row["name"]), p})
		}
	}
	if len(pk) > 0 {
		for i := 0; i < len(pk); i++ {
			for j := i + 1; j < len(pk); j++ {
				if pk[j].pos < pk[i].pos {
					pk[i], pk[j] = pk[j], pk[i]
				}
			}
		}
		cols := make([]string, len(pk))
		for i, c := range pk {
			cols[i] = c.name
		}
		sets = append(sets, cols)
	}

	il, err := QueryDatabaseContext(ctx, uri, "PRAGMA index_list("+t+")")
	if err != nil {
		return sets, nil
	}
	for _, row := range il.Rows {
		if asInt64(row["unique"]) != 1 {
			continue
		}
		ii, err := QueryDatabaseContext(ctx, uri, "PRAGMA index_info("+d.quoteIdent(asString(row["name"]))+")")
		if err != nil {
			continue
		}
		var cols []string
		for _, r2 := range ii.Rows {
			if n := asString(r2["name"]); n != "" {
				cols = append(cols, n)
			}
		}
		if len(cols) > 0 {
			sets = append(sets, cols)
		}
	}
	return sets, nil
}

// infoSchemaUniqueKeySets reads primary-key and unique constraints from
// information_schema, which Postgres and MySQL share. The table name is a
// literal (escaped), not a bound parameter, to avoid their differing
// placeholder syntax. Unique indexes that are not constraints are not
// listed; the reconcile adds a constraint, so one it added is found again.
func infoSchemaUniqueKeySets(ctx context.Context, uri, table string) ([][]string, error) {
	t := lastSegment(table)
	lit := "'" + strings.ReplaceAll(t, "'", "''") + "'"
	q := "SELECT tc.constraint_name AS cname, kcu.column_name AS col " +
		"FROM information_schema.table_constraints tc " +
		"JOIN information_schema.key_column_usage kcu " +
		"ON tc.constraint_name = kcu.constraint_name AND tc.table_name = kcu.table_name " +
		"WHERE tc.table_name = " + lit + " AND tc.constraint_type IN ('PRIMARY KEY', 'UNIQUE') " +
		"ORDER BY kcu.ordinal_position"
	ds, err := QueryDatabaseContext(ctx, uri, q)
	if err != nil {
		return nil, err
	}
	byName := map[string][]string{}
	var order []string
	for _, row := range ds.Rows {
		name := asString(row["cname"])
		col := asString(row["col"])
		if name == "" || col == "" {
			continue
		}
		if _, seen := byName[name]; !seen {
			order = append(order, name)
		}
		byName[name] = append(byName[name], col)
	}
	sets := make([][]string, 0, len(order))
	for _, name := range order {
		sets = append(sets, byName[name])
	}
	return sets, nil
}

func lowerColumnSet(cols []string) map[string]bool {
	out := make(map[string]bool, len(cols))
	for _, c := range cols {
		out[strings.ToLower(strings.TrimSpace(c))] = true
	}
	return out
}

func sameColumnSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func asInt64(v interface{}) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	case []byte:
		n, _ := strconv.ParseInt(strings.TrimSpace(string(x)), 10, 64)
		return n
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return n
	default:
		return 0
	}
}

func asString(v interface{}) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []byte:
		return string(x)
	default:
		return fmt.Sprintf("%v", x)
	}
}
