import { describe, expect, it } from 'vitest'
import type { ChangeRequest } from '@brokoli/api'
import { cannotApply, changeAuthor, diffLines, shortValue } from './changes'

const cr = (over: Partial<ChangeRequest> = {}): ChangeRequest => ({
  id: 'c1',
  pipeline_id: 'p1',
  title: 'Faster',
  status: 'open',
  stale: false,
  base_version: 3,
  validation: { valid: true, errors: [] },
  created_by: 'a1',
  created_by_name: 'triage-bot',
  created_by_kind: 'agent',
  created_at: '2026-10-20T08:00:00Z',
  ...over,
})

describe('changeAuthor', () => {
  it('names a person, or an agent and whom it acted for', () => {
    expect(changeAuthor(cr({ created_by_kind: 'person', created_by_name: 'Ada' }))).toBe('Ada')
    expect(changeAuthor(cr())).toBe('agent triage-bot')
    expect(changeAuthor(cr({ on_behalf_of: 'Ada' }))).toBe('agent triage-bot, for Ada')
  })
})

describe('diffLines', () => {
  it('says what changes, in the order it happens', () => {
    expect(
      diffLines({
        pipeline: [{ field: 'schedule', before: '0 * * * *', after: '*/30 * * * *' }],
        nodes_removed: [{ id: 'old', name: 'Old', type: 'transform' }],
        nodes_added: [{ id: 'save', name: 'Save', type: 'sink_file' }],
        nodes_changed: [
          {
            id: 'f',
            name: 'Fetch',
            type: 'source_api',
            changes: [{ field: 'config.url', before: 'v1', after: 'v2' }],
          },
        ],
        edges_removed: [{ from: 'f', to: 'old' }],
        edges_added: [{ from: 'f', to: 'save' }],
      }),
    ).toEqual([
      'Schedule: 0 * * * * → */30 * * * *',
      'Removes node Old (transform)',
      'Adds node Save (sink_file)',
      'Fetch, url: v1 → v2',
      'Disconnects f → old',
      'Connects f → save',
    ])
    expect(diffLines(undefined)).toEqual([])
  })
  it('keeps values short', () => {
    expect(shortValue(undefined)).toBe('(none)')
    expect(shortValue({ a: 1 })).toBe('{"a":1}')
    expect(shortValue('x'.repeat(100))).toHaveLength(80)
  })
})

describe('cannotApply', () => {
  const ok = { canEdit: true, fromGit: false }
  it('says why not, in order of what matters', () => {
    expect(cannotApply(cr(), ok)).toBeNull()
    expect(cannotApply(cr({ status: 'applied' }), ok)).toMatch(/applied/)
    expect(cannotApply(cr(), { ...ok, fromGit: true })).toMatch(/repository/)
    expect(cannotApply(cr({ stale: true }), ok)).toMatch(/changed since/)
    expect(cannotApply(cr({ validation: { valid: false, errors: ['x'] } }), ok)).toMatch(
      /not valid/,
    )
    expect(cannotApply(cr(), { ...ok, canEdit: false })).toMatch(/permission/)
  })
})
