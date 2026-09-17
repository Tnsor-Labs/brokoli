import { useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { Ban, Maximize2, Redo2, RotateCcw, StepForward } from 'lucide-react'
import { ApiError, pipelineApi, runApi, type Pipeline } from '@brokoli/api'
import { useSession } from '@brokoli/auth'
import { PipelineGraph } from '@brokoli/pipeline-session'
import {
  Badge,
  Button,
  Callout,
  ConfirmDialog,
  EmptyState,
  Spinner,
  StatusBadge,
  Tabs,
  cx,
  errorMessage,
  formatDateTime,
  formatDuration,
  formatNumber,
  statusMeta,
  useToast,
} from '@brokoli/ui'
import { FAILURE, keys, paths } from '../keys'
import { LogView } from './LogView'
import { NodeData } from './NodeData'
import { Provenance } from './Provenance'
import { Queries } from './Queries'
import { Timeline } from './Timeline'
import { canRerunStatus, descendantClosure, isActive, nodeStatuses, primaryAttempts, runDuration, totalRows, triggeredByLabel } from './model'
import './gantt.css'

type Tab = 'timeline' | 'logs' | 'data' | 'sql' | 'events' | 'instances'

export function RunDetail({
  pipeline,
  runId,
  now,
  onSelectRun,
}: {
  pipeline: Pipeline
  runId: string
  now: number
  onSelectRun: (id: string) => void
}) {
  const session = useSession()
  const queryClient = useQueryClient()
  const toast = useToast()
  const [tab, setTab] = useState<Tab>('timeline')
  const [node, setNode] = useState<string | null>(null)
  const [confirmCancel, setConfirmCancel] = useState(false)
  const [rerunFrom, setRerunFrom] = useState<string | null>(null)
  const [busy, setBusy] = useState<'resume' | 'rerun' | null>(null)
  const [errorOpen, setErrorOpen] = useState(false)
  const run = useQuery({ queryKey: keys.run(runId), queryFn: () => runApi.get(runId) })
  // Events and instances are best-effort and independent: one failing never blocks the other or the run.
  const events = useQuery({ queryKey: keys.runEvents(runId), queryFn: () => runApi.events(runId) })
  const instances = useQuery({ queryKey: keys.runInstances(runId), queryFn: () => runApi.instances(runId) })
  const nodeNames = useMemo(() => Object.fromEntries(pipeline.nodes.map((n) => [n.id, n.name || n.id])), [pipeline.nodes])
  const nodeIds = useMemo(() => new Set(pipeline.nodes.map((n) => n.id)), [pipeline.nodes])

  if (run.isPending)
    return (
      <section className="bk-run-detail bk-run-detail-loading" aria-busy="true">
        <Spinner /> Loading run {runId.slice(0, 8)}
      </section>
    )
  if (run.isError)
    return (
      <section className="bk-run-detail">
        <Callout tone="danger" title={run.error instanceof ApiError && run.error.status === 404 ? 'This run no longer exists' : 'The run could not be loaded'}>
          {errorMessage(run.error)}
        </Callout>
      </section>
    )

  const r = run.data
  const primaries = primaryAttempts(r.node_runs)
  const counts = { ok: 0, failed: 0, skipped: 0, active: 0 }
  for (const nr of primaries.values()) {
    if (FAILURE.has(nr.status)) counts.failed++
    else if (nr.status === 'skipped') counts.skipped++
    else if (isActive(nr.status)) counts.active++
    else if (statusMeta(nr.status).tone === 'success') counts.ok++
  }
  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: keys.run(runId) })
    void queryClient.invalidateQueries({ queryKey: keys.runs(pipeline.id) })
  }
  const canRun = session.can('pipelines.run') && !pipeline.draft
  const longError = (r.error?.length ?? 0) > 280
  const sqlCount = events.data?.filter((e) => e.event_type === 'attempt.query').length
  // "Re-run from a node" reuses runs.resume and accepts a settled run
  // (success, failed or cancelled), never one still in flight.
  const canRerunNode = session.can('runs.resume') && canRerunStatus(r.status)
  const rerunClosure = rerunFrom ? descendantClosure(rerunFrom, pipeline.edges, nodeIds) : null
  const rerunDownstream = rerunClosure ? rerunClosure.size - 1 : 0

  const resume = async () => {
    setBusy('resume')
    try {
      const next = await runApi.resume(r.id)
      toast.success('Resumed as a new run', `Nodes that already succeeded are reused. New run ${next.id.slice(0, 8)}.`)
      refresh()
      onSelectRun(next.id)
    } catch (e) {
      toast.error('The run could not be resumed', e)
    } finally {
      setBusy(null)
    }
  }
  const rerun = async () => {
    setBusy('rerun')
    try {
      const next = await pipelineApi.run(pipeline.id, r.params && Object.keys(r.params).length ? r.params : undefined)
      toast.success('Run started', `Run ${next.id.slice(0, 8)} uses the current pipeline definition${r.params ? ' and the same parameters' : ''}.`)
      refresh()
      onSelectRun(next.id)
    } catch (e) {
      toast.error('The run did not start', e)
    } finally {
      setBusy(null)
    }
  }
  // No try/catch: ConfirmDialog awaits this, shows any error inline and keeps
  // itself open, matching the cancel dialog below.
  const rerunNode = async () => {
    const from = rerunFrom
    if (!from) return
    const next = await runApi.resume(r.id, from)
    toast.success(`Re-running from ${nodeNames[from] ?? from}`, `New run ${next.id.slice(0, 8)} re-runs that node and everything downstream. This run stays as it is.`)
    refresh()
    setRerunFrom(null)
    onSelectRun(next.id)
  }

  return (
    <section className="bk-run-detail" aria-label={`Run ${r.id.slice(0, 8)}`}>
      <header className="bk-run-head">
        <div>
          <h2>
            Run <span className="bk-mono">{r.id.slice(0, 8)}</span>
          </h2>
          <div className="bk-run-head-meta">
            <StatusBadge status={r.status} />
            {r.trigger && <Badge>{r.trigger}</Badge>}
            {r.pipeline_version ? <Badge>v{r.pipeline_version}</Badge> : null}
            {r.cancel_requested && isActive(r.status) && <Badge tone="warning">Cancelling</Badge>}
            {r.resumed_from_run_id && (
              <button type="button" className="bk-link" onClick={() => onSelectRun(r.resumed_from_run_id!)}>
                Resumed from {r.resumed_from_run_id.slice(0, 8)}
              </button>
            )}
          </div>
        </div>
        <div className="bk-run-head-actions">
          {isActive(r.status) && session.can('runs.cancel') && (
            <Button size="sm" variant="danger" icon={<Ban size={14} aria-hidden="true" />} onClick={() => setConfirmCancel(true)} disabled={r.cancel_requested}>
              Cancel run
            </Button>
          )}
          {r.status === 'failed' && session.can('runs.resume') && (
            <Button size="sm" icon={<StepForward size={14} aria-hidden="true" />} onClick={resume} loading={busy === 'resume'} title="Start a new run that reuses the nodes that already succeeded">
              Resume
            </Button>
          )}
          {!isActive(r.status) && canRun && (
            <Button size="sm" icon={<RotateCcw size={14} aria-hidden="true" />} onClick={rerun} loading={busy === 'rerun'}>
              Run again
            </Button>
          )}
        </div>
      </header>

      <dl className="bk-run-summary">
        <div>
          <dt>Started</dt>
          <dd>{formatDateTime(r.started_at)}</dd>
        </div>
        {triggeredByLabel(r.triggered_by) && (
          <div>
            <dt>Started by</dt>
            <dd>{triggeredByLabel(r.triggered_by)}</dd>
          </div>
        )}
        <div>
          <dt>Finished</dt>
          <dd>{isActive(r.status) ? 'In progress' : formatDateTime(r.finished_at)}</dd>
        </div>
        <div>
          <dt>Duration</dt>
          <dd className="bk-mono">{formatDuration(runDuration(r, now))}</dd>
        </div>
        <div>
          <dt>Rows</dt>
          <dd className="bk-mono">{formatNumber(totalRows(r))}</dd>
        </div>
        <div>
          <dt>Nodes</dt>
          <dd>
            {counts.ok} succeeded
            {counts.failed > 0 && <span className="bk-text-danger">, {counts.failed} failed</span>}
            {counts.skipped > 0 && `, ${counts.skipped} skipped`}
            {counts.active > 0 && `, ${counts.active} running`}
          </dd>
        </div>
        {r.data_interval_start && (
          <div>
            <dt>Data interval</dt>
            <dd>
              {formatDateTime(r.data_interval_start)} to {formatDateTime(r.data_interval_end)}
            </dd>
          </div>
        )}
        {r.params && Object.keys(r.params).length > 0 && (
          <div className="is-wide">
            <dt>Parameters</dt>
            <dd className="bk-run-params">
              {Object.entries(r.params).map(([k, v]) => (
                <code key={k}>
                  {k}={v}
                </code>
              ))}
            </dd>
          </div>
        )}
      </dl>

      {r.error && (
        <Callout tone="danger" title="Run error">
          <pre className={cx('bk-run-error', longError && !errorOpen && 'is-clamped')}>{r.error}</pre>
          {longError && (
            <button type="button" className="bk-link" onClick={() => setErrorOpen((v) => !v)}>
              {errorOpen ? 'Show less' : 'Show the full error'}
            </button>
          )}
        </Callout>
      )}

      <Tabs
        label="Run detail"
        value={tab}
        onChange={setTab}
        items={[
          { id: 'timeline', label: 'Timeline' },
          { id: 'logs', label: 'Logs' },
          { id: 'data', label: 'Data', count: primaries.size || undefined },
          { id: 'sql', label: 'SQL', count: sqlCount || undefined },
          { id: 'events', label: 'Provenance', count: events.data?.length },
          { id: 'instances', label: 'Instances', count: instances.data?.length },
        ]}
      />

      {node && nodeIds.has(node) && canRerunNode && (
        <div className="bk-run-node-action">
          <span className="bk-muted">
            Selected node: <strong>{nodeNames[node] ?? node}</strong>
          </span>
          <Button size="sm" icon={<Redo2 size={14} aria-hidden="true" />} onClick={() => setRerunFrom(node)}>
            Re-run from here
          </Button>
        </div>
      )}

      <div className="bk-run-tab">
        {tab === 'timeline' && (
          <div className="bk-run-timeline">
            <div className="bk-gantt-open">
              <Link className="bk-button bk-button-secondary bk-button-sm" to={paths.timeline(pipeline.id, runId)}>
                <Maximize2 size={14} aria-hidden="true" /> Open full timeline
              </Link>
            </div>
            {pipeline.nodes.length > 0 && (
              <div className="bk-run-graph">
                <PipelineGraph
                  nodes={pipeline.nodes}
                  edges={pipeline.edges}
                  statuses={nodeStatuses(r)}
                  selected={node}
                  onSelect={(id) => {
                    setNode(id)
                    if (primaries.has(id)) setTab('data')
                  }}
                />
              </div>
            )}
            <Timeline
              run={r}
              nodes={pipeline.nodes}
              now={now}
              selected={node}
              onSelect={(id) => {
                setNode(id)
                if (primaries.has(id)) setTab('data')
              }}
            />
          </div>
        )}
        {tab === 'logs' && <LogView run={r} nodeNames={nodeNames} />}
        {tab === 'data' &&
          (primaries.size === 0 ? (
            <EmptyState title="No node output yet">Output samples appear here as nodes finish.</EmptyState>
          ) : (
            <div className="bk-run-data">
              <div className="bk-chip-row" role="tablist" aria-label="Node">
                {[...primaries.values()].map((nr) => (
                  <button
                    key={nr.node_id}
                    type="button"
                    role="tab"
                    aria-selected={(node ?? [...primaries.keys()][0]) === nr.node_id}
                    className={cx('bk-chip', (node ?? [...primaries.keys()][0]) === nr.node_id && 'is-active', `bk-tone-${statusMeta(nr.status).tone}`)}
                    onClick={() => setNode(nr.node_id)}
                  >
                    <i aria-hidden="true" />
                    {nodeNames[nr.node_id] ?? nr.node_id}
                    <span className="bk-mono">{formatNumber(nr.row_count, { compact: true })}</span>
                  </button>
                ))}
              </div>
              {(() => {
                const id = node && primaries.has(node) ? node : [...primaries.keys()][0]
                const nr = primaries.get(id)!
                return <NodeData key={id} runId={r.id} node={nr} name={nodeNames[id] ?? id} />
              })()}
            </div>
          ))}
        {tab === 'sql' &&
          (events.isPending ? (
            <div className="bk-inline-loading">
              <Spinner size="sm" /> Loading queries
            </div>
          ) : events.isError ? (
            <Callout tone="danger" title="Queries could not be loaded">
              {errorMessage(events.error)}
            </Callout>
          ) : (
            <Queries events={events.data} nodeNames={nodeNames} />
          ))}
        {tab === 'events' &&
          (events.isPending ? (
            <div className="bk-inline-loading">
              <Spinner size="sm" /> Loading events
            </div>
          ) : events.isError ? (
            <Callout tone="danger" title="Events could not be loaded">
              {errorMessage(events.error)}
            </Callout>
          ) : (
            <Provenance events={events.data} nodeNames={nodeNames} />
          ))}
        {tab === 'instances' &&
          (instances.isPending ? (
            <div className="bk-inline-loading">
              <Spinner size="sm" /> Loading instances
            </div>
          ) : instances.isError ? (
            <Callout tone="danger" title="Instances could not be loaded">
              {errorMessage(instances.error)}
            </Callout>
          ) : instances.data.length === 0 ? (
            <EmptyState title="No physical instances">Instances are recorded as the run executes.</EmptyState>
          ) : (
            <div className="bk-data-table">
              <table>
                <thead>
                  <tr>
                    <th>Node</th>
                    <th>Instance</th>
                    <th>Status</th>
                    <th className="is-num">Attempt</th>
                    <th className="is-num">Rows</th>
                    <th className="is-num">Duration</th>
                  </tr>
                </thead>
                <tbody>
                  {instances.data.map((i) => (
                    <tr key={`${i.logical_node_id}-${i.instance_key}-${i.attempt}`}>
                      <td>{nodeNames[i.logical_node_id] ?? i.logical_node_id}</td>
                      <td className="bk-mono">{i.instance_key || 'single'}</td>
                      <td>
                        <StatusBadge status={i.status} />
                      </td>
                      <td className="is-num">{i.attempt + 1}</td>
                      <td className="is-num">{formatNumber(i.row_count)}</td>
                      <td className="is-num">{formatDuration(i.duration_ms)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ))}
      </div>

      {confirmCancel && (
        <ConfirmDialog
          title="Cancel this run?"
          tone="danger"
          confirmLabel="Cancel run"
          cancelLabel="Keep running"
          onCancel={() => setConfirmCancel(false)}
          onConfirm={async () => {
            await runApi.cancel(r.id)
            toast.success('Cancellation requested', 'The run stops at the next safe point.')
            setConfirmCancel(false)
            refresh()
          }}
        >
          <p>Nodes that are running are stopped and the run is marked cancelled. Output already written by finished nodes stays where it is.</p>
        </ConfirmDialog>
      )}

      {rerunFrom && (
        <ConfirmDialog
          title={`Re-run from ${nodeNames[rerunFrom] ?? rerunFrom}?`}
          confirmLabel="Re-run"
          cancelLabel="Keep as is"
          onCancel={() => setRerunFrom(null)}
          onConfirm={rerunNode}
        >
          <p>
            This starts a <strong>new run</strong> that re-executes {nodeNames[rerunFrom] ?? rerunFrom}
            {rerunDownstream > 0 ? ` and ${formatNumber(rerunDownstream)} downstream ${rerunDownstream === 1 ? 'node' : 'nodes'}` : ' (no downstream nodes)'}. Nodes upstream that already
            succeeded are reused. This run is left exactly as it is.
          </p>
        </ConfirmDialog>
      )}
    </section>
  )
}
