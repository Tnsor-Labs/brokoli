import { useMemo, type ReactNode } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { Play } from 'lucide-react'
import { observeApi, pipelineApi, schedulerApi, type DashboardStats, type PipelineSummary } from '@brokoli/api'
import { keys, paths, useLiveConnected, useNow, useThrottledActivity } from '@brokoli/pipelines'
import { Button, Callout, Dot, IconButton, Page, PageHeader, Skeleton, errorMessage, formatDateTime, formatNumber, formatRelative } from '@brokoli/ui'
import { HeatmapGrid } from '../calendar/Heatmap'
import { buildHeatmap } from '../calendar/heatmap'
import { Stat, rateTone } from '../Stat'
import { DeadLetters } from './DeadLetters'
import { FleetTable, nextLabel } from './FleetTable'
import { Panel } from './Panel'
import { RecentRuns } from './RecentRuns'
import { useRunActions } from './actions'
import { firstLine, fleetRows, needsAttention, upcoming } from './model'
import '../observe.css'

function Sparkline({ trends }: { trends: NonNullable<DashboardStats['trends']> }) {
  const max = Math.max(1, ...trends.map((t) => t.total))
  return (
    <div className="ob-spark" role="img" aria-label={trends.map((t) => `${t.date}: ${t.total} runs, ${t.failed} failed`).join('; ')}>
      {trends.map((t) => (
        <span key={t.date} className="ob-spark-bar" title={`${t.date}: ${t.total} runs, ${t.success} succeeded, ${t.failed} failed`}>
          <i className="ob-spark-fail" style={{ height: `${(t.failed / max) * 100}%` }} />
          <i className="ob-spark-ok" style={{ height: `${(Math.max(0, t.total - t.failed) / max) * 100}%` }} />
        </span>
      ))}
    </div>
  )
}

function NeedsAttention({ failing, now }: { failing: PipelineSummary[]; now: number }) {
  const actions = useRunActions()
  if (!failing.length)
    return (
      <div className="ob-attention is-clear" role="status">
        <Dot tone="success" /> No pipeline's latest run failed.
      </div>
    )
  return (
    <Panel title="Needs attention" subtitle="Pipelines whose latest run failed" count={failing.length} tone="danger" className="ob-attention">
      <ul className="ob-rows">
        {failing.map((p) => (
          <li key={p.id} className="ob-rows-row ob-attention-row">
            <div>
              <Link className="ob-link-strong" to={paths.runs(p.id)}>
                {p.name}
              </Link>
              <span className="ob-small ob-quiet" title={formatDateTime(p.last_run_at)}>
                {' '}
                failed {formatRelative(p.last_run_at, now)}
              </span>
              {p.last_run_error && (
                <p className="ob-error-line" title={p.last_run_error}>
                  {firstLine(p.last_run_error)}
                </p>
              )}
            </div>
            <span className="ob-row-actions is-visible">
              <Link className="bk-button bk-button-ghost bk-button-sm" to={paths.runs(p.id)}>
                Open runs
              </Link>
              {actions.canRun && !p.draft && (
                <IconButton size="sm" label={`Run ${p.name} again`} disabled={actions.busy === `run:${p.id}`} onClick={() => void actions.start(p.id, p.name)}>
                  <Play size={14} aria-hidden="true" />
                </IconButton>
              )}
            </span>
          </li>
        ))}
      </ul>
    </Panel>
  )
}

function Welcome() {
  return (
    <section className="ob-welcome">
      <h2>Nothing has run yet</h2>
      <p>Three steps get the first data moving. This page fills in as soon as a pipeline runs.</p>
      <ol className="ob-steps">
        <li>
          <strong>Add a connection</strong>
          <span>Needed for databases and APIs. Files and sample data work without one.</span>
          <Link to="/connections">Connections</Link>
        </li>
        <li>
          <strong>Create a pipeline</strong>
          <span>Start from a template or an empty canvas.</span>
          <Link to="/pipelines">Pipelines</Link>
        </li>
        <li>
          <strong>Run it</strong>
          <span>Start it from the editor or the pipeline list, or give it a schedule.</span>
        </li>
      </ol>
    </section>
  )
}

