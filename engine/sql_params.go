package engine

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Run parameters in SQL are bound, never spliced (#774).
//
// A run parameter is chosen by whoever starts the run, and pipelines.run
// does not imply pipelines.edit. Substituting ${param.x} into a query's
// text let a role that may only run a pipeline change the SQL it executes
// with the connection's credentials. So a source query keeps its
// ${param...} references through variable resolution (resolveNodeConfig),
// and they are replaced here, just before the query runs, by the driver's
// own placeholders, with the values passed as arguments. The value never
// becomes SQL text, so no scanning mistake below can turn it into SQL: a
// misread position makes the query fail, it cannot make it run something
// else.
//
// Two forms are accepted:
//
//   - '${param.x}', the whole of a string literal: bound as text.
//   - ${param.x} on its own, outside any literal: bound as a number. A
//     whole number is bound as an integer; a decimal as its exact text,
//     which the database converts; anything else is refused, because a
//     bare reference used to become SQL text, typically a table or
//     column name, which binding cannot express.
//
// Anything else is refused with a message saying what to write instead: a
// reference inside a longer literal ('%${param.x}%') or used as a quoted
// name ("${param.x}"). References in comments are left alone.

// sqlPlaceholderStyle is how a driver spells its positional placeholders.
type sqlPlaceholderStyle int

const (
	placeholderQuestion sqlPlaceholderStyle = iota // ?      mysql, sqlite, clickhouse, snowflake, databricks, bigquery
	placeholderDollar                              // $1     postgres, redshift
	placeholderAtP                                 // @p1    sqlserver
	placeholderColon                               // :1     oracle
)

func (s sqlPlaceholderStyle) render(n int) string {
	switch s {
	case placeholderDollar:
		return "$" + strconv.Itoa(n)
	case placeholderAtP:
		return "@p" + strconv.Itoa(n)
	case placeholderColon:
		return ":" + strconv.Itoa(n)
	default:
		return "?"
	}
}

// sqlParamDialect is what binding needs to know about a database.
type sqlParamDialect struct {
	style sqlPlaceholderStyle
	// doubleQuotedStrings: "..." is a string literal, as in MySQL, Spark
	// SQL and GoogleSQL, rather than a quoted name.
	doubleQuotedStrings bool
}

// sqlParamDialectFor returns the binding rules for a connection URI.
func sqlParamDialectFor(uri string) sqlParamDialect {
	if isBigQueryURI(uri) {
		return sqlParamDialect{style: placeholderQuestion, doubleQuotedStrings: true}
	}
	switch dialectForURI(uri) {
	case "postgres":
		return sqlParamDialect{style: placeholderDollar}
	case "sqlserver":
		return sqlParamDialect{style: placeholderAtP}
	case "oracle":
		return sqlParamDialect{style: placeholderColon}
	case "mysql", "databricks":
		return sqlParamDialect{style: placeholderQuestion, doubleQuotedStrings: true}
	default:
		return sqlParamDialect{style: placeholderQuestion}
	}
}

// hasSQLParamReference reports whether a query names a run parameter.
func hasSQLParamReference(query string) bool {
	return paramReference.MatchString(query)
}

var (
	sqlWholeNumber = regexp.MustCompile(`^-?[0-9]+$`)
	sqlDecimal     = regexp.MustCompile(`^-?[0-9]+\.[0-9]+$`)
)

// sqlParamPositionError names a reference used where it cannot be bound.
type sqlParamPositionError struct {
	ref, where, instead string
}

func (e *sqlParamPositionError) Error() string {
	return fmt.Sprintf("%s is used %s, where a run parameter cannot be bound; %s "+
		"(run parameters are bound, never written into SQL text)", e.ref, e.where, e.instead)
}

// bindSQLParams replaces the ${param...} references in query with the
// dialect's placeholders and returns the values to bind, in order.
// resolve returns a reference's value given its key ("param.x" or
// "param.x|filter"). A query without references comes back unchanged with
// no arguments.
func bindSQLParams(query string, d sqlParamDialect, resolve func(key string) string) (string, []interface{}, error) {
	if !hasSQLParamReference(query) {
		return query, nil, nil
	}
	var out strings.Builder
	var args []interface{}
	bind := func(ref string, quoted bool) error {
		key := ref[2 : len(ref)-1]
		value := resolve(key)
		if quoted {
			args = append(args, value)
		} else {
			switch {
			case sqlWholeNumber.MatchString(value):
				n, err := strconv.ParseInt(value, 10, 64)
				if err != nil {
					return fmt.Errorf("%s: %q is out of range for a whole number", ref, value)
				}
				args = append(args, n)
			case sqlDecimal.MatchString(value):
				args = append(args, value)
			default:
				return &sqlParamPositionError{ref: ref, where: fmt.Sprintf("outside quotes with a value that is not a number (%q)", value),
					instead: fmt.Sprintf("quote it to bind it as text: '%s'", ref)}
			}
		}
		out.WriteString(d.style.render(len(args)))
		return nil
	}

	for i := 0; i < len(query); {
		rest := query[i:]
		switch {
		case strings.HasPrefix(rest, "--"):
			end := strings.IndexByte(rest, '\n')
			if end < 0 {
				end = len(rest)
			}
			out.WriteString(rest[:end])
			i += end
		case strings.HasPrefix(rest, "/*"):
			end := strings.Index(rest[2:], "*/")
			if end < 0 {
				end = len(rest)
			} else {
				end += 4
			}
			out.WriteString(rest[:end])
			i += end
		case rest[0] == '\'':
			// '${param.x}' as the whole literal is bound as text. A
			// one-letter prefix (N'', E'') belongs to the literal and goes
			// with it.
			if ref := wholeLiteralParam(rest, '\''); ref != "" {
				trimLiteralPrefix(&out)
				if err := bind(ref, true); err != nil {
					return "", nil, err
				}
				i += len(ref) + 2
				continue
			}
			end, err := skipQuoted(rest, '\'', "a longer string literal",
				"build the value outside the literal instead, for example CONCAT('%', '${param.x}', '%')")
			if err != nil {
				return "", nil, err
			}
			out.WriteString(rest[:end])
			i += end
		case rest[0] == '"':
			if d.doubleQuotedStrings {
				if ref := wholeLiteralParam(rest, '"'); ref != "" {
					if err := bind(ref, true); err != nil {
						return "", nil, err
					}
					i += len(ref) + 2
					continue
				}
				end, err := skipQuoted(rest, '"', "a longer string literal",
					"build the value outside the literal instead, for example CONCAT('%', '${param.x}', '%')")
				if err != nil {
					return "", nil, err
				}
				out.WriteString(rest[:end])
				i += end
				continue
			}
			end, err := skipQuoted(rest, '"', "as a quoted name",
				"a table or column name cannot be a run parameter; use a fixed name, or a workspace variable (${var.x}) set by an editor")
			if err != nil {
				return "", nil, err
			}
			out.WriteString(rest[:end])
			i += end
		case rest[0] == '`':
			end, err := skipQuoted(rest, '`', "as a quoted name",
				"a table or column name cannot be a run parameter; use a fixed name, or a workspace variable (${var.x}) set by an editor")
			if err != nil {
				return "", nil, err
			}
			out.WriteString(rest[:end])
			i += end
		case strings.HasPrefix(rest, "${param."):
			ref := paramReference.FindString(rest)
			if ref == "" || !strings.HasPrefix(rest, ref) {
				out.WriteByte(rest[0])
				i++
				continue
			}
			if err := bind(ref, false); err != nil {
				return "", nil, err
			}
			i += len(ref)
		default:
			out.WriteByte(rest[0])
			i++
		}
	}
	return out.String(), args, nil
}

// wholeLiteralParam returns the reference when s starts with a literal
// whose entire content is one ${param...} reference, or "".
func wholeLiteralParam(s string, quote byte) string {
	if len(s) < 2 || s[0] != quote {
		return ""
	}
	ref := paramReference.FindString(s[1:])
	if ref == "" || !strings.HasPrefix(s[1:], ref) {
		return ""
	}
	if len(s) < len(ref)+2 || s[len(ref)+1] != quote {
		return ""
	}
	return ref
}

// skipQuoted returns the length of the quoted run at the start of s, a
// doubled quote being an escaped one. A ${param...} reference inside it is
// an error naming where it was found. An unterminated run extends to the
// end; the database reports that, not this.
func skipQuoted(s string, quote byte, where, instead string) (int, error) {
	for i := 1; i < len(s); i++ {
		if s[i] == quote {
			if i+1 < len(s) && s[i+1] == quote {
				i++
				continue
			}
			if ref := paramReference.FindString(s[:i]); ref != "" {
				return 0, &sqlParamPositionError{ref: ref, where: "inside " + where, instead: instead}
			}
			return i + 1, nil
		}
	}
	if ref := paramReference.FindString(s); ref != "" {
		return 0, &sqlParamPositionError{ref: ref, where: "inside " + where, instead: instead}
	}
	return len(s), nil
}

// trimLiteralPrefix drops a one-letter string-literal prefix (N, E, B, X,
// R in some dialects) just written to out, when it is not the end of a
// longer word.
func trimLiteralPrefix(out *strings.Builder) {
	s := out.String()
	if s == "" {
		return
	}
	last := s[len(s)-1]
	if !strings.ContainsRune("NnEe", rune(last)) {
		return
	}
	if len(s) >= 2 && isSQLWordByte(s[len(s)-2]) {
		return
	}
	trimmed := s[:len(s)-1]
	out.Reset()
	out.WriteString(trimmed)
}

func isSQLWordByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// sqlParamPositionErrors reports, for validation, the references a query
// uses where they can never be bound, whatever their values: inside a
// longer single-quoted literal. Quoted names and double-quoted literals
// depend on the database, which a pipeline using conn_id names only at run
// time, so they are checked then.
func sqlParamPositionErrors(query string) []string {
	if !hasSQLParamReference(query) {
		return nil
	}
	// The question style and ANSI double quotes: what every dialect agrees
	// on for single-quoted literals, which is all this reports.
	_, _, err := bindSQLParams(query, sqlParamDialect{doubleQuotedStrings: true}, func(string) string { return "0" })
	var pos *sqlParamPositionError
	if err != nil && errors.As(err, &pos) && strings.HasPrefix(pos.where, "inside a longer") {
		return []string{err.Error()}
	}
	return nil
}

// bindNodeSQL binds a node's query against this run's parameters for the
// database at uri.
func (r *Runner) bindNodeSQL(uri, query string) (string, []interface{}, error) {
	resolve := func(key string) string { return "" }
	if r.varCtx != nil {
		resolve = r.varCtx.resolveKey
	}
	return bindSQLParams(query, sqlParamDialectFor(uri), resolve)
}
