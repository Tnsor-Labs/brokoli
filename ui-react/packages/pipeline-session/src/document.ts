import type { Pipeline, PipelineEdge, PipelineNode } from '@brokoli/api'
import { catalogEntry, portsFor } from './catalog'

/*
 * Pure document logic for the editor. No React here, so every rule is unit
 * tested (document.test.ts).
 *
 * The server decodes a pipeline strictly: an unknown key anywhere (top
 * level, node, edge) is a 400. The editor therefore never builds nodes or
 * edges from scratch for saving. It keeps the objects the server returned
 * and applies edits to them with spreads, so fields the UI does not model
 * (a task node's `interface`, typed `parameters`, `extensions`, edge ports)
 * round-trip untouched, and fields the UI does model never pick up view
 * state such as React Flow's `selected` or `measured`.
 */

export const NODE_WIDTH = 220
export const NODE_HEIGHT = 64

/** Condition grammar, copied from models/pipeline.go supportedConditionExpressions. */
const CONDITION_PATTERNS = [
  /^row_count\s*(==|!=|>=|<=|>|<)\s*\d+$/,
  /^column_exists\(\s*"[^"]+"\s*\)$/,
  /^null_pct\(\s*"[^"]+"\s*\)\s*(==|!=|>=|<=|>|<)\s*(?:\d+(?:\.\d*)?|\.\d+)$/,
  /^(min|max)\(\s*"[^"]+"\s*\)\s*(==|!=|>=|<=|>|<)\s*(?:\d+(?:\.\d*)?|\.\d+)$/,
]

export function isConditionSupported(expression: string) {
  const e = expression.trim()
  return e === 'always_true' || e === 'always_false' || CONDITION_PATTERNS.some((p) => p.test(e))
}

export function newNodeId(existing: Iterable<string>) {
  const taken = new Set(existing)
  for (;;) {
    const id = `n_${crypto.randomUUID().replaceAll('-', '').slice(0, 8)}`
    if (!taken.has(id)) return id
  }
}

export function createNode(type: string, position: { x: number; y: number }, existingIds: Iterable<string>): PipelineNode {
  return { id: newNodeId(existingIds), type, name: catalogEntry(type).label, config: {}, position }
}

/** Duplicates keep every field of the original (capabilities, interface), with a new id and name. */
export function duplicateNode(node: PipelineNode, existingIds: Iterable<string>): PipelineNode {
  return {
    ...structuredClone(node),
    id: newNodeId(existingIds),
    name: `${node.name} (copy)`,
    position: { x: node.position.x + 40, y: node.position.y + 40 },
  }
}

/**
 * Applies a config patch in one step. Undefined and empty-string values
 * remove the key: Go treats an absent key as "use the default", and for
 * some keys (python_path, node_path) it rejects an empty string outright.
 */
export function patchConfig(config: Record<string, unknown>, patch: Record<string, unknown>) {
  const next = { ...config }
  for (const [key, value] of Object.entries(patch)) {
    if (value === undefined || value === '') delete next[key]
    else next[key] = value
  }
  return next
}

function reaches(edges: PipelineEdge[], from: string, target: string) {
  const out = new Map<string, string[]>()
  for (const e of edges) out.set(e.from, [...(out.get(e.from) ?? []), e.to])
  const seen = new Set<string>()
  const stack = [from]
  while (stack.length) {
    const id = stack.pop()!
    if (id === target) return true
    if (seen.has(id)) continue
    seen.add(id)
    stack.push(...(out.get(id) ?? []))
  }
  return false
}

/** Why a connection is not allowed, or null when it is. */
export function connectionProblem(nodes: PipelineNode[], edges: PipelineEdge[], from: string, to: string): string | null {
  if (from === to) return 'A node cannot connect to itself.'
  const source = nodes.find((n) => n.id === from)
  const target = nodes.find((n) => n.id === to)
  if (!source || !target) return 'One of these nodes no longer exists.'
  if (edges.some((e) => e.from === from && e.to === to)) return 'These nodes are already connected.'
  const sp = portsFor(source)
  const tp = portsFor(target)
  if (!sp.output) return `${source.name} has no output to connect.`
  if (!tp.input) return `${target.name} does not take input.`
  const incoming = edges.filter((e) => e.to === to).length
  if (tp.maxInputs >= 0 && incoming >= tp.maxInputs)
    return tp.maxInputs === 1 ? `${target.name} already has its input.` : `${target.name} takes at most ${tp.maxInputs} inputs.`
  if (reaches(edges, to, from)) return 'That connection would create a loop.'
  return null
}

/** Structural warnings the canvas can show before the server is asked. */
export function nodeWarnings(node: PipelineNode, edges: PipelineEdge[]): string[] {
  const incoming = edges.filter((e) => e.to === node.id).length
  const out: string[] = []
  if (node.type === 'join' && incoming !== 2) out.push(`A join needs exactly 2 inputs; it has ${incoming}.`)
  if (node.type === 'condition') {
    const expr = typeof node.config.expression === 'string' ? node.config.expression : ''
    if (expr && !isConditionSupported(expr)) out.push('The condition expression is not in the supported grammar.')
    if (edges.some((e) => e.from === node.id && e.condition === undefined)) out.push('Every branch out of an If / Else needs to be marked true or false.')
  }
  return out
}

