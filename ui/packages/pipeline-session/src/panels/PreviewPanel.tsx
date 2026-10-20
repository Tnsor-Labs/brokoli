import { useState } from 'react'
import { X } from 'lucide-react'
import type { DryRunResponse } from '@brokoli/api'
import { Badge, Callout, IconButton, StatusBadge, cx } from '@brokoli/ui'

function cell(value: unknown) {
  if (value === null || value === undefined) return <span className="bk-null">null</span>
  if (typeof value === 'object') return JSON.stringify(value)
  return String(value)
}

/*
 * Preview output. The server runs the saved pipeline on at most 10 rows per
 * node in a scratch store and returns each node's output, status and error.
 * Nodes that may write outside Brokoli (outputs, notifications, shell, dbt)
 * are held back, as is what follows them, and no run is recorded.
 */
export function PreviewPanel({ result, nodeNames, onClose }: { result: DryRunResponse; nodeNames: Record<string, string>; onClose: () => void }) {
  const entries = Object.values(result.results ?? {})
  const [active, setActive] = useState(() => entries.find((r) => r.rows?.length)?.node_id ?? entries[0]?.node_id ?? '')
  const current = entries.find((r) => r.node_id === active)
  const columns = current?.columns ?? []
  const rows = current?.rows ?? []
  return (
    <section className="ps-preview" aria-label="Preview results">
      <header className="ps-preview-head">
        <div>
          <h3>Preview</h3>
          <p>
            The saved pipeline, run on up to 10 rows per node. Nothing is written: output nodes and others that act outside Brokoli are held back, and no run is
            recorded.
          </p>
        </div>
        <IconButton label="Close preview" onClick={onClose}>
          <X size={16} aria-hidden="true" />
        </IconButton>
      </header>
      {result.error && (
        <Callout tone="danger" title="The preview stopped early">
          {result.error}
        </Callout>
      )}
      {!entries.length ? (
        <p className="ps-form-note">No node produced output.</p>
      ) : (
        <div className="ps-preview-body">
          <div className="ps-preview-tabs" role="tablist" aria-label="Node">
            {entries.map((r) => (
              <button key={r.node_id} type="button" role="tab" aria-selected={r.node_id === active} className={cx(r.node_id === active && 'is-active')} onClick={() => setActive(r.node_id)}>
                <span>{nodeNames[r.node_id] ?? r.name ?? r.node_id}</span>
                {r.status === 'held' || r.status === 'not_run' ? (
                  <Badge tone="neutral">{r.status === 'held' ? 'held' : 'not run'}</Badge>
                ) : (
                  <StatusBadge status={r.error ? 'failed' : r.status} />
                )}
                <small className="bk-mono">{r.rows?.length ?? 0}</small>
              </button>
            ))}
          </div>
          <div className="ps-preview-content">
            {current?.error && <Callout tone="danger">{current.error}</Callout>}
            {current?.status === 'skipped' && <p className="ps-form-note">Skipped: this node is on a branch that did not match.</p>}
            {(current?.status === 'held' || current?.status === 'not_run') && (
              <p className="ps-form-note">{current.reason}.</p>
            )}
            {columns.length > 0 ? (
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
                    {rows.map((row, i) => (
                      <tr key={i}>
                        <td className="bk-muted">{i + 1}</td>
                        {columns.map((c) => (
                          <td key={c}>{cell(row[c])}</td>
                        ))}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ) : (
              !current?.error &&
              current?.status !== 'skipped' &&
              current?.status !== 'held' &&
              current?.status !== 'not_run' && <p className="ps-form-note">This node produced no columns.</p>
            )}
          </div>
        </div>
      )}
    </section>
  )
}
