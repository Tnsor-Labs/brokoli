import type { DashboardRun, DashboardStats, DeadLetter, PipelineSummary, SchedulerEntry } from '@brokoli/api'
import { ACTIVE, FAILURE } from '@brokoli/pipelines'
import { toDate } from '@brokoli/ui'

const lower = (s: string | undefined | null) => (s ?? '').toLowerCase()
const time = (s: string | undefined | null) => toDate(s)?.getTime() ?? 0

export function firstLine(text: string | undefined | null, max = 160): string {
  const line = (text ?? '').split('\n', 1)[0].trim()
  return line.length > max ? `${line.slice(0, max - 1)}…` : line
}

/**
 * Share of finished runs that succeeded, rounded down so a single failure
 * never displays as 100%. Null when nothing finished: that is "no data",
 * not "perfect". The server's own rate counts in-flight and cancelled runs
 * as failures and reports 100 for an empty window, so it is not used.
 */
export function finishedRate(success: number, failed: number): number | null {
  const finished = success + failed
  return finished > 0 ? Math.floor((success * 100) / finished) : null
}

/*
 * Pipelines whose latest run failed. Read from the pipeline summary, which
 * carries each pipeline's newest run, so a pipeline that failed and then
 * recovered is not listed (the previous dashboard scanned a 50-run sample
 * for any failure and kept recovered pipelines until the sample rolled).
 */
export function needsAttention(summary: PipelineSummary[]): PipelineSummary[] {
  return summary.filter((p) => FAILURE.has(lower(p.last_run_status))).sort((a, b) => time(b.last_run_at) - time(a.last_run_at))
}

export type RunGroup = { pipelineId: string; name: string; runs: DashboardRun[] }

/** Recent runs grouped by pipeline, ordered by each pipeline's newest run. */
export function groupRecent(runs: DashboardRun[], limit: number): { groups: RunGroup[]; hidden: number } {
  const byPipeline = new Map<string, RunGroup>()
  for (const run of runs) {
    const group = byPipeline.get(run.pipeline_id)
    if (group) group.runs.push(run)
    else byPipeline.set(run.pipeline_id, { pipelineId: run.pipeline_id, name: run.pipeline_name || run.pipeline_id, runs: [run] })
  }
  const all = [...byPipeline.values()]
  return { groups: all.slice(0, limit), hidden: Math.max(0, all.length - limit) }
}

type Rollup = NonNullable<DashboardStats['pipeline_rollups']>[number]

export type FleetRow = {
  pipeline: PipelineSummary
  rollup?: Rollup
  next?: string
  /** Over the newest 200 runs of the pipeline, finished runs only. */
  health: number | null
  rank: number
}

export function fleetRows(summary: PipelineSummary[], rollups: Rollup[], schedule: SchedulerEntry[]): FleetRow[] {
  const rollupById = new Map(rollups.map((r) => [r.pipeline_id, r]))
  // Keyed by id: the previous dashboard keyed next runs by name, so two pipelines with one name shared a value.
  const nextById = new Map(schedule.map((s) => [s.pipeline_id, s.next_run]))
  const rows = summary.map((p): FleetRow => {
    const rollup = rollupById.get(p.id)
    const running = p.runs_running > 0 || (rollup?.running ?? 0) > 0 || ACTIVE.has(lower(p.last_run_status))
    const rank = FAILURE.has(lower(p.last_run_status)) ? 0 : running ? 1 : p.enabled && !p.draft ? 2 : 3
    const scheduled = p.enabled && !p.draft && Boolean(p.schedule)
    return { pipeline: p, rollup, next: scheduled ? nextById.get(p.id) : undefined, health: finishedRate(p.runs_success, p.runs_failed), rank }
  })
  return rows.sort((a, b) => a.rank - b.rank || a.pipeline.name.localeCompare(b.pipeline.name) || a.pipeline.id.localeCompare(b.pipeline.id))
}

/*
 * Next scheduled runs. The scheduler endpoint is not scoped to the caller's
 * organisation, so entries are kept only for pipelines this user can see in
 * the pipeline summary.
 */
export function upcoming(schedule: SchedulerEntry[], visible: Set<string>, limit: number): SchedulerEntry[] {
  return schedule
    .filter((s) => visible.has(s.pipeline_id) && toDate(s.next_run))
    .sort((a, b) => time(a.next_run) - time(b.next_run))
    .slice(0, limit)
}

export type DeadLetterGroup = {
  key: string
  pipelineId: string
  pipelineName: string
  nodeName: string
  error: string
  fullError: string
  ids: string[]
  newest: string
}

/** Dead letters with the same pipeline, node and first error line are one group. */
export function groupDeadLetters(entries: DeadLetter[]): DeadLetterGroup[] {
  const groups = new Map<string, DeadLetterGroup>()
  for (const e of entries) {
    const error = firstLine(e.error, 90)
    const key = `${e.pipeline_id}|${e.node_name}|${error}`
    const group = groups.get(key)
    if (group) {
      group.ids.push(e.id)
      if (time(e.created_at) > time(group.newest)) group.newest = e.created_at
    } else {
      groups.set(key, {
        key,
        pipelineId: e.pipeline_id,
        pipelineName: e.pipeline_name || e.pipeline_id,
        nodeName: e.node_name,
        error,
        fullError: e.error,
        ids: [e.id],
        newest: e.created_at,
      })
    }
  }
  return [...groups.values()].sort((a, b) => time(b.newest) - time(a.newest))
}
