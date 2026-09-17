import { useQuery } from '@tanstack/react-query'
import { ApiError, runApi, type NodeRun } from '@brokoli/api'
import { Badge, Callout, EmptyState, Spinner, cx, errorMessage, formatNumber } from '@brokoli/ui'
import { keys } from '../keys'

function cell(value: unknown) {
  if (value === null || value === undefined) return <span className="bk-null">null</span>
  if (typeof value === 'object') return <span className="bk-mono">{JSON.stringify(value).slice(0, 200)}</span>
  const text = String(value)
  return text.length > 160 ? `${text.slice(0, 160)}...` : text
}

const notFound = (e: unknown) => e instanceof ApiError && e.status === 404

/*
 * Stored output sample and data profile for one node of a run. A missing
 * preview or profile (404) is a normal state for nodes that produce no
 * dataset and is said as such; any other failure is shown as an error.
 */
export function NodeData({ runId, node, name }: { runId: string; node: NodeRun; name: string }) {
  const preview = useQuery({
    queryKey: keys.nodePreview(runId, node.node_id),
    queryFn: () => runApi.preview(runId, node.node_id),
    retry: false,
  })
  const profile = useQuery({
    queryKey: keys.nodeProfile(runId, node.node_id),
    queryFn: () => runApi.profile(runId, node.node_id),
    retry: false,
  })
  const columns = preview.data?.columns ?? []
  const rows = preview.data?.rows ?? []
  const p = profile.data?.profile
  const drift = profile.data?.drift ?? []

  return (
    <div className="bk-node-data">
      <section>
        <header className="bk-section-head">
          <h3>Output sample</h3>
          <span className="bk-muted">
            {name} produced {formatNumber(node.row_count)} rows{rows.length ? `; ${rows.length} are stored as a sample` : ''}.
          </span>
        </header>
        {preview.isPending ? (
          <div className="bk-inline-loading">
            <Spinner size="sm" /> Loading sample
          </div>
        ) : preview.isError ? (
          notFound(preview.error) ? (
            <EmptyState title="No stored sample">This node did not store an output sample for this run.</EmptyState>
          ) : (
            <Callout tone="danger">{errorMessage(preview.error)}</Callout>
          )
        ) : !columns.length ? (
          <EmptyState title="Empty output">The node finished without columns to show.</EmptyState>
        ) : (
          <>
            {preview.data?.truncated &&
              (() => {
                // Prefer the preview's own total; fall back to the node's row
                // count (which the header above already shows) so the two never
                // disagree. Only when neither is known do we say so.
                const total = preview.data.total_rows ?? (node.row_count || null)
                return (
                  <Callout tone="warning" title="Showing a sample, not the full output">
                    {total != null
                      ? `This sample has ${formatNumber(rows.length)} of ${formatNumber(total)} rows.`
                      : `This sample is capped at ${formatNumber(rows.length)} rows; the full size is unknown.`}
                  </Callout>
                )
              })()}
            <div className="bk-data-table">
              <table>
                <thead>
                  <tr>
                    <th>#</th>
                    {columns.map((c) => (
                      <th key={c}>{c}</th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {rows.map((r, i) => (
                    <tr key={i}>
                      <td className="bk-muted">{i + 1}</td>
                      {columns.map((c) => (
                        <td key={c}>{cell(r[c])}</td>
                      ))}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </>
        )}
      </section>

      <section>
        <header className="bk-section-head">
          <h3>Profile</h3>
          {p && (
            <span className="bk-muted">
              {formatNumber(p.row_count)} rows, {p.column_count} columns, profiled in {p.profiling_ms}ms
            </span>
          )}
        </header>
        {profile.isPending ? (
          <div className="bk-inline-loading">
            <Spinner size="sm" /> Loading profile
          </div>
        ) : profile.isError ? (
          notFound(profile.error) ? (
            <p className="bk-muted">No profile was recorded for this node.</p>
          ) : (
            <Callout tone="danger">{errorMessage(profile.error)}</Callout>
          )
        ) : (
          <>
            {drift.length > 0 && (
              <div className="bk-drift">
                {drift.map((d, i) => (
                  <Callout key={i} tone={d.severity === 'critical' ? 'danger' : 'warning'} title={`${d.type.replaceAll('_', ' ')}: ${d.column}`}>
                    {d.previous || 'none'} to {d.current || 'none'}
                  </Callout>
                ))}
              </div>
            )}
            {p?.columns?.length ? (
              <div className="bk-data-table">
                <table>
                  <thead>
                    <tr>
                      <th>Column</th>
                      <th>Type</th>
                      <th className="is-num">Null %</th>
                      <th className="is-num">Unique %</th>
                      <th>Min</th>
                      <th>Max</th>
                      <th className="is-num">Mean</th>
                    </tr>
                  </thead>
                  <tbody>
                    {p.columns.map((c) => (
                      <tr key={c.name}>
                        <td>{c.name}</td>
                        <td>
                          <Badge>{c.type}</Badge>
                        </td>
                        <td className={cx('is-num', c.null_pct > 20 && 'is-warning')}>{c.null_pct.toFixed(1)}</td>
                        <td className="is-num">{c.unique_pct.toFixed(1)}</td>
                        <td>{c.min_val ?? <span className="bk-null">none</span>}</td>
                        <td>{c.max_val ?? <span className="bk-null">none</span>}</td>
                        <td className="is-num">{c.is_numeric && typeof c.mean_val === 'number' ? c.mean_val.toFixed(2) : ''}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ) : (
              <p className="bk-muted">The profile has no columns.</p>
            )}
          </>
        )}
      </section>
    </div>
  )
}
