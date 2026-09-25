import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link, useNavigate } from 'react-router-dom'
import { RefreshCw } from 'lucide-react'
import { pipelineApi } from '@brokoli/api'
import {
  Button,
  Callout,
  EmptyState,
  Page,
  PageHeader,
  Select,
  Skeleton,
  errorMessage,
  formatDateTime,
  formatDuration,
  formatNumber,
  statusMeta,
  toDate,
} from '@brokoli/ui'
import { keys, paths } from '../keys'
import './grid.css'

const short = new Intl.DateTimeFormat(undefined, { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })

/*
 * Runs x nodes. Each column is a run (oldest left), each row a node of the
 * current definition. A red row is a node that keeps breaking; a red column
 * is a bad run. Load failures are shown as failures, not as "no runs".
 */
export function GridPage({ pipelineId }: { pipelineId: string }) {
  const [count, setCount] = useState(30)
  const navigate = useNavigate()
  const pipeline = useQuery({ queryKey: keys.pipeline(pipelineId), queryFn: () => pipelineApi.get(pipelineId) })
  const grid = useQuery({ queryKey: keys.grid(pipelineId, count), queryFn: () => pipelineApi.grid(pipelineId, count) })
  const runs = [...(grid.data?.runs ?? [])].reverse()
  let previousVersion: number | null = null

  return (
    <Page wide>
      <nav className="bk-crumbs" aria-label="Breadcrumb">
        <Link to={paths.list}>Pipelines</Link>
        <span aria-hidden="true">/</span>
        <Link to={paths.runs(pipelineId)}>{pipeline.data?.name ?? 'Pipeline'}</Link>
        <span aria-hidden="true">/</span>
        <span aria-current="page">Run grid</span>
      </nav>
      <PageHeader
        eyebrow="Run grid"
        title={pipeline.data?.name ?? 'Run grid'}
        description="Every recent run against every node. Newest runs are on the right; click a cell or a column to open that run."
        actions={
          <>
            <Select value={String(count)} onChange={(e) => setCount(Number(e.target.value))} aria-label="Number of runs" className="bk-grid-count">
              {[15, 30, 60, 100].map((n) => (
                <option key={n} value={n}>
                  Last {n} runs
                </option>
              ))}
            </Select>
            <Button icon={<RefreshCw size={15} aria-hidden="true" />} onClick={() => void grid.refetch()} loading={grid.isFetching}>
              Refresh
            </Button>
          </>
        }
      />
      {grid.isError ? (
        <Callout tone="danger" title="The run grid could not be loaded">
          {errorMessage(grid.error)}
        </Callout>
      ) : grid.isPending ? (
        <div className="bk-grid-loading">
          {Array.from({ length: 6 }, (_, i) => (
            <Skeleton key={i} height={22} />
          ))}
        </div>
      ) : !runs.length ? (
        <div className="bk-table-wrap">
          <EmptyState title="No runs yet">The grid fills in once this pipeline has run history.</EmptyState>
        </div>
      ) : (
        <div className="bk-grid-wrap">
          <table className="bk-grid">
            <thead>
              <tr>
                <th scope="col" className="bk-grid-node">
                  Node
                </th>
                {runs.map((r) => {
                  const boundary = previousVersion !== null && r.pipeline_version !== previousVersion
                  previousVersion = r.pipeline_version
                  const started = toDate(r.started_at)
                  const title = [
                    `Run ${r.id.slice(0, 8)}: ${statusMeta(r.status).label}`,
                    `Started ${formatDateTime(r.started_at)}`,
                    r.trigger && `Trigger: ${r.trigger}`,
                    r.data_interval_start && `Interval ${formatDateTime(r.data_interval_start)} to ${formatDateTime(r.data_interval_end)}`,
                    `Pipeline version ${r.pipeline_version}`,
                  ]
                    .filter(Boolean)
                    .join('\n')
                  return (
                    <th key={r.id} scope="col" className={boundary ? 'is-boundary' : undefined}>
                      <button type="button" title={title} onClick={() => navigate(paths.runs(pipelineId, r.id))} className={`bk-tone-${statusMeta(r.status).tone}`}>
                        <span>{started ? short.format(started) : 'Pending'}</span>
                        {r.trigger === 'backfill' && <b>B</b>}
                        {boundary && <b>v{r.pipeline_version}</b>}
                      </button>
                    </th>
                  )
                })}
              </tr>
            </thead>
            <tbody>
              {grid.data.nodes.map((n) => (
                <tr key={n.id}>
                  <th scope="row" className="bk-grid-node">
                    <strong>{n.name || n.id}</strong>
                    <small>{n.type.replaceAll('_', ' ')}</small>
                  </th>
                  {runs.map((r) => {
                    const cell = grid.data.cells[r.id]?.[n.id]
                    if (!cell)
                      return (
                        <td key={r.id}>
                          <span className="bk-grid-cell is-absent" title="Did not run" aria-label={`${n.name}: did not run in run ${r.id.slice(0, 8)}`} />
                        </td>
                      )
                    const meta = statusMeta(cell.status)
                    const title = [
                      `${n.name}: ${meta.label}`,
                      `Duration ${formatDuration(cell.duration_ms)}`,
                      `${formatNumber(cell.row_count)} rows`,
                      cell.attempt > 0 && `Attempt ${cell.attempt + 1}`,
                      cell.error,
                    ]
                      .filter(Boolean)
                      .join('\n')
                    return (
                      <td key={r.id}>
                        <button
                          type="button"
                          className={`bk-grid-cell bk-tone-${meta.tone}${meta.pulse ? ' is-pulsing' : ''}`}
                          title={title}
                          aria-label={title.replaceAll('\n', ', ')}
                          onClick={() => navigate(paths.runs(pipelineId, r.id))}
                        />
                      </td>
                    )
                  })}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Page>
  )
}
