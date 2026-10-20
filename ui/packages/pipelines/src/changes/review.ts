import type {
  ChangeTest,
  FieldChange,
  Pipeline,
  PipelineDiff,
  PipelineEdge,
  PipelineNode,
  PipelinePreview,
} from '@brokoli/api'

/*
 * Reviewing a change request (ADR-046): what a change does, in the forms a
 * person checks it in. Pure, so it is tested on its own; the page renders it.
 */

// ── text diffs ──

export type DiffLine = {
  kind: 'same' | 'add' | 'del'
  text: string
  before?: number
  after?: number
}

/** Above this many lines on either side, a diff shows both texts whole rather than aligning them. */
export const MAX_DIFF_LINES = 4000

/**
 * The line diff of two texts (longest common subsequence), in order:
 * removed lines come before the lines that replace them.
 */
export function lineDiff(before: string, after: string): DiffLine[] {
  const a = before.split('\n')
  const b = after.split('\n')
  if (a.length > MAX_DIFF_LINES || b.length > MAX_DIFF_LINES) {
    return [
      ...a.map((text, i) => ({ kind: 'del' as const, text, before: i + 1 })),
      ...b.map((text, i) => ({ kind: 'add' as const, text, after: i + 1 })),
    ]
  }
  // Common prefix and suffix first: most edits touch a few lines of a long script.
  let start = 0
  while (start < a.length && start < b.length && a[start] === b[start]) start++
  let endA = a.length
  let endB = b.length
  while (endA > start && endB > start && a[endA - 1] === b[endB - 1]) {
    endA--
    endB--
  }
  const midA = a.slice(start, endA)
  const midB = b.slice(start, endB)
  const n = midA.length
  const m = midB.length
  // lcs[i][j]: the longest common subsequence of midA[i:] and midB[j:].
  const lcs: Uint32Array[] = Array.from({ length: n + 1 }, () => new Uint32Array(m + 1))
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      lcs[i][j] =
        midA[i] === midB[j] ? lcs[i + 1][j + 1] + 1 : Math.max(lcs[i + 1][j], lcs[i][j + 1])
    }
  }
  const out: DiffLine[] = []
  for (let k = 0; k < start; k++)
    out.push({ kind: 'same', text: a[k], before: k + 1, after: k + 1 })
  let i = 0
  let j = 0
  while (i < n || j < m) {
    if (i < n && j < m && midA[i] === midB[j]) {
      out.push({ kind: 'same', text: midA[i], before: start + i + 1, after: start + j + 1 })
      i++
      j++
    } else if (j >= m || (i < n && lcs[i + 1][j] >= lcs[i][j + 1])) {
      out.push({ kind: 'del', text: midA[i], before: start + i + 1 })
      i++
    } else {
      out.push({ kind: 'add', text: midB[j], after: start + j + 1 })
      j++
    }
  }
  for (let k = 0; k < a.length - endA; k++) {
    out.push({ kind: 'same', text: a[endA + k], before: endA + k + 1, after: endB + k + 1 })
  }
  return out
}

export type DiffHunk = { lines: DiffLine[]; hiddenBefore: number }

/**
 * The diff cut to the changed lines and `context` lines around them; each
 * hunk says how many unchanged lines were left out before it.
 */
export function hunks(lines: DiffLine[], context = 3): { hunks: DiffHunk[]; hiddenAfter: number } {
  const keep = new Array<boolean>(lines.length).fill(false)
  lines.forEach((l, i) => {
    if (l.kind === 'same') return
    for (let k = Math.max(0, i - context); k <= Math.min(lines.length - 1, i + context); k++)
      keep[k] = true
  })
  const out: DiffHunk[] = []
  let hidden = 0
  let current: DiffHunk | null = null
  lines.forEach((l, i) => {
    if (!keep[i]) {
      hidden++
      current = null
      return
    }
    if (!current) {
      current = { lines: [], hiddenBefore: hidden }
      out.push(current)
      hidden = 0
    }
    current.lines.push(l)
  })
  return { hunks: out, hiddenAfter: hidden }
}

