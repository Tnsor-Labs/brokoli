import { useMemo, useState, type ReactNode } from 'react'
import { useInfiniteQuery, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import { CalendarClock, ChevronRight, LayoutGrid, PencilLine, Play, SlidersHorizontal } from 'lucide-react'
import { pipelineApi, type PhysicalWorkUnit, type Run } from '@brokoli/api'
import { useSession } from '@brokoli/auth'
import {
  Badge,
  Button,
  Callout,
  EmptyState,
  Page,
  PageHeader,
  Skeleton,
  StatusBadge,
  cx,
  errorMessage,
  formatDateTime,
  formatDuration,
  formatRelative,
  statusMeta,
  toDate,
  useToast,
} from '@brokoli/ui'
import { FAILURE, SUCCESS, keys, paths } from '../keys'
import { useNow, useRunActivity } from '../live'
import { BackfillDialog, RunParamsDialog, backfillMessage } from './Dialogs'
import { RunDetail } from './RunDetail'
import { isActive, runDuration } from './model'
import './runs.css'

const PAGE = 25
const dayLabel = new Intl.DateTimeFormat(undefined, { weekday: 'short', month: 'short', day: 'numeric' })

function localDay(d: Date) {
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
}

function PlanSection({ pipelineId }: { pipelineId: string }) {
  const [open, setOpen] = useState(false)
  const [unit, setUnit] = useState<PhysicalWorkUnit | null>(null)
  const plan = useQuery({ queryKey: keys.plan(pipelineId), queryFn: () => pipelineApi.plan(pipelineId) })
  if (plan.isPending) return null
  if (plan.isError)
    return (
      <p className="bk-muted bk-plan-missing">
        The execution plan is not available: {errorMessage(plan.error)}
      </p>
    )
  const data = plan.data
  return (
    <section className="bk-panel-card">
      <button type="button" className="bk-disclosure" onClick={() => setOpen((v) => !v)} aria-expanded={open}>
        <ChevronRight size={16} aria-hidden="true" className="bk-disclosure-icon" />
        <span>
          <strong>Execution plan</strong>
          <small>How Brokoli places the work before runtime data is known.</small>
        </span>
        <span className="bk-disclosure-meta">
          {data.stages.length} stage{data.stages.length === 1 ? '' : 's'} · {data.static_instance_count}+ known instances
          {data.dynamic_nodes > 0 && ` · ${data.dynamic_nodes} fan-out${data.dynamic_nodes === 1 ? '' : 's'}`}
        </span>
      </button>
      {open && (
        <div className="bk-plan">
          <div className="bk-plan-stages">
            {data.stages.map((stage) => (
              <div key={stage.index} className="bk-plan-stage">
                <span className="bk-plan-stage-label">Stage {stage.index + 1}</span>
                {stage.work_units.map((u) => (
                  <button
                    key={u.logical_node_id}
                    type="button"
                    className={cx('bk-plan-unit', unit?.logical_node_id === u.logical_node_id && 'is-selected')}
                    onClick={() => setUnit(unit?.logical_node_id === u.logical_node_id ? null : u)}
                  >
                    <i className={u.runtime_resolved ? 'is-dynamic' : ''} aria-hidden="true" />
                    <strong>{u.logical_node_id}</strong>
                    <small>
                      {u.kind} · {u.runtime_resolved ? 'resolved at runtime' : `${u.static_instance_count} instance${u.static_instance_count === 1 ? '' : 's'}`}
                    </small>
                  </button>
                ))}
              </div>
            ))}
          </div>
          {unit && (
            <div className="bk-plan-detail">
              <strong>
                {unit.logical_node_id} <Badge>{unit.node_type}</Badge>
              </strong>
              <p>{unit.explain}</p>
              <dl>
                <dt>Retry scope</dt>
                <dd>{unit.retry_scope}</dd>
                <dt>Instance key</dt>
                <dd className="bk-mono">{unit.instance_key_template || 'single'}</dd>
                {unit.concurrency_group && (
                  <>
                    <dt>Pool</dt>
                    <dd>{unit.concurrency_group}</dd>
                  </>
                )}
                {unit.max_concurrency ? (
                  <>
                    <dt>Max concurrency</dt>
                    <dd>{unit.max_concurrency}</dd>
                  </>
                ) : null}
              </dl>
            </div>
          )}
        </div>
      )}
    </section>
  )
}

function History({ runs, selected, onSelect }: { runs: Run[]; selected: string | null; onSelect: (id: string) => void }) {
  const days = useMemo(() => {
    const map = new Map<string, Run[]>()
    for (const r of runs) {
      const d = toDate(r.started_at)
      if (!d) continue
      const key = localDay(d)
      map.set(key, [...(map.get(key) ?? []), r])
    }
    return [...map.entries()]
      .sort(([a], [b]) => b.localeCompare(a))
      .slice(0, 14)
      .map(([key, list]) => ({ key, list: list.sort((a, b) => (toDate(a.started_at)!.getTime() - toDate(b.started_at)!.getTime())) }))
  }, [runs])
  if (!days.length) return null
  return (
    <section className="bk-panel-card bk-history">
      <header className="bk-section-head">
        <h3>Recent activity</h3>
        <span className="bk-muted">Each mark is one run, grouped by the day it started (local time).</span>
      </header>
      <div className="bk-history-days">
        {days.map(({ key, list }) => (
          <div key={key} className="bk-history-day">
            <span className="bk-history-date">{dayLabel.format(new Date(`${key}T00:00:00`))}</span>
            <span className="bk-history-cells">
              {list.map((r) => {
                const meta = statusMeta(r.status)
                return (
                  <button
                    key={r.id}
                    type="button"
                    className={cx('bk-history-cell', `bk-tone-${meta.tone}`, selected === r.id && 'is-selected')}
                    title={`${r.id.slice(0, 8)}: ${meta.label}, ${formatDateTime(r.started_at)}`}
                    aria-label={`Run ${r.id.slice(0, 8)}, ${meta.label}, ${formatDateTime(r.started_at)}`}
                    onClick={() => onSelect(r.id)}
                  />
                )
              })}
            </span>
            <span className="bk-history-count">{list.length}</span>
          </div>
        ))}
      </div>
    </section>
  )
}

export function RunsPage({ pipelineId, extra }: { pipelineId: string; extra?: ReactNode }) {
  const session = useSession()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const toast = useToast()
  const [params, setParams] = useSearchParams()
  const [dialog, setDialog] = useState<'params' | 'backfill' | null>(null)
  const [starting, setStarting] = useState(false)
  const pipeline = useQuery({ queryKey: keys.pipeline(pipelineId), queryFn: () => pipelineApi.get(pipelineId) })
  const runs = useInfiniteQuery({
    queryKey: keys.runs(pipelineId),
    queryFn: ({ pageParam }) => pipelineApi.runsPage(pipelineId, pageParam, PAGE),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => (last.has_next && last.cursor ? last.cursor : undefined),
  })
  const list = useMemo(() => runs.data?.pages.flatMap((p) => p.items ?? []) ?? [], [runs.data])
  const selectedId = params.get('run') ?? list[0]?.id ?? null
  const anyActive = list.some((r) => isActive(r.status))
  const now = useNow(anyActive ? 1000 : 30_000)

  const select = (runId: string) =>
    setParams(
      (p) => {
        p.set('run', runId)
        return p
      },
      { replace: false },
    )

  useRunActivity(() => {
    void queryClient.invalidateQueries({ queryKey: keys.runs(pipelineId) })
    if (selectedId) {
      void queryClient.invalidateQueries({ queryKey: keys.run(selectedId), exact: true })
      void queryClient.invalidateQueries({ queryKey: keys.runEvents(selectedId) })
      void queryClient.invalidateQueries({ queryKey: keys.runInstances(selectedId) })
    }
  })

  const p = pipeline.data
  const canRun = session.can('pipelines.run')
  const runBlocked = !p ? 'Loading' : p.draft ? 'Drafts cannot run. Publish it from the editor first.' : ''
  const backfillBlocked = runBlocked || (!p?.schedule ? 'Backfill needs a schedule: it runs one interval of the schedule at a time.' : '')

  const start = async (runParams?: Record<string, string>) => {
    const result = await pipelineApi.run(pipelineId, runParams)
    toast.success('Run started', `Run ${result.id.slice(0, 8)} is ${result.status}.`)
    await queryClient.invalidateQueries({ queryKey: keys.runs(pipelineId) })
    select(result.id)
  }
  const runNow = async () => {
    setStarting(true)
    try {
      await start()
    } catch (e) {
      toast.error('The run did not start', e)
    } finally {
      setStarting(false)
    }
  }

  const latest = list[0]
  const decided = list.filter((r) => SUCCESS.has(r.status) || FAILURE.has(r.status))
  const successRate = decided.length ? Math.round((decided.filter((r) => SUCCESS.has(r.status)).length / decided.length) * 100) : null

  if (pipeline.isError)
    return (
      <Page>
        <Callout tone="danger" title="This pipeline could not be loaded" action={<Button onClick={() => navigate(paths.list)}>Back to pipelines</Button>}>
          {errorMessage(pipeline.error)}
        </Callout>
      </Page>
    )

  return (
    <Page wide className="bk-runs-page">
      <nav className="bk-crumbs" aria-label="Breadcrumb">
        <Link to={paths.list}>Pipelines</Link>
        <span aria-hidden="true">/</span>
        <span aria-current="page">{p?.name ?? 'Pipeline'}</span>
      </nav>
      <PageHeader
        eyebrow="Runs"
        title={p?.name ?? <Skeleton width={260} height={32} />}
        description={p?.description || 'Run history, timing, output and logs for this pipeline.'}
        meta={
          p && (
            <>
              {p.draft && <Badge tone="warning">Draft</Badge>}
              {p.enabled === false && <Badge tone="queued">Paused</Badge>}
              <Badge tone={p.schedule ? 'accent' : 'neutral'}>
                <CalendarClock size={12} aria-hidden="true" /> {p.schedule || 'Manual'}
              </Badge>
            </>
          )
        }
        actions={
          <>
            <Button icon={<LayoutGrid size={15} aria-hidden="true" />} onClick={() => navigate(paths.grid(pipelineId))}>
              Grid
            </Button>
            <Button icon={<PencilLine size={15} aria-hidden="true" />} onClick={() => navigate(paths.editor(pipelineId))}>
              Open editor
            </Button>
            {canRun && (
              <>
                <Button disabled={Boolean(backfillBlocked)} title={backfillBlocked || 'Run past schedule intervals'} onClick={() => setDialog('backfill')}>
                  Backfill
                </Button>
                <Button
                  icon={<SlidersHorizontal size={15} aria-hidden="true" />}
                  disabled={Boolean(runBlocked)}
                  title={runBlocked || 'Run with parameters'}
                  onClick={() => setDialog('params')}
                >
                  With parameters
                </Button>
                <Button variant="primary" icon={<Play size={15} aria-hidden="true" />} disabled={Boolean(runBlocked)} title={runBlocked || 'Run now'} loading={starting} onClick={runNow}>
                  Run now
                </Button>
              </>
            )}
          </>
        }
      />

      {extra}

      {latest && (
        <div className="bk-metrics">
          <div className="bk-metric">
            <span>Latest run</span>
            <StatusBadge status={latest.status} />
            <small>{formatRelative(latest.started_at, now)}</small>
          </div>
          <div className="bk-metric">
            <span>Success rate</span>
            <strong>{successRate === null ? 'No finished runs' : `${successRate}%`}</strong>
            <small>of {decided.length} finished run{decided.length === 1 ? '' : 's'} loaded</small>
          </div>
          <div className="bk-metric">
            <span>Latest duration</span>
            <strong className="bk-mono">{formatDuration(runDuration(latest, now))}</strong>
            <small>{isActive(latest.status) ? 'still running' : formatDateTime(latest.finished_at)}</small>
          </div>
          <div className="bk-metric">
            <span>Runs loaded</span>
            <strong>{list.length}</strong>
            <small>{runs.hasNextPage ? 'more history available' : 'complete history'}</small>
          </div>
        </div>
      )}

      <PlanSection pipelineId={pipelineId} />
      <History runs={list} selected={selectedId} onSelect={select} />

      {runs.isError ? (
        <Callout tone="danger" title="Runs could not be loaded">
          {errorMessage(runs.error)}
        </Callout>
      ) : runs.isPending ? (
        <div className="bk-runs-layout">
          <div className="bk-run-list">
            {Array.from({ length: 5 }, (_, i) => (
              <Skeleton key={i} height={62} />
            ))}
          </div>
        </div>
      ) : list.length === 0 ? (
        <div className="bk-table-wrap">
          <EmptyState
            icon={<Play size={20} aria-hidden="true" />}
            title="No runs yet"
            action={
              canRun && (
                <Button variant="primary" onClick={runNow} disabled={Boolean(runBlocked)} title={runBlocked || undefined} loading={starting}>
                  Run now
                </Button>
              )
            }
          >
            {runBlocked || 'Start the first run to see its timeline, output and logs here.'}
          </EmptyState>
        </div>
      ) : (
        <div className="bk-runs-layout">
          <div className="bk-run-list" role="listbox" aria-label="Runs">
            {list.map((r) => {
              const duration = runDuration(r, now)
              return (
                <button
                  key={r.id}
                  type="button"
                  role="option"
                  aria-selected={selectedId === r.id}
                  className={cx('bk-run-item', selectedId === r.id && 'is-selected', `bk-tone-${statusMeta(r.status).tone}`)}
                  onClick={() => select(r.id)}
                >
                  <span className="bk-run-item-top">
                    <StatusBadge status={r.status} />
                    <span className="bk-mono bk-muted">{r.id.slice(0, 8)}</span>
                  </span>
                  <span className="bk-run-item-bottom">
                    <span title={formatDateTime(r.started_at)}>{r.started_at ? formatRelative(r.started_at, now) : 'Not started'}</span>
                    <span className="bk-mono">{formatDuration(duration)}</span>
                    {r.trigger && r.trigger !== 'manual' && <Badge>{r.trigger}</Badge>}
                  </span>
                </button>
              )
            })}
            {runs.hasNextPage && (
              <Button variant="ghost" onClick={() => void runs.fetchNextPage()} loading={runs.isFetchingNextPage}>
                Load older runs
              </Button>
            )}
          </div>
          {selectedId && p && <RunDetail key={selectedId} pipeline={p} runId={selectedId} now={now} onSelectRun={select} />}
        </div>
      )}

      {dialog === 'params' && p && <RunParamsDialog defaults={p.params} onRun={start} onClose={() => setDialog(null)} />}
      {dialog === 'backfill' && p && (
        <BackfillDialog
          pipelineId={pipelineId}
          schedule={p.schedule}
          timezone={p.schedule_timezone}
          onClose={() => setDialog(null)}
          onDone={(plan) => {
            toast.success('Backfill accepted', backfillMessage(plan))
            void queryClient.invalidateQueries({ queryKey: keys.runs(pipelineId) })
          }}
        />
      )}
    </Page>
  )
}
