import { useEffect, useLayoutEffect, useMemo, useRef, useState, type CSSProperties, type KeyboardEvent } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { Clock, X, ZoomIn, ZoomOut } from 'lucide-react'
import { observeApi, pipelineApi, runApi, type LogEntry, type NodeStats, type Pipeline, type Run } from '@brokoli/api'
import {
  Badge,
  Button,
  Callout,
  Dot,
  EMPTY,
  EmptyState,
  IconButton,
  Page,
  PageHeader,
  Select,
  Skeleton,
  Spinner,
  StatusBadge,
  cx,
  errorMessage,
  formatDateTime,
  formatDuration,
  formatNumber,
  formatTime,
  statusMeta,
} from '@brokoli/ui'
import { keys, paths } from '../keys'
import { useNow, useRunActivity } from '../live'
import {
  RUN_ROW,
  attemptSpan,
  epoch,
  groupByAttempt,
  retryCount,
  rowsWritten,
  runElapsed,
  runWindow,
  ticks,
  timelineRows,
  upstreamOf,
  type Row,
  type Window,
} from './fullTimeline'
import { isActive } from './model'
import './runs.css'
import './gantt.css'

type Stat = NonNullable<NodeStats['nodes']>[string]

const LABEL_W = 240
const MIN_ZOOM = 1
const MAX_ZOOM = 64
const LEVEL_RANK: Record<string, number> = { debug: 0, info: 1, warning: 2, warn: 2, error: 3 }
const stampFmt = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit', fractionalSecondDigits: 3, hourCycle: 'h23' })
const stamp = (ts: string) => {
  const d = new Date(ts)
  return Number.isNaN(d.getTime()) ? ts : stampFmt.format(d)
}
const pct = (ms: number, total: number) => `${(ms / total) * 100}%`
const offset = (t: number, win: Window) => `+${formatDuration(Math.max(0, t - win.origin))}`
const retries = (n: number) => `${n} ${n === 1 ? 'retry' : 'retries'}`
const lastOf = (row: Row | undefined) => row?.attempts[row.attempts.length - 1]

/*
 * Full execution timeline of one run: every node on a zoomable time axis,
 * every attempt drawn, recent typical durations marked, and a detail panel
 * with what the node waited for and its logs.
 *
 * Node names and edges come from the pipeline's current definition; core
 * has no route that returns the version a run used. Nodes the run executed
 * that the definition no longer has are still listed, by id.
 */
