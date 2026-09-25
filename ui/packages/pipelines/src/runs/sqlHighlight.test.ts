import { describe, expect, it } from 'vitest'
import { highlightSql } from './sqlHighlight'

// Reassemble the tokens to prove nothing is dropped or duplicated.
const roundtrip = (sql: string) => highlightSql(sql).map((t) => t.text).join('')
const classOf = (sql: string, needle: string) => highlightSql(sql).find((t) => t.text === needle)?.cls

describe('highlightSql', () => {
  it('preserves the exact text', () => {
    const sql = "SELECT id FROM orders WHERE day = '2024-03-13' -- note"
    expect(roundtrip(sql)).toBe(sql)
  })

  it('classifies keywords case-insensitively', () => {
    expect(classOf('select 1', 'select')).toBe('kw')
    expect(classOf('SELECT 1', 'SELECT')).toBe('kw')
  })

  it('classifies strings and comments', () => {
    expect(classOf("x = 'abc'", "'abc'")).toBe('str')
    expect(classOf('x -- trailing', '-- trailing')).toBe('com')
  })

  it('does not light a keyword inside a string or identifier', () => {
    expect(classOf("'from here'", "'from here'")).toBe('str')
    // "orders" contains no standalone keyword; it stays plain text.
    expect(highlightSql('orders').every((t) => t.cls === '')).toBe(true)
  })

  it('handles a doubled-quote escape inside a string', () => {
    const sql = "'it''s here'"
    expect(roundtrip(sql)).toBe(sql)
    expect(classOf(sql, "'it''s here'")).toBe('str')
  })

  it('leaves an unterminated string from a truncated statement intact', () => {
    const sql = "INSERT INTO t VALUES ('abc"
    expect(roundtrip(sql)).toBe(sql)
  })

  it('treats the brokoli truncation marker as a comment', () => {
    const sql = 'SELECT 1\n-- [brokoli] statement truncated: 100 of 200 bytes recorded'
    expect(classOf(sql, '-- [brokoli] statement truncated: 100 of 200 bytes recorded')).toBe('com')
  })
})
