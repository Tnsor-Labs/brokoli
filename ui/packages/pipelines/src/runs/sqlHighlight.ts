// A small, dependency-free SQL tokenizer for read-only display. It is not a
// parser: it recognises the three things worth colouring in a recorded
// statement -- keywords, string literals and comments -- and leaves everything
// else (identifiers, numbers, punctuation) as plain code text. A full editor
// per statement would be far heavier, and this renders a long INSERT cheaply.

export type SqlToken = { text: string; cls: '' | 'kw' | 'str' | 'com' }

// Common SQL across the dialects Brokoli targets. Matched whole-word and
// case-insensitively, so it never lights up a substring of an identifier.
const KEYWORDS = [
  'SELECT', 'FROM', 'WHERE', 'AND', 'OR', 'NOT', 'NULL', 'AS', 'DISTINCT', 'INSERT', 'INTO', 'VALUES', 'UPDATE', 'SET',
  'DELETE', 'CREATE', 'TABLE', 'TEMPORARY', 'TEMP', 'IF', 'EXISTS', 'DROP', 'ALTER', 'ADD', 'COLUMN', 'PRIMARY', 'KEY',
  'FOREIGN', 'REFERENCES', 'UNIQUE', 'CONSTRAINT', 'INDEX', 'VIEW', 'GROUP', 'BY', 'ORDER', 'ASC', 'DESC', 'HAVING',
  'LIMIT', 'OFFSET', 'JOIN', 'INNER', 'LEFT', 'RIGHT', 'FULL', 'OUTER', 'CROSS', 'ON', 'USING', 'UNION', 'ALL',
  'INTERSECT', 'EXCEPT', 'CASE', 'WHEN', 'THEN', 'ELSE', 'END', 'IN', 'IS', 'BETWEEN', 'LIKE', 'ILIKE', 'WITH',
  'RETURNING', 'CONFLICT', 'DO', 'NOTHING', 'DEFAULT', 'CAST', 'TRUNCATE', 'BEGIN', 'COMMIT', 'ROLLBACK',
  'INTEGER', 'INT', 'BIGINT', 'SMALLINT', 'REAL', 'FLOAT', 'DOUBLE', 'DECIMAL', 'NUMERIC', 'TEXT', 'VARCHAR', 'CHAR',
  'BOOLEAN', 'BOOL', 'DATE', 'TIMESTAMP', 'TIME', 'BLOB', 'JSON', 'JSONB', 'SERIAL',
]

// Order: a comment or a string wins over a keyword at the same position, so a
// keyword inside a string stays a string. A `''` inside a string is the SQL
// escape for a quote and is consumed as part of the literal.
const TOKEN = new RegExp(`(--[^\\n]*)|('(?:[^']|'')*')|\\b(?:${KEYWORDS.join('|')})\\b`, 'gi')

export function highlightSql(sql: string): SqlToken[] {
  const tokens: SqlToken[] = []
  let last = 0
  for (const m of sql.matchAll(TOKEN)) {
    const at = m.index ?? 0
    if (at > last) tokens.push({ text: sql.slice(last, at), cls: '' })
    last = at + m[0].length
    tokens.push({ text: m[0], cls: m[1] ? 'com' : m[2] ? 'str' : 'kw' })
  }
  if (last < sql.length) tokens.push({ text: sql.slice(last), cls: '' })
  return tokens
}