export function FullTimelinePage({ pipelineId, runId }: { pipelineId: string; runId: string }) {
  const queryClient = useQueryClient()
  const pipeline = useQuery({ queryKey: keys.pipeline(pipelineId), queryFn: () => pipelineApi.get(pipelineId) })
  const run = useQuery({ queryKey: keys.run(runId), queryFn: () => runApi.get(runId) })
  const logs = useQuery({ queryKey: keys.runLogs(runId), queryFn: () => runApi.logs(runId) })
  // Optional: typical durations from recent successful runs. A failure only hides the markers.
  const stats = useQuery({ queryKey: keys.nodeStats(pipelineId), queryFn: () => observeApi.nodeStats(pipelineId, 10), staleTime: 60_000 })
  const live = isActive(run.data?.status)
  const now = useNow(live ? 1000 : 60_000)

  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: keys.run(runId), exact: true })
    void queryClient.invalidateQueries({ queryKey: keys.runLogs(runId) })
  }
  useRunActivity(refresh, live)
  // One last reload when the run finishes: the final durations and log lines arrive with it.
  const wasLive = useRef(live)
  useEffect(() => {
    if (wasLive.current && !live) void queryClient.invalidateQueries({ queryKey: keys.runLogs(runId) })
    wasLive.current = live
  }, [live, queryClient, runId])

  const r = run.data
  const p = pipeline.data
  const rows = useMemo(() => timelineRows(p?.nodes ?? [], r?.node_runs), [p?.nodes, r?.node_runs])
  const win = r ? runWindow(r, now) : null
  const hasChart = Boolean(win)
  const [selected, setSelected] = useState<string | null>(null)

  // Zoom: the track is `zoom` times the visible width; the scroll position is kept anchored under the pointer.
  const [zoom, setZoom] = useState(1)
  const scroller = useRef<HTMLDivElement>(null)
  const [viewport, setViewport] = useState(0)
  const trackPx = Math.max(200, (viewport - LABEL_W) * zoom)
  const anchor = useRef<{ fraction: number; px: number } | null>(null)

  useLayoutEffect(() => {
    const el = scroller.current
    if (!el) return
    const observer = new ResizeObserver(() => setViewport(el.clientWidth))
    observer.observe(el)
    setViewport(el.clientWidth)
    return () => observer.disconnect()
  }, [hasChart])

  useLayoutEffect(() => {
    const el = scroller.current
    const a = anchor.current
    if (!el || !a) return
    el.scrollLeft = a.fraction * trackPx - a.px
    anchor.current = null
  }, [trackPx])

  const applyZoom = (next: number, pointerPx?: number) => {
    const el = scroller.current
    if (!el) return
    const clamped = Math.min(MAX_ZOOM, Math.max(MIN_ZOOM, next))
    const px = pointerPx ?? (el.clientWidth - LABEL_W) / 2
    anchor.current = { fraction: (el.scrollLeft + px) / trackPx, px }
    setZoom(clamped)
  }
  const zoomTo = useRef(applyZoom)
  zoomTo.current = applyZoom
  const zoomNow = useRef(zoom)
  zoomNow.current = zoom

  // Ctrl or Cmd with the wheel (and trackpad pinch) zooms. React's wheel listener is passive, so this one is native.
  useEffect(() => {
    const el = scroller.current
    if (!el) return
    const onWheel = (e: WheelEvent) => {
      if (!e.ctrlKey && !e.metaKey) return
      e.preventDefault()
      const px = e.clientX - el.getBoundingClientRect().left - LABEL_W
      if (px < 0) return
      const delta = Math.max(-50, Math.min(50, e.deltaY))
      zoomTo.current(zoomNow.current * Math.exp(-delta * 0.005), px)
    }
    el.addEventListener('wheel', onWheel, { passive: false })
    return () => el.removeEventListener('wheel', onWheel)
  }, [hasChart])

  const fit = () => {
    anchor.current = null
    setZoom(1)
    if (scroller.current) scroller.current.scrollLeft = 0
  }

  const rowRefs = useRef(new Map<string, HTMLButtonElement>())
  const order = [RUN_ROW, ...rows.map((row) => row.id)]
  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return
    e.preventDefault()
    const i = selected ? order.indexOf(selected) : -1
    const next = order[Math.min(order.length - 1, Math.max(0, i + (e.key === 'ArrowDown' ? 1 : -1)))]
    setSelected(next)
    rowRefs.current.get(next)?.focus()
  }
  const setRef = (id: string) => (el: HTMLButtonElement | null) => {
    if (el) rowRefs.current.set(id, el)
    else rowRefs.current.delete(id)
  }

  const crumbs = (
    <nav className="bk-crumbs" aria-label="Breadcrumb">
      <Link to={paths.list}>Pipelines</Link>
      <span aria-hidden="true">/</span>
      <Link to={paths.runs(pipelineId, runId)}>{p?.name ?? 'Runs'}</Link>
      <span aria-hidden="true">/</span>
      <span aria-current="page">Timeline of run {runId.slice(0, 8)}</span>
    </nav>
  )

  if (run.isError)
    return (
      <Page wide>
        {crumbs}
        <Callout
          tone="danger"
          title="This run could not be loaded"
          action={
            <Link className="bk-button bk-button-secondary bk-button-sm" to={paths.runs(pipelineId)}>
              Back to runs
            </Link>
          }
        >
          {errorMessage(run.error)}
        </Callout>
      </Page>
    )

  const elapsed = r ? runElapsed(r, now) : null
  const started = rows.filter((row) => row.firstStart !== null).length
  const written = rowsWritten(rows)
  const orphans = rows.filter((row) => row.orphan).length
  const statFor = (id: string): Stat | undefined => stats.data?.nodes?.[id]
  const showMarks = Boolean(stats.data?.nodes && Object.keys(stats.data.nodes).length)
  const axis = win ? ticks(win.total, trackPx) : null
  const gridStyle = { '--label-w': `${LABEL_W}px`, '--track-w': `${trackPx}px`, width: LABEL_W + trackPx } as CSSProperties

  return (
    <Page wide className="bk-gantt-page">
      {crumbs}
      <PageHeader
        eyebrow="Execution timeline"
        title={p?.name ?? (pipeline.isError ? 'Run timeline' : <Skeleton width={260} height={32} />)}
        description={`Run ${runId.slice(0, 8)}: when each node ran, how long it took, what it waited for, and its logs.`}
        meta={
          r && (
            <>
              <StatusBadge status={r.status} />
              {r.trigger && <Badge tone="neutral">{r.trigger}</Badge>}
              {r.pipeline_version !== undefined && r.pipeline_version > 0 && <Badge tone="neutral">Version {r.pipeline_version}</Badge>}
            </>
          )
        }
        actions={
          <Link className="bk-button bk-button-secondary bk-button-md" to={paths.runs(pipelineId, runId)}>
            Back to the run
          </Link>
        }
      />

      {r && (
        <dl className="bk-gantt-metrics">
          <div>
            <dt>Duration</dt>
            <dd>{formatDuration(elapsed)}</dd>
          </div>
          <div>
            <dt>Nodes run</dt>
            <dd>
              {started} of {rows.length}
            </dd>
          </div>
          <div>
            <dt>Retries</dt>
            <dd>{retryCount(rows)}</dd>
          </div>
          <div title="Rows reported by the sink nodes that ran. Adding up every node would count each row once per node it passed through.">
            <dt>Rows written</dt>
            <dd>{written === null ? EMPTY : formatNumber(written, { compact: true })}</dd>
          </div>
          {r.trace_id && (
            <div>
              <dt>Trace</dt>
              <dd className="bk-mono" title={r.trace_id}>
                {r.trace_id.slice(0, 16)}
              </dd>
            </div>
          )}
        </dl>
      )}

      {(pipeline.isError || orphans > 0) && (
        <div className="bk-gantt-notes">
          {pipeline.isError && (
            <Callout tone="warning" title="The pipeline definition could not be loaded">
              {errorMessage(pipeline.error)} Nodes are listed by id, and what each node waited for is unknown.
            </Callout>
          )}
          {orphans > 0 && (
            <Callout tone="info">
              {orphans === 1 ? 'One node' : `${orphans} nodes`} this run executed {orphans === 1 ? 'is' : 'are'} no longer in the pipeline, so{' '}
              {orphans === 1 ? 'it is' : 'they are'} listed by id. Names and dependencies shown are those of the current definition.
            </Callout>
          )}
        </div>
      )}

      {run.isPending ? (
        <div className="bk-gantt-chart" aria-busy="true">
          <div className="bk-gantt-notes" style={{ padding: 18 }}>
            {Array.from({ length: 6 }, (_, i) => (
              <Skeleton key={i} height={24} />
            ))}
          </div>
        </div>
      ) : !win || !r ? (
        <EmptyState icon={<Clock size={20} aria-hidden="true" />} title={live ? 'No node has started yet' : 'This run has no timing data'}>
          {live ? 'Bars appear here as nodes start.' : 'No node recorded a start time, so there is nothing to place on a timeline.'}
        </EmptyState>
      ) : (
        <>
          <div className="bk-gantt-toolbar">
            <div className="bk-gantt-legend" aria-label="Legend">
              {(['success', 'running', 'failed', 'pending', 'cancelled'] as const).map((s) => (
                <span key={s}>
                  <Dot tone={statusMeta(s).tone} /> {statusMeta(s).label}
                </span>
              ))}
              <span>
                <Dot tone="neutral" /> Earlier attempt (faded)
              </span>
              {showMarks && (
                <>
                  <span>
                    <i className="bk-gantt-mark is-avg" aria-hidden="true" /> Average
                  </span>
                  <span>
                    <i className="bk-gantt-mark is-p95" aria-hidden="true" /> 95th percentile
                  </span>
                </>
              )}
            </div>
            <div className="bk-gantt-zoom">
              <span className="bk-gantt-hint">Ctrl or Cmd and scroll to zoom</span>
              <IconButton size="sm" label="Zoom out" onClick={() => applyZoom(zoom / 2)} disabled={zoom <= MIN_ZOOM}>
                <ZoomOut size={15} aria-hidden="true" />
              </IconButton>
              <output aria-label="Zoom level">{zoom < 10 ? zoom.toFixed(1).replace(/\.0$/, '') : Math.round(zoom)}x</output>
              <IconButton size="sm" label="Zoom in" onClick={() => applyZoom(zoom * 2)} disabled={zoom >= MAX_ZOOM}>
                <ZoomIn size={15} aria-hidden="true" />
              </IconButton>
              <Button size="sm" variant="ghost" onClick={fit} disabled={zoom === 1}>
                Fit
              </Button>
            </div>
          </div>

          <div className={cx('bk-gantt-body', selected && 'has-detail')}>
            <div className="bk-gantt-chart">
              <div ref={scroller} className="bk-gantt-scroll" onKeyDown={onKeyDown}>
                <div className="bk-gantt-inner" style={gridStyle}>
                  <div className="bk-gantt-axis" aria-hidden="true">
                    <span>Time from start</span>
                    <span className="bk-gantt-ticks">
                      {axis!.offsets.map((t) => (
                        <span key={t} style={{ left: pct(t, win.total) }}>
                          {t === 0 ? '0' : formatDuration(t)}
                        </span>
                      ))}
                    </span>
                  </div>
                  <div className="bk-gantt-gridlines" aria-hidden="true">
                    {axis!.offsets.slice(1).map((t) => (
                      <i key={t} style={{ left: pct(t, win.total) }} />
                    ))}
                  </div>

                  <button
                    ref={setRef(RUN_ROW)}
                    type="button"
                    className={cx('bk-gantt-row', 'is-run', selected === RUN_ROW && 'is-selected')}
                    aria-pressed={selected === RUN_ROW}
                    onClick={() => setSelected(selected === RUN_ROW ? null : RUN_ROW)}
                  >
                    <span className="bk-gantt-label">
                      <span className="bk-gantt-name">
                        <strong>Run</strong>
                        <small>Totals and run-level logs</small>
                      </span>
                      <span className="bk-gantt-dur">{formatDuration(elapsed)}</span>
                    </span>
                    <span className="bk-gantt-track">
                      <i
                        className={cx('bk-gantt-bar', 'is-run', `bk-tone-${statusMeta(r.status).tone}`, live && 'is-live')}
                        style={{ left: pct((epoch(r.started_at) ?? win.origin) - win.origin, win.total), width: pct(elapsed ?? 0, win.total) }}
                      />
                    </span>
                  </button>

                  {rows.map((row) => {
                    const primary = lastOf(row)
                    const s = primary ? attemptSpan(primary, now) : null
                    const n = row.attempts.length - 1
                    return (
                      <button
                        key={row.id}
                        ref={setRef(row.id)}
                        type="button"
                        className={cx('bk-gantt-row', selected === row.id && 'is-selected', primary?.status === 'failed' && 'is-failed')}
                        aria-pressed={selected === row.id}
                        onClick={() => setSelected(selected === row.id ? null : row.id)}
                      >
                        <span className="bk-gantt-label">
                          <span className="bk-gantt-name">
                            <strong title={row.name}>{row.name}</strong>
                            {row.orphan && <small>Not in the current definition</small>}
                          </span>
                          {n > 0 && <em className="bk-gantt-retry">{retries(n)}</em>}
                          <span className="bk-gantt-dur">{s ? formatDuration(s.duration) : ''}</span>
                        </span>
                        <Bars row={row} win={win} now={now} live={live} stat={statFor(row.id)} />
                      </button>
                    )
                  })}
                </div>
              </div>
            </div>

            {selected && (
              <Detail
                key={selected}
                selected={selected}
                rows={rows}
                run={r}
                pipeline={p}
                win={win}
                now={now}
                live={live}
                stat={statFor(selected)}
                statsState={stats.isPending ? 'pending' : stats.isError ? 'error' : 'ok'}
                logs={logs.data}
                logsPending={logs.isPending}
                logsError={logs.isError ? errorMessage(logs.error) : ''}
                onSelect={(id) => {
                  setSelected(id)
                  rowRefs.current.get(id)?.scrollIntoView({ block: 'nearest' })
                }}
                onClose={() => setSelected(null)}
              />
            )}
          </div>
        </>
      )}
    </Page>
  )
}