// ── field changes ──

const CODE_FIELDS = /(^|\.)(script|code|query|sql|source|command|expression|template|body)$/i

/** The language a code field is written in, for its diff. */
export function codeLanguage(nodeType: string, field: string, node?: PipelineNode): string {
  const f = field.toLowerCase()
  if (/(query|sql)$/.test(f) || nodeType.includes('db') || nodeType === 'sql_generate') return 'sql'
  if (nodeType === 'bash' || /command$/.test(f)) return 'shell'
  const lang = node?.config?.language
  return typeof lang === 'string' && lang ? lang : nodeType === 'code' ? 'python' : 'text'
}

/** How a field change is best read: a text diff, or a value before and after. */
export function fieldKind(c: FieldChange): 'text' | 'value' {
  const isText = (v: unknown) => v === undefined || v === null || typeof v === 'string'
  if (!isText(c.before) || !isText(c.after)) return 'value'
  const text = `${c.before ?? ''}\n${c.after ?? ''}`
  return CODE_FIELDS.test(c.field) ||
    text.includes('\n', text.indexOf('\n') + 1) ||
    text.length > 120
    ? 'text'
    : 'value'
}

/** A value as a person reads it: strings as they are, the rest as indented JSON. */
export function showValue(v: unknown): string {
  if (v === undefined || v === null || v === '') return ''
  return typeof v === 'string' ? v : JSON.stringify(v, null, 2)
}

/** A field's name as a person reads it: "config.rules" is "rules". */
export function fieldLabel(field: string): string {
  return field.startsWith('config.') ? field.slice('config.'.length) : field
}

// ── the graph ──

export type Mark = 'added' | 'changed' | 'removed'

const edgeKey = (e: PipelineEdge) => `${e.from}->${e.to}`

/**
 * The graph a change makes, drawn over what it replaces: the proposed nodes
 * and edges, plus the removed ones, each marked.
 */
export function graphDiff(
  current: Pipeline | undefined,
  proposed: Pipeline | undefined,
  diff: PipelineDiff | undefined,
): {
  nodes: PipelineNode[]
  edges: PipelineEdge[]
  marks: Record<string, Mark>
  edgeMarks: Record<string, 'added' | 'removed'>
} {
  const marks: Record<string, Mark> = {}
  const edgeMarks: Record<string, 'added' | 'removed'> = {}
  for (const n of diff?.nodes_added ?? []) marks[n.id] = 'added'
  for (const n of diff?.nodes_changed ?? []) marks[n.id] = 'changed'
  for (const n of diff?.nodes_removed ?? []) marks[n.id] = 'removed'
  for (const e of diff?.edges_added ?? []) edgeMarks[edgeKey(e)] = 'added'
  for (const e of diff?.edges_removed ?? []) edgeMarks[edgeKey(e)] = 'removed'

  const nodes = [...(proposed?.nodes ?? [])]
  const removed = new Set((diff?.nodes_removed ?? []).map((n) => n.id))
  for (const n of current?.nodes ?? []) if (removed.has(n.id)) nodes.push(n)
  const edges = [...(proposed?.edges ?? [])]
  const removedEdges = new Set((diff?.edges_removed ?? []).map(edgeKey))
  for (const e of current?.edges ?? []) if (removedEdges.has(edgeKey(e))) edges.push(e)
  return { nodes, edges, marks, edgeMarks }
}

/** How many things a change touches, for its summary line. */
export function diffCounts(diff: PipelineDiff | undefined) {
  return {
    added: diff?.nodes_added?.length ?? 0,
    changed: diff?.nodes_changed?.length ?? 0,
    removed: diff?.nodes_removed?.length ?? 0,
    edges: (diff?.edges_added?.length ?? 0) + (diff?.edges_removed?.length ?? 0),
    settings: diff?.pipeline?.length ?? 0,
  }
}

// ── the test ──

