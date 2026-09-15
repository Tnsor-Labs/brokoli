/*
 * Longest-path layering for the lineage and dependency graphs.
 *
 * A node's layer is one more than the deepest of its parents, so every edge
 * points rightwards. Cycles cannot occur in a pipeline DAG, but lineage
 * merges assets across pipelines and can close a loop (a file one pipeline
 * writes and another reads back); the edge that closes a loop is ignored
 * instead of recursing forever.
 *
 * Nodes are visited and placed in (name, id) order, so the same graph
 * always produces the same picture regardless of the order the server sent
 * it in. The previous interface kept the server's order, which is random
 * for lineage, and the layout changed on every load.
 */

export type LayoutNode = { id: string; name: string }
export type LayoutEdge = { from: string; to: string }
export type LayoutOptions = {
  /** Horizontal distance between layers. */
  colGap: number
  /** Vertical distance between nodes in a layer. */
  rowGap: number
  /** 'center' centres each layer against the tallest one; 'top' aligns them all to the top. */
  align?: 'center' | 'top'
}

export const byName = (a: LayoutNode, b: LayoutNode) => a.name.localeCompare(b.name) || (a.id < b.id ? -1 : a.id > b.id ? 1 : 0)

export function computeLayers(nodes: LayoutNode[], edges: LayoutEdge[]): Map<string, number> {
  const known = new Set(nodes.map((n) => n.id))
  const order = new Map([...nodes].sort(byName).map((n, i) => [n.id, i]))
  const parents = new Map<string, string[]>()
  for (const e of edges) {
    if (e.from === e.to || !known.has(e.from) || !known.has(e.to)) continue
    const list = parents.get(e.to) ?? []
    if (!list.includes(e.from)) list.push(e.from)
    parents.set(e.to, list)
  }
  for (const list of parents.values()) list.sort((a, b) => order.get(a)! - order.get(b)!)

  const layer = new Map<string, number>()
  const visiting = new Set<string>()
  const visit = (id: string): number => {
    const memo = layer.get(id)
    if (memo !== undefined) return memo
    // An edge back into the current path closes a cycle; it does not add depth.
    if (visiting.has(id)) return -1
    visiting.add(id)
    let depth = 0
    for (const p of parents.get(id) ?? []) depth = Math.max(depth, visit(p) + 1)
    visiting.delete(id)
    layer.set(id, depth)
    return depth
  }
  for (const n of [...nodes].sort(byName)) visit(n.id)
  return layer
}

export function layeredLayout(nodes: LayoutNode[], edges: LayoutEdge[], opts: LayoutOptions): Map<string, { x: number; y: number }> {
  const layers = computeLayers(nodes, edges)
  const columns = new Map<number, LayoutNode[]>()
  const seen = new Set<string>()
  for (const n of nodes) {
    if (seen.has(n.id)) continue
    seen.add(n.id)
    const l = layers.get(n.id) ?? 0
    columns.set(l, [...(columns.get(l) ?? []), n])
  }
  const tallest = Math.max(0, ...[...columns.values()].map((c) => c.length))
  const out = new Map<string, { x: number; y: number }>()
  for (const [l, column] of columns) {
    column.sort(byName)
    const offset = opts.align === 'top' ? 0 : (tallest - column.length) / 2
    column.forEach((n, i) => out.set(n.id, { x: l * opts.colGap, y: (offset + i) * opts.rowGap }))
  }
  return out
}

/** Every node reachable by following edges in one direction, excluding the start. */
export function reachable(start: string, edges: LayoutEdge[], direction: 'up' | 'down'): Set<string> {
  const next = new Map<string, string[]>()
  for (const e of edges) {
    const [a, b] = direction === 'down' ? [e.from, e.to] : [e.to, e.from]
    next.set(a, [...(next.get(a) ?? []), b])
  }
  const out = new Set<string>()
  const queue = [start]
  while (queue.length) {
    for (const n of next.get(queue.shift()!) ?? []) {
      if (n === start || out.has(n)) continue
      out.add(n)
      queue.push(n)
    }
  }
  return out
}
