import type { NodeRun, PipelineEdge, PipelineNode, Run } from '@brokoli/api'
import { ACTIVE } from '../keys'

/*
 * Layout for the full run timeline, kept free of React so it can be tested.
 * Times are epoch milliseconds; offsets are milliseconds from the origin of
 * the run's window.
 */

/** Id of the pseudo-row that stands for the run itself (run-level logs and totals). */
export const RUN_ROW = '__run__'

const active = (status: string | null | undefined) => ACTIVE.has((status ?? '').toLowerCase())

/** Go's zero time marshals as 0001-01-01; that, null and unparseable values are all "absent". */
export function epoch(value: string | null | undefined): number | null {
  if (!value) return null
  const t = new Date(value).getTime()
  if (Number.isNaN(t) || new Date(t).getUTCFullYear() <= 1) return null
  return t
}

export type Span = { start: number; end: number; duration: number }

/**
 * Where an attempt sits in time. A running attempt reports a duration of 0,
 * so its bar grows with the clock instead. An attempt that never started
 * (skipped, pending) has no span and draws no bar.
 */
export function attemptSpan(nr: Pick<NodeRun, 'started_at' | 'duration_ms' | 'status'>, now: number): Span | null {
  const start = epoch(nr.started_at)
  if (start === null) return null
  const duration = nr.duration_ms > 0 ? nr.duration_ms : active(nr.status) ? Math.max(0, now - start) : 0
  return { start, end: start + duration, duration }
}

export type Window = { origin: number; end: number; total: number }

/**
 * The time range the axis covers. The origin is the run's start, or the
 * first node start when the run has none recorded; null when nothing has
 * started at all. The total is at least 1ms, so a run in which everything
 * took 0ms still has a valid scale.
 */
export function runWindow(run: Pick<Run, 'started_at' | 'finished_at' | 'status' | 'node_runs'>, now: number): Window | null {
  const spans = (run.node_runs ?? []).map((nr) => attemptSpan(nr, now)).filter((s): s is Span => s !== null)
  const origin = epoch(run.started_at) ?? (spans.length ? Math.min(...spans.map((s) => s.start)) : null)
  if (origin === null) return null
  const end = Math.max(origin, epoch(run.finished_at) ?? 0, active(run.status) ? now : 0, ...spans.map((s) => s.end))
  return { origin, end, total: Math.max(1, end - origin) }
}

/** Wall-clock duration of the run: to its finish, to now while active, else to the last node's end. */
export function runElapsed(run: Pick<Run, 'started_at' | 'finished_at' | 'status' | 'node_runs'>, now: number): number | null {
  const win = runWindow(run, now)
  if (!win) return null
  const start = epoch(run.started_at) ?? win.origin
  const finished = epoch(run.finished_at)
  if (finished !== null) return Math.max(0, finished - start)
  return active(run.status) ? Math.max(0, now - start) : win.end - start
}

/*
 * Axis steps: 1, 2 and 5 times a power of ten while under a second, then
 * clock-friendly multiples of seconds, minutes and hours, then whole days.
 */
const SECOND = 1000
const MINUTE = 60 * SECOND
const HOUR = 60 * MINUTE
const DAY = 24 * HOUR
const STEPS = [
  1, 2, 5, 10, 20, 50, 100, 200, 500,
  SECOND, 2 * SECOND, 5 * SECOND, 10 * SECOND, 15 * SECOND, 30 * SECOND,
  MINUTE, 2 * MINUTE, 5 * MINUTE, 10 * MINUTE, 15 * MINUTE, 30 * MINUTE,
  HOUR, 2 * HOUR, 3 * HOUR, 6 * HOUR, 12 * HOUR, DAY,
]

/** The smallest step that is at least `minStepMs`. */
export function niceStep(minStepMs: number): number {
  for (const step of STEPS) if (step >= minStepMs) return step
  return Math.ceil(minStepMs / DAY) * DAY
}