export type NodeComparison = {
  id: string
  name: string
  type: string
  mark?: Mark
  before?: PipelinePreview['nodes'][number]
  after?: PipelinePreview['nodes'][number]
  /** Columns the proposed version adds and drops at this node. */
  columnsAdded: string[]
  columnsDropped: string[]
  /** Whether the rows the two versions show differ. */
  rowsDiffer: boolean
  /** Columns both versions have whose values differ, with how many rows differ. */
  valuesChanged: Record<string, number>
  /** "row:column" of every proposed cell whose value differs from the current one. */
  changedCells: Set<string>
  /** What changed at this node, in words; empty when nothing did. */
  verdict: string
  /** Whether the node fails in both versions. */
  failsInBoth: boolean
}

const ran = (s?: string) => s === 'success' || s === 'succeeded' || s === 'completed'

/**
 * The two previews side by side, node by node in the proposed pipeline's
 * order (removed nodes last), each with what the change does there.
 */
export function compareTest(test: ChangeTest, marks: Record<string, Mark>): NodeComparison[] {
  const before = new Map(test.current.nodes.map((n) => [n.node_id, n]))
  const after = new Map(test.proposed.nodes.map((n) => [n.node_id, n]))
  const ids = [
    ...test.proposed.nodes.map((n) => n.node_id),
    ...test.current.nodes.map((n) => n.node_id).filter((id) => !after.has(id)),
  ]
  return ids.map((id) => {
    const b = before.get(id)
    const a = after.get(id)
    const colsB = b?.columns ?? []
    const colsA = a?.columns ?? []
    const columnsAdded = colsA.filter((c) => !colsB.includes(c))
    const columnsDropped = colsB.filter((c) => !colsA.includes(c))
    const rowsDiffer = JSON.stringify(b?.rows ?? []) !== JSON.stringify(a?.rows ?? [])
    // Row by row, the columns both versions have: previews keep the first
    // rows in order, so the same position is the same record unless the
    // change filters or reorders them, which the row count usually shows.
    const valuesChanged: Record<string, number> = {}
    const changedCells = new Set<string>()
    const rowsB = b?.rows ?? []
    const rowsA = a?.rows ?? []
    for (let i = 0; i < Math.min(rowsA.length, rowsB.length); i++) {
      for (const c of colsA) {
        if (!colsB.includes(c)) continue
        if (JSON.stringify(rowsA[i][c]) !== JSON.stringify(rowsB[i][c])) {
          valuesChanged[c] = (valuesChanged[c] ?? 0) + 1
          changedCells.add(`${i}:${c}`)
        }
      }
    }
    const failsInBoth = a?.status === 'failed' && b?.status === 'failed'
    let verdict = ''
    if (!a) verdict = 'Removed by the change'
    else if (!b) verdict = ran(a.status) ? 'New in the change' : ''
    else if (a.status === 'failed' && b.status !== 'failed') verdict = 'Fails with the change'
    else if (b.status === 'failed' && a.status !== 'failed')
      verdict = ran(a.status) ? 'Fixed by the change' : ''
    else if (ran(a.status) && ran(b.status)) {
      const parts: string[] = []
      if (columnsAdded.length) parts.push(`adds ${columnsAdded.join(', ')}`)
      if (columnsDropped.length) parts.push(`drops ${columnsDropped.join(', ')}`)
      if (rowsB.length !== rowsA.length) parts.push(`${rowsB.length} → ${rowsA.length} rows`)
      const shown = Math.min(rowsA.length, rowsB.length)
      for (const [c, n] of Object.entries(valuesChanged))
        parts.push(`${c} differs in ${n} of ${shown} rows`)
      verdict = parts.length ? parts.join('; ') : ''
    } else if (a.status !== b.status) verdict = `${b.status} → ${a.status}`
    return {
      id,
      name: a?.name || b?.name || id,
      type: a?.type || b?.type || '',
      mark: marks[id],
      before: b,
      after: a,
      columnsAdded,
      columnsDropped,
      rowsDiffer,
      valuesChanged,
      changedCells,
      verdict,
      failsInBoth,
    }
  })
}
