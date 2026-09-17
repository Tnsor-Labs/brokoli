import { describe, expect, it } from 'vitest'
import type { PipelineEdge } from '@brokoli/api'
import { canRerunStatus, descendantClosure } from './model'

const edges = (...pairs: [string, string][]): PipelineEdge[] => pairs.map(([from, to]) => ({ from, to }))
const set = (...ids: string[]) => new Set(ids)

describe('descendantClosure', () => {
  it('includes the chosen node and everything downstream of it', () => {
    const e = edges(['a', 'b'], ['b', 'c'], ['c', 'd'])
    expect([...descendantClosure('b', e, set('a', 'b', 'c', 'd'))].sort()).toEqual(['b', 'c', 'd'])
  })

  it('returns just the node when it is a leaf', () => {
    const e = edges(['a', 'b'], ['b', 'c'])
    expect([...descendantClosure('c', e, set('a', 'b', 'c'))]).toEqual(['c'])
  })

  it('counts a diamond once', () => {
    const e = edges(['a', 'b'], ['a', 'c'], ['b', 'd'], ['c', 'd'])
    expect(descendantClosure('a', e, set('a', 'b', 'c', 'd')).size).toBe(4)
  })

  it('terminates on a cycle and a self edge', () => {
    const e = edges(['a', 'b'], ['b', 'a'], ['c', 'c'])
    expect([...descendantClosure('a', e, set('a', 'b', 'c')).values()].sort()).toEqual(['a', 'b'])
  })

  it('skips an edge whose endpoint is not in the node set', () => {
    // A dangling edge left by an edit must not pull in a node that no longer exists.
    const e = edges(['a', 'b'], ['b', 'gone'])
    expect([...descendantClosure('a', e, set('a', 'b')).values()].sort()).toEqual(['a', 'b'])
  })
})

describe('canRerunStatus', () => {
  it('accepts settled runs', () => {
    for (const s of ['success', 'failed', 'cancelled', 'SUCCESS']) expect(canRerunStatus(s)).toBe(true)
  })
  it('refuses in-flight or never-executed runs', () => {
    for (const s of ['running', 'pending', 'waiting', 'blocked', 'skipped', '', null, undefined]) expect(canRerunStatus(s)).toBe(false)
  })
})
