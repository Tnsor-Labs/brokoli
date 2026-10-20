import { describe, expect, it } from 'vitest'
import type { ChangeTest, Pipeline } from '@brokoli/api'
import { compareTest, fieldKind, graphDiff, hunks, lineDiff } from './review'

const kinds = (s: ReturnType<typeof lineDiff>) =>
  s.map((l) => `${l.kind === 'same' ? ' ' : l.kind === 'add' ? '+' : '-'}${l.text}`)

describe('lineDiff', () => {
  it('keeps what is common and marks what changed, removals first', () => {
    expect(kinds(lineDiff('a\nb\nc\nd', 'a\nB\nc\nd\ne'))).toEqual([
      ' a',
      '-b',
      '+B',
      ' c',
      ' d',
      '+e',
    ])
  })
  it('numbers lines on each side', () => {
    const d = lineDiff('x\ny', 'w\nx\ny')
    expect(d[0]).toMatchObject({ kind: 'add', text: 'w', after: 1 })
    expect(d[2]).toMatchObject({ kind: 'same', text: 'y', before: 2, after: 3 })
  })
  it('is empty of changes for equal texts, and whole for a new one', () => {
    expect(lineDiff('a\nb', 'a\nb').every((l) => l.kind === 'same')).toBe(true)
    expect(kinds(lineDiff('', 'a'))).toEqual(['-', '+a'])
  })
})

describe('hunks', () => {
  it('keeps changed lines with their context and counts what it leaves out', () => {
    const before = Array.from({ length: 20 }, (_, i) => `l${i}`).join('\n')
    const after = before.replace('l10', 'L10')
    const { hunks: hs, hiddenAfter } = hunks(lineDiff(before, after), 2)
    expect(hs).toHaveLength(1)
    expect(hs[0].hiddenBefore).toBe(8)
    expect(hs[0].lines.map((l) => l.text)).toEqual(['l8', 'l9', 'l10', 'L10', 'l11', 'l12'])
    expect(hiddenAfter).toBe(7)
  })
})

describe('fieldKind', () => {
  it('reads scripts and long text as a diff, short values as before and after', () => {
    expect(fieldKind({ field: 'config.script', before: 'a', after: 'b' })).toBe('text')
    expect(fieldKind({ field: 'config.note', before: 'one\ntwo', after: 'one\nthree' })).toBe(
      'text',
    )
    expect(fieldKind({ field: 'config.url', before: 'https://a', after: 'https://b' })).toBe(
      'value',
    )
    expect(fieldKind({ field: 'config.rules', before: [], after: [{ type: 'x' }] })).toBe('value')
  })
})

describe('graphDiff', () => {
  it('draws the proposed graph with the removed parts kept and marked', () => {
    const current = {
      nodes: [{ id: 'a' }, { id: 'b' }, { id: 'old' }],
      edges: [
        { from: 'a', to: 'old' },
        { from: 'a', to: 'b' },
      ],
    } as unknown as Pipeline
    const proposed = {
      nodes: [{ id: 'a' }, { id: 'b' }, { id: 'new' }],
      edges: [
        { from: 'a', to: 'b' },
        { from: 'b', to: 'new' },
      ],
    } as unknown as Pipeline
    const g = graphDiff(current, proposed, {
      nodes_added: [{ id: 'new', name: 'New', type: 'filter' }],
      nodes_removed: [{ id: 'old', name: 'Old', type: 'filter' }],
      nodes_changed: [{ id: 'b', name: 'B', type: 'code', changes: [] }],
      edges_added: [{ from: 'b', to: 'new' }],
      edges_removed: [{ from: 'a', to: 'old' }],
    })
    expect(g.nodes.map((n) => n.id)).toEqual(['a', 'b', 'new', 'old'])
    expect(g.marks).toEqual({ new: 'added', old: 'removed', b: 'changed' })
    expect(g.edges).toHaveLength(3)
    expect(g.edgeMarks).toEqual({ 'b->new': 'added', 'a->old': 'removed' })
  })
})

describe('compareTest', () => {
  const node = (
    id: string,
    status: string,
    columns: string[],
    rows: Record<string, unknown>[],
  ) => ({ node_id: id, name: id, type: 'code', status, columns, rows })
  it('says what the change does at each node', () => {
    const test: ChangeTest = {
      rows: 10,
      current: {
        nodes: [
          node('src', 'success', ['id'], [{ id: 1 }]),
          node('tier', 'success', ['id', 'tier'], [{ id: 1, tier: 'high' }]),
          node('gone', 'success', ['id'], [{ id: 1 }]),
          node('broken', 'failed', [], []),
          { node_id: 'save', name: 'save', type: 'sink_file', status: 'held', reason: 'writes' },
        ],
      },
      proposed: {
        nodes: [
          node('src', 'success', ['id'], [{ id: 1 }]),
          node(
            'tier',
            'success',
            ['id', 'tier', 'median'],
            [{ id: 1, tier: 'medium', median: 200 }],
          ),
          node('broken', 'success', ['id'], [{ id: 1 }]),
          { node_id: 'save', name: 'save', type: 'sink_file', status: 'held', reason: 'writes' },
        ],
      },
    }
    const byId = Object.fromEntries(
      compareTest(test, { tier: 'changed', gone: 'removed' }).map((c) => [c.id, c]),
    )
    expect(byId.src.verdict).toBe('')
    expect(byId.tier.verdict).toBe('adds median; tier differs in 1 of 1 rows')
    expect(byId.tier.changedCells.has('0:tier')).toBe(true)
    expect(byId.tier.rowsDiffer).toBe(true)
    expect(byId.tier.mark).toBe('changed')
    expect(byId.gone.verdict).toBe('Removed by the change')
    expect(byId.broken.verdict).toBe('Fixed by the change')
    expect(byId.broken.failsInBoth).toBe(false)
    expect(byId.save.verdict).toBe('')
    expect(Object.keys(byId)).toEqual(['src', 'tier', 'broken', 'save', 'gone'])
  })
  it('notices values that change without a column changing', () => {
    const test: ChangeTest = {
      rows: 10,
      current: { nodes: [node('tier', 'success', ['tier'], [{ tier: 'high' }])] },
      proposed: { nodes: [node('tier', 'success', ['tier'], [{ tier: 'medium' }])] },
    }
    expect(compareTest(test, {})[0].verdict).toBe('tier differs in 1 of 1 rows')
  })
})
