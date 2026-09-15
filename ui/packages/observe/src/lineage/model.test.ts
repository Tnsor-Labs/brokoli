import { describe, expect, it } from 'vitest'
import type { LineageGraph } from '@brokoli/api'
import { buildModel, columnGroups, kindOf, pipelinesOf } from './model'

const graph: LineageGraph = {
  nodes: [
    { id: 'file:/in.csv', type: 'file', name: 'in.csv' },
    { id: 'table:orders', type: 'table', name: 'orders' },
    { id: 'proc:p1:t', type: 'processing', name: 'clean', sub_type: 'transform', pipeline_id: 'p1', pipeline: 'Nightly' },
    { id: 'file:/in.csv', type: 'file', name: 'in.csv' },
  ],
  edges: [
    { from: 'file:/in.csv', to: 'table:orders', pipeline_id: 'p2', pipeline: 'Load' },
    { from: 'file:/in.csv', to: 'table:orders', pipeline_id: 'p1', pipeline: 'Nightly' },
    { from: 'file:/in.csv', to: 'table:orders', pipeline_id: 'p1', pipeline: 'Nightly' },
    { from: 'file:/in.csv', to: 'proc:p1:t', pipeline_id: 'p1', pipeline: 'Nightly' },
    { from: 'file:/in.csv', to: 'table:gone', pipeline_id: 'p1', pipeline: 'Nightly' },
  ],
  column_edges: [
    { from: 'file:/in.csv', from_column: 'id', to: 'table:orders', to_column: 'id', confidence: 0.7, mapping_reason: 'observed column name match' },
    { from: 'file:/in.csv', from_column: 'amount', to: 'table:orders', to_column: 'amount', confidence: 0.7, mapping_reason: 'observed column name match' },
  ],
}

describe('buildModel', () => {
  const model = buildModel(graph)

  it('merges parallel edges into one that lists every pipeline once', () => {
    const edge = model.edges.find((e) => e.to === 'table:orders')!
    expect(model.edges.filter((e) => e.to === 'table:orders')).toHaveLength(1)
    expect(edge.pipelines.map((p) => p.name)).toEqual(['Load', 'Nightly'])
  })

  it('drops duplicate nodes and edges to unknown nodes, and keeps neighbour lists unique', () => {
    expect(model.nodes).toHaveLength(3)
    expect(model.edges).toHaveLength(2)
    expect(model.downstream.get('file:/in.csv')).toEqual(['table:orders', 'proc:p1:t'])
    expect(model.upstream.get('table:orders')).toEqual(['file:/in.csv'])
  })

  it('groups column mappings by neighbour and direction', () => {
    expect(columnGroups(model, 'table:orders')).toEqual([expect.objectContaining({ neighbour: 'file:/in.csv', direction: 'upstream' })])
    expect(columnGroups(model, 'file:/in.csv')[0].mappings).toHaveLength(2)
    expect(columnGroups(model, 'proc:p1:t')).toEqual([])
  })

  it('lists the pipelines touching a node, including the one a step belongs to', () => {
    expect(pipelinesOf(model, 'table:orders').map((p) => p.id)).toEqual(['p2', 'p1'])
    expect(pipelinesOf(model, 'proc:p1:t')).toEqual([{ id: 'p1', name: 'Nightly' }])
  })
})

describe('kindOf', () => {
  it('names every processing type instead of a generic label', () => {
    expect(kindOf({ id: 'a', type: 'processing', name: 'x', sub_type: 'dbt' }).label).toBe('dbt')
    expect(kindOf({ id: 'a', type: 'processing', name: 'x', sub_type: 'my_plugin' }).label).toBe('my_plugin')
    expect(kindOf({ id: 'a', type: 'table', name: 'x' }).family).toContain('tax-output')
  })
})
