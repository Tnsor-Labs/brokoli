import type { ColumnEdge, LineageGraph, LineageNode } from '@brokoli/api'

/*
 * Client-side model of GET /api/lineage.
 *
 * The server can return the same asset pair once per pipeline that moves
 * data between them. Those parallel edges are merged into one edge that
 * lists every pipeline, since drawn separately they overlap exactly and
 * only the top one could be hovered. Neighbour lists are deduplicated for
 * the same reason.
 */

export type MergedEdge = { id: string; from: string; to: string; pipelines: { id: string; name: string }[] }

export type LineageModel = {
  nodes: LineageNode[]
  byId: Map<string, LineageNode>
  edges: MergedEdge[]
  columnEdges: ColumnEdge[]
  upstream: Map<string, string[]>
  downstream: Map<string, string[]>
}

export function buildModel(graph: LineageGraph): LineageModel {
  const byId = new Map<string, LineageNode>()
  for (const n of graph.nodes ?? []) if (!byId.has(n.id)) byId.set(n.id, n)
  const nodes = [...byId.values()].sort((a, b) => a.name.localeCompare(b.name) || (a.id < b.id ? -1 : 1))

  const merged = new Map<string, MergedEdge>()
  for (const e of graph.edges ?? []) {
    if (!byId.has(e.from) || !byId.has(e.to)) continue
    const key = JSON.stringify([e.from, e.to])
    const edge = merged.get(key) ?? { id: key, from: e.from, to: e.to, pipelines: [] }
    if (e.pipeline_id && !edge.pipelines.some((p) => p.id === e.pipeline_id)) edge.pipelines.push({ id: e.pipeline_id, name: e.pipeline || e.pipeline_id })
    merged.set(key, edge)
  }
  const edges = [...merged.values()]
  for (const e of edges) e.pipelines.sort((a, b) => a.name.localeCompare(b.name))

  const upstream = new Map<string, string[]>()
  const downstream = new Map<string, string[]>()
  for (const e of edges) {
    downstream.set(e.from, [...(downstream.get(e.from) ?? []), e.to])
    upstream.set(e.to, [...(upstream.get(e.to) ?? []), e.from])
  }
  return { nodes, byId, edges, columnEdges: graph.column_edges ?? [], upstream, downstream }
}

export const isAsset = (n: LineageNode) => n.type !== 'processing'

const ASSET: Record<string, { label: string; family: string; mini: string }> = {
  file: { label: 'File', family: 'var(--bk-color-tax-source)', mini: 'ob-mini-source' },
  api: { label: 'API', family: 'var(--bk-color-tax-source)', mini: 'ob-mini-source' },
  table: { label: 'Table', family: 'var(--bk-color-tax-output)', mini: 'ob-mini-output' },
}

/** Labels for every processing node type the server can report; plugin types show their raw name. */
const STEP: Record<string, string> = {
  transform: 'Transform',
  code: 'Code',
  join: 'Join',
  quality_check: 'Quality check',
  sql_generate: 'SQL generate',
  sink_api: 'API sink',
  migrate: 'Migrate',
  condition: 'Condition',
  wait: 'Wait',
  dbt: 'dbt',
  notify: 'Notify',
  union: 'Union',
  dataset_map: 'Dataset map',
  dataset_filter: 'Dataset filter',
  task: 'Task',
}

export function kindOf(n: LineageNode): { label: string; family: string; mini: string } {
  if (n.type === 'processing') {
    return { label: (n.sub_type && STEP[n.sub_type]) || n.sub_type || 'Processing step', family: 'var(--bk-color-tax-processing)', mini: 'ob-mini-processing' }
  }
  return ASSET[n.type] ?? { label: n.type || 'Asset', family: 'var(--bk-color-text-muted)', mini: 'ob-mini-other' }
}

/** Pipelines that read, write or contain this node. */
export function pipelinesOf(model: LineageModel, id: string): { id: string; name: string }[] {
  const out = new Map<string, string>()
  const node = model.byId.get(id)
  if (node?.pipeline_id) out.set(node.pipeline_id, node.pipeline || node.pipeline_id)
  for (const e of model.edges) if (e.from === id || e.to === id) for (const p of e.pipelines) out.set(p.id, p.name)
  return [...out].map(([pid, name]) => ({ id: pid, name })).sort((a, b) => a.name.localeCompare(b.name))
}

export type ColumnGroup = { neighbour: string; direction: 'upstream' | 'downstream'; mappings: ColumnEdge[] }

/** Column mappings touching a node, grouped by the node on the other side and the direction of flow. */
export function columnGroups(model: LineageModel, id: string): ColumnGroup[] {
  const groups = new Map<string, ColumnGroup>()
  for (const c of model.columnEdges) {
    const direction = c.to === id ? 'upstream' : c.from === id ? 'downstream' : null
    if (!direction) continue
    const neighbour = direction === 'upstream' ? c.from : c.to
    const key = `${direction}|${neighbour}`
    const group = groups.get(key) ?? { neighbour, direction, mappings: [] }
    group.mappings.push(c)
    groups.set(key, group)
  }
  return [...groups.values()].sort((a, b) => (a.direction === b.direction ? 0 : a.direction === 'upstream' ? -1 : 1))
}