function Bars({ row, win, now, live, stat }: { row: Row; win: Window; now: number; live: boolean; stat?: Stat }) {
  const last = row.attempts.length - 1
  const primary = row.attempts[last]
  const primarySpan = primary ? attemptSpan(primary, now) : null
  const mark = (ms: number) => primarySpan && ms > 0 && primarySpan.start - win.origin + ms <= win.total
  return (
    <span className="bk-gantt-track">
      {row.attempts.map((a, i) => {
        const s = attemptSpan(a, now)
        if (!s) return null
        const meta = statusMeta(a.status)
        return (
          <i
            key={a.id || i}
            className={cx('bk-gantt-bar', `bk-tone-${meta.tone}`, i < last && 'is-earlier', i === last && isActive(a.status) && 'is-live')}
            style={{ left: pct(s.start - win.origin, win.total), width: pct(s.duration, win.total) }}
            title={`Attempt ${(a.attempt ?? i) + 1}: ${meta.label}, ${formatDuration(s.duration)}, started at ${offset(s.start, win)}`}
          />
        )
      })}
      {stat && mark(stat.avg) && (
        <i
          className="bk-gantt-mark is-avg"
          style={{ left: pct(primarySpan!.start - win.origin + stat.avg, win.total) }}
          title={`Average of recent successful runs: ${formatDuration(stat.avg)}`}
        />
      )}
      {stat && mark(stat.p95) && (
        <i
          className="bk-gantt-mark is-p95"
          style={{ left: pct(primarySpan!.start - win.origin + stat.p95, win.total) }}
          title={`95th percentile of recent successful runs: ${formatDuration(stat.p95)}`}
        />
      )}
      {!primarySpan && <span className="bk-gantt-waiting">{primary ? statusMeta(primary.status).label : live ? 'Waiting' : 'Did not run'}</span>}
    </span>
  )
}