/*
 * IR version. A conditional edge requires IR "2.1" exactly. "" and "2.0"
 * are upgraded. "2.2" cannot carry conditional edges at all, so saving
 * refuses with an explanation instead of silently downgrading the pipeline
 * (which the Svelte editor did, dropping 2.2 semantics).
 */
export function irVersionFor(pipeline: Pick<Pipeline, 'ir_version'>, edges: PipelineEdge[]): { version?: string; error?: string } {
  const conditional = edges.some((e) => e.condition !== undefined)
  if (!conditional) return { version: pipeline.ir_version }
  if (!pipeline.ir_version || pipeline.ir_version === '2.0' || pipeline.ir_version === '2.1') return { version: '2.1' }
  return {
    error: `This pipeline uses IR ${pipeline.ir_version}, which does not support If / Else branches. Remove the branch labels or recreate the branch in an IR 2.1 pipeline.`,
  }
}

export function buildSavePayload(pipeline: Pipeline, nodes: PipelineNode[], edges: PipelineEdge[]): Pipeline {
  const ir = irVersionFor(pipeline, edges)
  if (ir.error) throw new Error(ir.error)
  const payload: Pipeline = { ...pipeline, nodes, edges }
  if (ir.version === undefined) delete payload.ir_version
  else payload.ir_version = ir.version
  return payload
}

/*
 * Layered layout: each node goes one column right of its furthest
 * predecessor, rows are ordered by the average row of their predecessors to
 * reduce crossings, and columns are centred on each other. Nodes inside a
 * cycle (which the server rejects anyway) keep their position.
 */
export function autoLayout(nodes: PipelineNode[], edges: PipelineEdge[]): PipelineNode[] {
  const ids = new Set(nodes.map((n) => n.id))
  const valid = edges.filter((e) => ids.has(e.from) && ids.has(e.to))
  const indegree = new Map(nodes.map((n) => [n.id, 0]))
  const preds = new Map(nodes.map((n) => [n.id, [] as string[]]))
  const succ = new Map(nodes.map((n) => [n.id, [] as string[]]))
  for (const e of valid) {
    indegree.set(e.to, (indegree.get(e.to) ?? 0) + 1)
    preds.get(e.to)!.push(e.from)
    succ.get(e.from)!.push(e.to)
  }
  const layer = new Map<string, number>()
  const queue = nodes.filter((n) => !indegree.get(n.id)).map((n) => n.id)
  queue.forEach((id) => layer.set(id, 0))
  while (queue.length) {
    const id = queue.shift()!
    for (const next of succ.get(id)!) {
      layer.set(next, Math.max(layer.get(next) ?? 0, (layer.get(id) ?? 0) + 1))
      indegree.set(next, indegree.get(next)! - 1)
      if (indegree.get(next) === 0) queue.push(next)
    }
  }
  const columns: string[][] = []
  for (const n of nodes) {
    const l = layer.get(n.id)
    if (l === undefined) continue
    ;(columns[l] ??= []).push(n.id)
  }
  const row = new Map<string, number>()
  columns.forEach((col, c) => {
    if (c > 0) {
      const weight = (id: string) => {
        const ps = preds.get(id)!.filter((p) => row.has(p))
        return ps.length ? ps.reduce((s, p) => s + row.get(p)!, 0) / ps.length : Infinity
      }
      col.sort((a, b) => weight(a) - weight(b))
    }
    col.forEach((id, r) => row.set(id, r))
  })
  const tallest = Math.max(1, ...columns.map((c) => c?.length ?? 0))
  const gapX = NODE_WIDTH + 90
  const gapY = NODE_HEIGHT + 46
  return nodes.map((n) => {
    const l = layer.get(n.id)
    if (l === undefined) return n
    const offset = ((tallest - columns[l].length) * gapY) / 2
    return { ...n, position: { x: 60 + l * gapX, y: 60 + offset + row.get(n.id)! * gapY } }
  })
}

/**
 * Where to put a node added by clicking the palette: the anchor (the centre
 * of the view) if it is free, otherwise the nearest free slot below and then
 * to the right of it. Without this, two clicks stacked two nodes exactly on
 * top of each other and the second hid the first.
 */
export function nextFreePosition(nodes: PipelineNode[], anchor?: { x: number; y: number }) {
  const start = anchor ?? { x: 80, y: 80 }
  const overlaps = (p: { x: number; y: number }) =>
    nodes.some((n) => Math.abs(n.position.x - p.x) < NODE_WIDTH + 20 && Math.abs(n.position.y - p.y) < NODE_HEIGHT + 20)
  const stepX = NODE_WIDTH + 90
  const stepY = NODE_HEIGHT + 46
  for (let i = 0; i < 200; i++) {
    const p = { x: Math.round(start.x + Math.floor(i / 5) * stepX), y: Math.round(start.y + (i % 5) * stepY) }
    if (!overlaps(p)) return p
  }
  return { x: Math.round(start.x), y: Math.round(start.y) }
}

/** Keys edited by the settings drawer; everything else on the pipeline passes through untouched. */
export function sanitizeTags(input: string[]) {
  return [...new Set(input.map((t) => t.trim()).filter(Boolean))]
}