/** Tick offsets for a track `trackPx` wide showing `total` ms, keeping labels at least `minGapPx` apart. */
export function ticks(total: number, trackPx: number, minGapPx = 88): { step: number; offsets: number[] } {
  if (!(total > 0) || !(trackPx > 0)) return { step: 0, offsets: [0] }
  const step = niceStep((total * minGapPx) / trackPx)
  const offsets: number[] = []
  for (let t = 0; t <= total; t += step) offsets.push(t)
  return { step, offsets }
}

export type Row = {
  id: string
  name: string
  type?: string
  /** Oldest attempt first. */
  attempts: NodeRun[]
  firstStart: number | null
  /** Ran in this run but is not in the current definition (removed since). */
  orphan: boolean
}

/**
 * One row per node of the definition plus one per node the run executed that
 * the definition no longer has, ordered by first start. Rows that never
 * started go last, in definition order.
 */
export function timelineRows(nodes: Pick<PipelineNode, 'id' | 'name' | 'type'>[], nodeRuns: NodeRun[] | null | undefined): Row[] {
  const byNode = new Map<string, NodeRun[]>()
  for (const nr of nodeRuns ?? []) {
    const list = byNode.get(nr.node_id)
    if (list) list.push(nr)
    else byNode.set(nr.node_id, [nr])
  }
  for (const list of byNode.values()) list.sort((a, b) => (a.attempt ?? 0) - (b.attempt ?? 0))
  const known = new Set(nodes.map((n) => n.id))
  const base: Omit<Row, 'attempts' | 'firstStart'>[] = [
    ...nodes.map((n) => ({ id: n.id, name: n.name || n.id, type: n.type, orphan: false })),
    ...[...byNode.keys()].filter((id) => !known.has(id)).map((id) => ({ id, name: id, orphan: true })),
  ]
  const rows = base.map((r) => {
    const attempts = byNode.get(r.id) ?? []
    const starts = attempts.map((a) => epoch(a.started_at)).filter((t): t is number => t !== null)
    return { ...r, attempts, firstStart: starts.length ? Math.min(...starts) : null }
  })
  return rows.sort((a, b) => {
    if (a.firstStart === null || b.firstStart === null) return a.firstStart === b.firstStart ? 0 : a.firstStart === null ? 1 : -1
    return a.firstStart - b.firstStart
  })
}

export function upstreamOf(nodeId: string, edges: Pick<PipelineEdge, 'from' | 'to'>[] | null | undefined): string[] {
  return [...new Set((edges ?? []).filter((e) => e.to === nodeId).map((e) => e.from))]
}

/** Rows reported by the sink nodes that ran; null when no sink ran. Summing every node would count each row once per node it passed. */
export function rowsWritten(rows: Row[]): number | null {
  const sinks = rows.filter((r) => r.type?.startsWith('sink_') && r.attempts.length)
  if (!sinks.length) return null
  return sinks.reduce((sum, r) => sum + (r.attempts[r.attempts.length - 1].row_count || 0), 0)
}

export function retryCount(rows: Row[]): number {
  return rows.reduce((n, r) => n + Math.max(0, r.attempts.length - 1), 0)
}

/**
 * Log lines grouped by attempt (0 is the first try); a single unlabelled
 * group when no line carries an attempt. The server omits `attempt` when it
 * is 0, so once any line has one, a missing value means the first try.
 */
export function groupByAttempt<T extends { attempt?: number | null }>(lines: T[]): { attempt: number | null; lines: T[] }[] {
  if (!lines.some((l) => typeof l.attempt === 'number' && l.attempt > 0)) return [{ attempt: null, lines }]
  const groups = new Map<number, T[]>()
  for (const l of lines) {
    const key = typeof l.attempt === 'number' ? l.attempt : 0
    const list = groups.get(key)
    if (list) list.push(l)
    else groups.set(key, [l])
  }
  return [...groups].sort(([a], [b]) => a - b).map(([attempt, group]) => ({ attempt, lines: group }))
}
