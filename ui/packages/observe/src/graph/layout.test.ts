import { describe, expect, it } from 'vitest'
import { computeLayers, layeredLayout, reachable, type LayoutEdge, type LayoutNode } from './layout'

const node = (id: string, name = id): LayoutNode => ({ id, name })
const edge = (from: string, to: string): LayoutEdge => ({ from, to })
const opts = { colGap: 100, rowGap: 10 }

describe('computeLayers', () => {
  it('puts each node of a chain one layer to the right of its parent', () => {
    const layers = computeLayers([node('c'), node('a'), node('b')], [edge('a', 'b'), edge('b', 'c')])
    expect(Object.fromEntries(layers)).toEqual({ a: 0, b: 1, c: 2 })
  })

  it('uses the longest path in a diamond with a shortcut', () => {
    const edges = [edge('a', 'b'), edge('a', 'c'), edge('b', 'd'), edge('c', 'd'), edge('a', 'd')]
    const layers = computeLayers(['a', 'b', 'c', 'd'].map((id) => node(id)), edges)
    expect(Object.fromEntries(layers)).toEqual({ a: 0, b: 1, c: 1, d: 2 })
  })

  it('terminates on a cycle and still places every node', () => {
    const layers = computeLayers([node('a'), node('b'), node('c')], [edge('a', 'b'), edge('b', 'c'), edge('c', 'a')])
    expect(layers.size).toBe(3)
    expect(new Set(layers.values()).size).toBe(3)
  })

  it('ignores self loops and edges whose endpoints are unknown', () => {
    const layers = computeLayers([node('a'), node('b')], [edge('a', 'a'), edge('ghost', 'a'), edge('b', 'ghost'), edge('a', 'b')])
    expect(Object.fromEntries(layers)).toEqual({ a: 0, b: 1 })
  })
})

describe('layeredLayout', () => {
  it('is identical whatever order the server sends nodes and edges in', () => {
    const nodes = [node('1', 'zeta'), node('2', 'alpha'), node('3', 'mid'), node('4', 'beta'), node('5', 'alpha')]
    const edges = [edge('2', '3'), edge('4', '3'), edge('1', '3'), edge('3', '5')]
    const expected = layeredLayout(nodes, edges, opts)
    for (let i = 0; i < 20; i++) {
      const shuffledNodes = [...nodes].sort(() => Math.random() - 0.5)
      const shuffledEdges = [...edges].sort(() => Math.random() - 0.5)
      expect(layeredLayout(shuffledNodes, shuffledEdges, opts)).toEqual(expected)
    }
    // Within a layer: by name, then by id for equal names.
    expect(expected.get('2')!.y).toBeLessThan(expected.get('4')!.y)
    expect(expected.get('4')!.y).toBeLessThan(expected.get('1')!.y)
  })

  it('centres short layers by default and top-aligns them on request', () => {
    const nodes = [node('a'), node('b'), node('c'), node('d')]
    const edges = [edge('a', 'd'), edge('b', 'd'), edge('c', 'd')]
    expect(layeredLayout(nodes, edges, opts).get('d')).toEqual({ x: 100, y: 10 })
    expect(layeredLayout(nodes, edges, { ...opts, align: 'top' }).get('d')).toEqual({ x: 100, y: 0 })
  })
})

describe('reachable', () => {
  const edges = [edge('a', 'b'), edge('b', 'c'), edge('x', 'b'), edge('c', 'a')]
  it('follows edges transitively in either direction and survives cycles', () => {
    expect([...reachable('b', edges, 'down')].sort()).toEqual(['a', 'c'])
    expect([...reachable('b', edges, 'up')].sort()).toEqual(['a', 'c', 'x'])
  })
})
