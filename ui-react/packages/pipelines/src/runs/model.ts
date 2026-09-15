import type { LogEntry, NodeRun, Run, RunAttribution } from '@brokoli/api'
import { toDate } from '@brokoli/ui'
import { ACTIVE } from '../keys'

export const isActive = (status: string | undefined | null) => ACTIVE.has((status ?? '').toLowerCase())

const TRIGGER_KIND_LABEL: Record<string, string> = {
  schedule: 'a schedule',
  webhook: 'a webhook',
  dependency: 'a dependency',
  backfill: 'a backfill',
  api_token: 'an API token',
  retry: 'a retry',
}

/** Who or what started the run, in words: a person's name, or the mechanism. Null when not recorded. */
export function triggeredByLabel(t: RunAttribution | null | undefined): string | null {
  if (!t) return null
  if (t.kind === 'user') return t.user_name || 'a user'
  if (t.kind === 'api_token') return t.token_name ? `${t.token_name} (API token)` : 'an API token'
  return TRIGGER_KIND_LABEL[t.kind] ?? t.kind
}

/** The latest attempt of each node: the one that decided the node's outcome. */
export function primaryAttempts(nodeRuns: NodeRun[] | null | undefined): Map<string, NodeRun> {
  const out = new Map<string, NodeRun>()
  for (const nr of nodeRuns ?? []) {
    const seen = out.get(nr.node_id)
    if (!seen || (nr.attempt ?? 0) >= (seen.attempt ?? 0)) out.set(nr.node_id, nr)
  }
  return out
}

export function attemptsByNode(nodeRuns: NodeRun[] | null | undefined): Map<string, NodeRun[]> {
  const out = new Map<string, NodeRun[]>()
  for (const nr of nodeRuns ?? []) out.set(nr.node_id, [...(out.get(nr.node_id) ?? []), nr])
  for (const list of out.values()) list.sort((a, b) => (a.attempt ?? 0) - (b.attempt ?? 0))
  return out
}

export function nodeStatuses(run: Run | undefined): Record<string, string> {
  return Object.fromEntries([...primaryAttempts(run?.node_runs)].map(([id, nr]) => [id, nr.status]))
}

export function totalRows(run: Run | undefined) {
  let sum = 0
  for (const nr of primaryAttempts(run?.node_runs).values()) sum += nr.row_count || 0
  return sum
}

export function runDuration(run: Pick<Run, 'started_at' | 'finished_at' | 'status'>, now: number): number | null {
  const start = toDate(run.started_at)
  if (!start) return null
  const end = toDate(run.finished_at)
  if (end) return end.getTime() - start.getTime()
  return isActive(run.status) ? now - start.getTime() : null
}

/*
 * Merges the live log tail into the history already on screen.
 *
 * The live key holds only the newest 200 lines with second-precision
 * timestamps and four fields, so replacing the history with it (what the
 * Svelte page did) cut long runs down to their last 200 lines and, for an
 * evicted run, blanked the view. Here the tail is matched against what is
 * displayed by (second, node, level, message) as a multiset: lines already
 * shown are consumed, anything left over is new and appended.
 */
export type LiveLine = { node_id?: string; level?: string; message?: string; timestamp?: string }

function signature(e: { timestamp?: string; node_id?: string; level?: string; message?: string }) {
  const t = toDate(e.timestamp)
  return `${t ? Math.floor(t.getTime() / 1000) : e.timestamp}|${e.node_id ?? ''}|${e.level ?? ''}|${e.message ?? ''}`
}

export function mergeLiveTail(shown: LogEntry[], tail: LiveLine[] | null | undefined, runId: string): LogEntry[] {
  if (!tail?.length) return shown
  const counts = new Map<string, number>()
  // Only the newest part of the history can overlap the tail.
  for (const e of shown.slice(-tail.length - 50)) counts.set(signature(e), (counts.get(signature(e)) ?? 0) + 1)
  const fresh: LogEntry[] = []
  for (const line of tail) {
    const s = signature(line)
    const c = counts.get(s) ?? 0
    if (c > 0) counts.set(s, c - 1)
    else
      fresh.push({
        run_id: runId,
        node_id: line.node_id ?? '',
        level: line.level ?? 'info',
        message: line.message ?? '',
        timestamp: line.timestamp ?? '',
      })
  }
  return fresh.length ? [...shown, ...fresh] : shown
}

export function chronicleLabel(eventType: string) {
  return eventType.replace(/^run\./, '').replace(/^attempt\./, 'attempt ').replace(/^retry\./, 'retry ').replaceAll('_', ' ')
}

export function chronicleDetail(event: { event_type: string; payload: Record<string, unknown> | null }) {
  const p = event.payload ?? {}
  if (typeof p.error === 'string' && p.error) return p.error
  if (typeof p.backoff_ms === 'number') return `Waiting ${p.backoff_ms}ms before the next attempt`
  if (typeof p.status === 'string' && p.status) return `Outcome recorded as ${p.status}`
  if (event.event_type === 'run.recovery_started') return 'The server began recovering this run after a restart'
  if (event.event_type === 'run.recovery_completed') return 'Recovery finished'
  return 'Execution fact recorded'
}
