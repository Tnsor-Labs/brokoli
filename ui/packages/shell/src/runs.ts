import { toDate } from '@brokoli/ui'

/** A run as it appears in the organisation's live dashboard state. */
export interface LiveRun {
  run_id: string
  pipeline_id: string
  pipeline_name?: string
  status: string
  started_at?: string
  finished_at?: string
  error?: string
}

export interface LiveDashboard {
  runs_running?: number
  running_run_ids?: string[] | null
  recent_runs?: LiveRun[] | null
}

const TERMINAL = new Set(['success', 'succeeded', 'completed', 'failed', 'cancelled', 'canceled'])
export const isTerminal = (status: string) => TERMINAL.has(status.toLowerCase())

/*
 * Runs that finished between two snapshots of live state.
 *
 * `seen` maps run id to the last status shown to the user. A run counts as
 * finished when it is terminal now and was not terminal before, or when it
 * was never seen and finished after `since` (it started and ended between two
 * snapshots). The previous interface compared the 24-hour success and
 * failure counters instead, which missed completions whenever old runs
 * aged out between snapshots, never reported cancellations, and could not
 * say which pipeline had finished.
 */
export function finishedRuns(seen: Map<string, string>, runs: LiveRun[], since: number): LiveRun[] {
  const out: LiveRun[] = []
  for (const run of runs) {
    if (!isTerminal(run.status)) continue
    const before = seen.get(run.run_id)
    if (before !== undefined) {
      if (!isTerminal(before)) out.push(run)
    } else if ((toDate(run.finished_at)?.getTime() ?? 0) >= since) out.push(run)
  }
  return out
}

export function remember(seen: Map<string, string>, runs: LiveRun[]) {
  for (const run of runs) seen.set(run.run_id, run.status)
  // The live list holds a few recent runs; keep the memory from growing without bound.
  if (seen.size > 500) {
    const keep = new Set(runs.map((r) => r.run_id))
    for (const id of seen.keys()) if (!keep.has(id)) seen.delete(id)
  }
}
