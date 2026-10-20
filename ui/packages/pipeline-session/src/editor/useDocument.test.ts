// @vitest-environment jsdom
import { act, renderHook } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import type { Pipeline } from '@brokoli/api'
import { useDocument } from './useDocument'

const pipeline = (positions: Array<{ x: number; y: number }>): Pipeline =>
  ({
    id: 'p',
    name: 'p',
    nodes: positions.map((position, i) => ({
      id: `n${i}`,
      type: 'transform',
      name: `n${i}`,
      config: {},
      position,
    })),
    edges: positions.slice(1).map((_, i) => ({ from: `n${i}`, to: `n${i + 1}` })),
  }) as unknown as Pipeline

describe('useDocument', () => {
  it('lays out stacked nodes on load without counting it as an edit', () => {
    const { result } = renderHook(() =>
      useDocument(
        pipeline([
          { x: 0, y: 0 },
          { x: 0, y: 0 },
        ]),
      ),
    )
    const [a, b] = result.current.doc!.nodes
    expect(a.position).not.toEqual(b.position)
    expect(result.current.dirty).toBe(false)
  })

  it('lays out stacked nodes again on a reset, still clean', () => {
    const loaded = pipeline([
      { x: 0, y: 0 },
      { x: 0, y: 0 },
    ])
    const { result } = renderHook(() => useDocument(loaded))
    act(() =>
      result.current.change((d) => ({ ...d, nodes: d.nodes.map((n) => ({ ...n, name: 'x' })) })),
    )
    expect(result.current.dirty).toBe(true)
    act(() => result.current.reset(loaded))
    const [a, b] = result.current.doc!.nodes
    expect(a.position).not.toEqual(b.position)
    expect(result.current.dirty).toBe(false)
  })

  it('keeps a clean document clean through a view change, but not a dirty one', () => {
    const { result } = renderHook(() =>
      useDocument(
        pipeline([
          { x: 0, y: 0 },
          { x: 300, y: 0 },
        ]),
      ),
    )
    const move = (d: { nodes: Pipeline['nodes'] }) => ({
      ...d,
      nodes: d.nodes.map((n) => ({ ...n, position: { x: n.position.x + 10, y: 5 } })),
    })
    act(() => result.current.change((d) => ({ ...d, ...move(d) }), false, true))
    expect(result.current.doc!.nodes[0].position).toEqual({ x: 10, y: 5 })
    expect(result.current.dirty).toBe(false)
    act(() => result.current.change((d) => ({ ...d, ...move(d) }), false))
    expect(result.current.dirty).toBe(true)
    act(() => result.current.change((d) => ({ ...d, ...move(d) }), false, true))
    expect(result.current.dirty).toBe(true)
  })

  it('leaves positions that are already laid out alone', () => {
    const { result } = renderHook(() =>
      useDocument(
        pipeline([
          { x: 1, y: 2 },
          { x: 400, y: 2 },
        ]),
      ),
    )
    expect(result.current.doc!.nodes.map((n) => n.position)).toEqual([
      { x: 1, y: 2 },
      { x: 400, y: 2 },
    ])
  })
})