function Detail({
  selected,
  rows,
  run,
  pipeline,
  win,
  now,
  live,
  stat,
  statsState,
  logs,
  logsPending,
  logsError,
  onSelect,
  onClose,
}: {
  selected: string
  rows: Row[]
  run: Run
  pipeline?: Pipeline
  win: Window
  now: number
  live: boolean
  stat?: Stat
  statsState: 'pending' | 'error' | 'ok'
  logs?: LogEntry[]
  logsPending: boolean
  logsError: string
  onSelect: (id: string) => void
  onClose: () => void
}) {
  const [level, setLevel] = useState('all')
  const isRun = selected === RUN_ROW
  const byId = new Map(rows.map((row) => [row.id, row]))
  const row = byId.get(selected)
  if (!isRun && !row) return null
  const primary = lastOf(row)
  const span = primary ? attemptSpan(primary, now) : null
  const min = level === 'all' ? -1 : (LEVEL_RANK[level] ?? -1)
  const lines = (logs ?? []).filter((l) => (isRun ? !l.node_id : l.node_id === selected) && (LEVEL_RANK[l.level] ?? 1) >= min)
  const groups = groupByAttempt(lines)
  const upstream = isRun
    ? []
    : upstreamOf(selected, pipeline?.edges).map((id) => {
        const up = byId.get(id)
        const last = lastOf(up)
        const s = last ? attemptSpan(last, now) : null
        return { id, name: up?.name ?? id, status: last?.status, end: s && !isActive(last?.status) ? s.end : null }
      })
  const lastEnd = Math.max(-Infinity, ...upstream.map((u) => u.end ?? -Infinity))
  const typical =
    statsState === 'pending'
      ? 'Loading'
      : statsState === 'error'
        ? 'Unavailable'
        : stat?.durations?.length
          ? `Average ${formatDuration(stat.avg)}, 95th percentile ${formatDuration(stat.p95)} over ${stat.durations.length} recent successful run${stat.durations.length === 1 ? '' : 's'}`
          : 'No recent successful runs to compare with'
  const status = isRun ? run.status : primary?.status
  const error = isRun ? run.error : primary?.error

  return (
    <aside className="bk-gantt-detail" aria-label={isRun ? 'Run details' : `Details of ${row!.name}`}>
      <header className="bk-gantt-detail-head">
        <div>
          <h2>{isRun ? 'Run' : row!.name}</h2>
          <p>
            {status ? <StatusBadge status={status} /> : <Badge tone="neutral">{live ? 'Waiting' : 'Did not run'}</Badge>}
            {!isRun && row!.type && <span className="bk-mono">{row!.type}</span>}
            {!isRun && row!.orphan && <span>Not in the current definition</span>}
          </p>
        </div>
        <IconButton size="sm" label="Close details" onClick={onClose}>
          <X size={15} aria-hidden="true" />
        </IconButton>
      </header>

      {isRun ? (
        <dl className="bk-gantt-facts">
          <dt>Started</dt>
          <dd>{formatDateTime(run.started_at)}</dd>
          <dt>Finished</dt>
          <dd>{live ? 'In progress' : formatDateTime(run.finished_at)}</dd>
          <dt>Duration</dt>
          <dd>{formatDuration(runElapsed(run, now))}</dd>
          <dt>Trigger</dt>
          <dd>{run.trigger || EMPTY}</dd>
          {run.trace_id && (
            <>
              <dt>Trace</dt>
              <dd className="bk-mono">{run.trace_id}</dd>
            </>
          )}
        </dl>
      ) : (
        <dl className="bk-gantt-facts">
          <dt>Duration</dt>
          <dd>{span ? formatDuration(span.duration) : EMPTY}</dd>
          <dt>Started</dt>
          <dd>{span ? `${formatTime(primary!.started_at)} (${offset(span.start, win)})` : EMPTY}</dd>
          <dt>Ended</dt>
          <dd>{span && !isActive(primary!.status) ? offset(span.end, win) : EMPTY}</dd>
          <dt>Queued for</dt>
          <dd>{typeof primary?.queue_ms === 'number' ? formatDuration(primary.queue_ms) : EMPTY}</dd>
          <dt>Rows</dt>
          <dd>{primary ? formatNumber(primary.row_count) : EMPTY}</dd>
          <dt>Throughput</dt>
          <dd>{typeof primary?.rows_per_sec === 'number' ? `${formatNumber(Math.round(primary.rows_per_sec), { compact: true })} rows/s` : EMPTY}</dd>
          <dt>Typical</dt>
          <dd>{typical}</dd>
          {primary?.trace_id && (
            <>
              <dt>Trace</dt>
              <dd className="bk-mono">{primary.trace_id}</dd>
            </>
          )}
        </dl>
      )}

      {error && (
        <Callout tone="danger" title={isRun ? 'Run error' : 'Node error'}>
          <pre className="bk-run-error">{error}</pre>
        </Callout>
      )}

      {!isRun && row!.attempts.length > 1 && (
        <section className="bk-gantt-section">
          <h3>Attempts</h3>
          <ol className="bk-gantt-list">
            {row!.attempts.map((a, i) => {
              const s = attemptSpan(a, now)
              return (
                <li key={a.id || i}>
                  <span className="bk-gantt-list-name">
                    <span>Attempt {(a.attempt ?? i) + 1}</span>
                    {a.error && <small>{a.error.split('\n')[0]}</small>}
                  </span>
                  <StatusBadge status={a.status} />
                  <span className="bk-mono">{s ? `${formatDuration(s.duration)} at ${offset(s.start, win)}` : EMPTY}</span>
                </li>
              )
            })}
          </ol>
        </section>
      )}

      {!isRun && (
        <section className="bk-gantt-section">
          <h3>Waited for</h3>
          {!pipeline ? (
            <p>Unknown without the pipeline definition.</p>
          ) : !upstream.length ? (
            <p>Nothing: this node has no upstream nodes.</p>
          ) : (
            <ul className="bk-gantt-list">
              {upstream.map((u) => (
                <li key={u.id}>
                  <button type="button" onClick={() => onSelect(u.id)} disabled={!byId.has(u.id)}>
                    <span className="bk-gantt-list-name">
                      <span>{u.name}</span>
                      {upstream.length > 1 && u.end !== null && u.end === lastEnd && <small className="bk-gantt-gate">Finished last</small>}
                    </span>
                    {u.status ? <StatusBadge status={u.status} /> : <span />}
                    <span className="bk-mono">{u.end !== null ? `ended ${offset(u.end, win)}` : 'did not finish'}</span>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </section>
      )}

      <section className="bk-gantt-section">
        <div className="bk-gantt-logs-head">
          <h3>{isRun ? 'Run-level logs' : 'Logs'}</h3>
          <Select value={level} onChange={(e) => setLevel(e.target.value)} aria-label="Minimum level">
            <option value="all">All levels</option>
            <option value="info">Info and above</option>
            <option value="warning">Warnings and errors</option>
            <option value="error">Errors only</option>
          </Select>
        </div>
        {logsError ? (
          <Callout tone="danger" title="Logs could not be loaded">
            {logsError}
          </Callout>
        ) : (
          <div className="bk-logs-body bk-gantt-logs" role="log">
            {logsPending ? (
              <div className="bk-logs-empty">
                <Spinner size="sm" /> Loading logs
              </div>
            ) : !lines.length ? (
              <div className="bk-logs-empty">
                {level !== 'all' ? 'No lines at this level.' : isRun ? 'No run-level log lines.' : live ? 'No log lines from this node yet.' : 'This node produced no logs.'}
              </div>
            ) : (
              groups.map((g) => (
                <div key={g.attempt ?? 'all'}>
                  {g.attempt !== null && <div className="bk-gantt-log-group">Attempt {g.attempt + 1}</div>}
                  {g.lines.map((l, i) => (
                    <div key={i} className={cx('bk-log-line', `is-${l.level === 'warn' ? 'warning' : l.level}`)}>
                      <time>{stamp(l.timestamp)}</time>
                      <span className="bk-log-level">{l.level}</span>
                      <span className="bk-log-msg">{l.message}</span>
                    </div>
                  ))}
                </div>
              ))
            )}
          </div>
        )}
      </section>
    </aside>
  )
}
