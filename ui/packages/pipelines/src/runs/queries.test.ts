import { describe, expect, it } from 'vitest'
import type { RunEvent } from '@brokoli/api'
import { classifyStatement, groupQueries, statementReason } from './queries'

const TRUNCATED = 'SELECT * FROM big\n-- [brokoli] statement truncated: 64000 of 91234 bytes recorded'
const WITHHELD = '-- [brokoli] statement not recorded: a secret it substitutes is shorter than 8 bytes and cannot be masked'
const NO_SQL = '-- [brokoli] no SQL statement: this write streamed rows into "orders" using the PostgreSQL COPY protocol, which streams rows and records no statement'

function ev(over: Partial<RunEvent> & { id: RunEvent['id'] }): RunEvent {
  return {
    run_id: 'r1',
    event_type: 'attempt.query',
    payload: {},
    created_at: '2026-09-17T10:00:00Z',
    ...over,
  }
}

function q(id: RunEvent['id'], node_id: string, attempt: number, statement: string): RunEvent {
  return ev({ id, node_id, attempt, payload: { statement } })
}

describe('classifyStatement', () => {
  it('reads a plain statement as sql, not truncated', () => {
    expect(classifyStatement("SELECT 1 WHERE d = '2024-03-13'")).toEqual({ kind: 'sql', truncated: false })
  })
  it('flags a truncated statement while keeping it sql', () => {
    expect(classifyStatement(TRUNCATED)).toEqual({ kind: 'sql', truncated: true })
  })
  it('recognises a withheld statement', () => {
    expect(classifyStatement(WITHHELD)).toEqual({ kind: 'withheld', truncated: false })
  })
  it('recognises a no-SQL-by-nature note', () => {
    expect(classifyStatement(NO_SQL)).toEqual({ kind: 'none', truncated: false })
  })
  it('does not mistake a statement that merely mentions the marker text', () => {
    // Leading content is real SQL, so it stays sql even though the body has the phrase.
    expect(classifyStatement("SELECT '-- [brokoli] no SQL statement'").kind).toBe('sql')
  })
})

describe('statementReason', () => {
  it('drops the brokoli prefix and label from a no-SQL note', () => {
    expect(statementReason(NO_SQL)).toBe(
      'this write streamed rows into "orders" using the PostgreSQL COPY protocol, which streams rows and records no statement',
    )
  })
  it('drops the prefix from a withheld refusal', () => {
    expect(statementReason(WITHHELD)).toBe('a secret it substitutes is shorter than 8 bytes and cannot be masked')
  })
  it('returns the raw text when the shape is unexpected', () => {
    expect(statementReason('-- [brokoli] something else entirely')).toBe('-- [brokoli] something else entirely')
  })
})

describe('groupQueries', () => {
  it('ignores events that are not attempt.query', () => {
    const events = [ev({ id: 1, event_type: 'run.started' }), q(2, 'extract', 0, 'SELECT 1'), ev({ id: 3, event_type: 'attempt.finished', node_id: 'extract' })]
    const groups = groupQueries(events)
    expect(groups).toHaveLength(1)
    expect(groups[0].statements).toHaveLength(1)
  })

  it('keeps several statements from one attempt in event-id order', () => {
    // A pushed-down overwrite records the clear and the refill as two events; out of order on input.
    const events = [q(11, 'load', 0, 'INSERT INTO t SELECT ...'), q(10, 'load', 0, 'DELETE FROM t')]
    const groups = groupQueries(events)
    expect(groups).toHaveLength(1)
    expect(groups[0].statements.map((s) => s.statement)).toEqual(['DELETE FROM t', 'INSERT INTO t SELECT ...'])
  })

  it('separates attempts of the same node', () => {
    const events = [q(1, 'extract', 0, 'SELECT 1'), q(2, 'extract', 1, 'SELECT 1')]
    const groups = groupQueries(events)
    expect(groups.map((g) => g.attempt)).toEqual([0, 1])
    expect(groups.every((g) => g.nodeId === 'extract')).toBe(true)
  })

  it('defaults a missing attempt to 0 and tolerates a missing statement', () => {
    const events = [ev({ id: 1, node_id: 'x', payload: {} }), ev({ id: 2, node_id: 'x', payload: { statement: 'SELECT 2' } })]
    const groups = groupQueries(events)
    expect(groups).toHaveLength(1)
    expect(groups[0].attempt).toBe(0)
    expect(groups[0].statements.map((s) => s.statement)).toEqual(['', 'SELECT 2'])
  })

  it('orders string ids numerically', () => {
    const events = [q('10', 'n', 0, 'b'), q('2', 'n', 0, 'a')]
    expect(groupQueries(events)[0].statements.map((s) => s.statement)).toEqual(['a', 'b'])
  })
})