function LiveState({ live }: { live: boolean }) {
  return live ? (
    <span className="ob-live">
      <Dot tone="success" pulse /> Live
    </span>
  ) : (
    <span className="ob-live" title="Figures refresh again when the connection returns, or when you reload.">
      <Dot tone="warning" /> Live updates paused, reconnecting
    </span>
  )
}

/** `banner` lets an edition add its own panels (Enterprise puts plan usage, worker health and SLA breaches there). */
export function DashboardPage({ banner }: { banner?: ReactNode } = {}) {
  const queryClient = useQueryClient()
  const now = useNow(15_000)
  const live = useLiveConnected()
  const stats = useQuery({ queryKey: ['observe', 'dashboard'], queryFn: observeApi.dashboard })
  const summary = useQuery({ queryKey: keys.summary, queryFn: pipelineApi.summary })
  const schedule = useQuery({ queryKey: keys.scheduler, queryFn: schedulerApi.status })
  const activity = useQuery({ queryKey: ['observe', 'calendar', 365], queryFn: () => observeApi.calendar(365), staleTime: 60_000 })

  // /api/dashboard is expensive on the server; at most one refresh every 5 seconds while runs are busy.
  useThrottledActivity(() => {
    void queryClient.invalidateQueries({ predicate: (q) => q.queryKey[0] === 'observe' && q.queryKey[1] !== 'calendar' })
    void queryClient.invalidateQueries({ queryKey: keys.summary })
    void queryClient.invalidateQueries({ queryKey: keys.scheduler })
  }, 5_000)
  useThrottledActivity(() => void activity.refetch(), 60_000)

  const pipelines = useMemo(() => summary.data ?? [], [summary.data])
  const s = stats.data
  const failing = useMemo(() => needsAttention(pipelines), [pipelines])
  const rows = useMemo(() => fleetRows(pipelines, s?.pipeline_rollups ?? [], schedule.data ?? []), [pipelines, s, schedule.data])
  const next = useMemo(() => upcoming(schedule.data ?? [], new Set(pipelines.map((p) => p.id)), 6), [schedule.data, pipelines])
  const heat = useMemo(() => buildHeatmap(activity.data ?? [], 365, now), [activity.data, now])
  const rate = s ? s.success_rate_24h : null
  const enabled = pipelines.filter((p) => p.enabled && !p.draft).length
  const drafts = pipelines.filter((p) => p.draft).length
  const delta = s ? s.runs_today - s.runs_yesterday : 0

  return (
    <Page wide>
      <PageHeader
        eyebrow="Observe"
        title="Dashboard"
        description="Run health across every pipeline, updated as runs start and finish."
        meta={<LiveState live={live} />}
      />
      {summary.isError && (
        <Callout tone="danger" title="Pipelines could not be loaded" action={<Button size="sm" onClick={() => void summary.refetch()}>Try again</Button>}>
          {errorMessage(summary.error)}
        </Callout>
      )}
      {stats.isError && (
        <Callout tone="danger" title="Run statistics could not be loaded" action={<Button size="sm" onClick={() => void stats.refetch()}>Try again</Button>}>
          {errorMessage(stats.error)}
        </Callout>
      )}
      {summary.isPending ? (
        <div className="ob-dash-loading">
          <div className="ob-kpis">
            {Array.from({ length: 6 }, (_, i) => (
              <Skeleton key={i} height={92} />
            ))}
          </div>
          <Skeleton height={280} />
        </div>
      ) : summary.isSuccess && !pipelines.length ? (
        <Welcome />
      ) : (
        <>
          {banner}
          {summary.isSuccess && <NeedsAttention failing={failing} now={now} />}
          <div className="ob-kpis">
            <Stat label="Failed, 24 hours" value={s ? formatNumber(s.runs_24h_failed) : '-'} foot={s ? (s.runs_24h_failed ? 'See the runs below' : 'None') : undefined} tone={s?.runs_24h_failed ? 'danger' : 'neutral'} />
            <Stat label="Running now" value={s ? formatNumber(s.runs_running) : '-'} foot={s ? (s.runs_running ? 'In flight' : 'Idle') : undefined} tone={s?.runs_running ? 'running' : 'neutral'} />
            <Stat
              label="Success, 24 hours"
              value={rate === null ? '-' : `${rate}%`}
              foot={s ? (rate === null ? 'No finished runs' : `${s.runs_24h_success} of ${s.runs_24h_finished} finished runs`) : undefined}
              tone={rateTone(rate)}
              title="Runs in flight, pending or cancelled are not counted either way."
            />
            <Stat
              label="Runs today"
              value={s ? formatNumber(s.runs_today) : '-'}
              foot={s ? (delta > 0 ? `${delta} more than yesterday` : delta < 0 ? `${-delta} fewer than yesterday` : 'Same as yesterday') : undefined}
              title="Today and yesterday by the server's clock."
            />
            <Stat
              label="Pipelines"
              value={formatNumber(pipelines.length)}
              foot={`${enabled} enabled, ${pipelines.length - enabled - drafts} paused${drafts ? `, ${drafts} draft${drafts === 1 ? '' : 's'}` : ''}`}
            />
            <Stat label="Last 7 days" value={s?.trends ? formatNumber(s.trends.reduce((n, t) => n + t.total, 0)) : '-'} foot="Runs per day">
              {s?.trends && <Sparkline trends={s.trends} />}
            </Stat>
          </div>
          <p className="ob-small ob-quiet ob-footnote">The per-pipeline table below counts each pipeline's newest 200 runs, so a very busy pipeline can be undercounted. The totals above count every run.</p>
          <div className="ob-dash-grid">
            <Panel title="Recent runs" subtitle="The newest 50 runs, grouped by pipeline" action={<Link to="/pipelines">All pipelines</Link>}>
              {s ? <RecentRuns stats={s} now={now} /> : stats.isPending ? <Skeleton height={200} /> : <p className="ob-empty-line">Unavailable.</p>}
            </Panel>
            <div className="ob-rail">
              <Panel title="Next scheduled">
                {schedule.isError ? (
                  <p className="ob-empty-line">The schedule could not be loaded: {errorMessage(schedule.error)}</p>
                ) : !next.length ? (
                  <p className="ob-empty-line">{schedule.isPending ? 'Loading' : 'No pipeline has a schedule.'}</p>
                ) : (
                  <ul className="ob-rows">
                    {next.map((e) => (
                      <li key={e.pipeline_id} className="ob-rows-row ob-split-row">
                        <Link to={paths.runs(e.pipeline_id)}>{e.pipeline_name}</Link>
                        <span className="ob-small ob-quiet" title={formatDateTime(e.next_run)}>
                          {nextLabel(e.next_run, now)}
                        </span>
                      </li>
                    ))}
                  </ul>
                )}
              </Panel>
              {!!s?.top_failing?.length && (
                <Panel title="Most failed runs" subtitle={`Per pipeline, in the last ${s.top_failing_window_hours ?? 24} hours`}>
                  <ul className="ob-rows">
                    {s.top_failing.map((t) => (
                      <li key={t.pipeline_id} className="ob-rows-row ob-split-row">
                        <Link to={paths.runs(t.pipeline_id)}>{t.name || pipelines.find((p) => p.id === t.pipeline_id)?.name || t.pipeline_id}</Link>
                        <span className="ob-small ob-danger">{t.fail_count} failed</span>
                      </li>
                    ))}
                  </ul>
                </Panel>
              )}
              <DeadLetters now={now} />
            </div>
          </div>
          {summary.isSuccess && (
            <Panel title="Pipelines" subtitle={`${pipelines.length} in total`} action={<Link to="/pipelines">Manage pipelines</Link>}>
              <FleetTable rows={rows} now={now} />
            </Panel>
          )}
          <Panel title="Activity" subtitle="Runs per day over the last year" action={<Link to="/calendar">Open the calendar</Link>}>
            {activity.isError ? (
              <p className="ob-empty-line">Run history could not be loaded: {errorMessage(activity.error)}</p>
            ) : activity.isPending ? (
              <Skeleton height={110} />
            ) : (
              <HeatmapGrid map={heat} label="Runs per day over the last year" size="sm" />
            )}
          </Panel>
        </>
      )}
    </Page>
  )
}
