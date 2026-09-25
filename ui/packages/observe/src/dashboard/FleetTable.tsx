import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { PencilLine, Play } from 'lucide-react'
import { useSession } from '@brokoli/auth'
import { paths } from '@brokoli/pipelines'
import { Badge, Button, IconButton, SearchInput, StatusBadge, cx, formatDateTime, formatRelative, toDate } from '@brokoli/ui'
import { rateTone } from '../Stat'
import { useRunActions } from './actions'
import type { FleetRow } from './model'

const COLLAPSED = 10

export function nextLabel(next: string, now: number) {
  const t = toDate(next)?.getTime()
  if (t === undefined) return '-'
  return t <= now ? 'Due now' : formatRelative(next, now)
}

/** Every pipeline with its latest outcome, failing first, then running, enabled and paused. */
export function FleetTable({ rows, now }: { rows: FleetRow[]; now: number }) {
  const session = useSession()
  const actions = useRunActions()
  const [search, setSearch] = useState('')
  const [expanded, setExpanded] = useState(false)
  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase()
    return q ? rows.filter((r) => `${r.pipeline.name} ${(r.pipeline.tags ?? []).join(' ')}`.toLowerCase().includes(q)) : rows
  }, [rows, search])
  const shown = expanded || search ? filtered : filtered.slice(0, COLLAPSED)

  return (
    <div className="ob-fleet">
      {rows.length > COLLAPSED && <SearchInput value={search} onChange={setSearch} placeholder="Find a pipeline by name or tag" className="ob-fleet-search" />}
      <div className="bk-table-wrap">
        <table className="bk-table ob-fleet-table">
          <thead>
            <tr>
              <th scope="col">Pipeline</th>
              <th scope="col">Schedule</th>
              <th scope="col">Last run</th>
              <th scope="col">Last 24 hours</th>
              <th scope="col">Health</th>
              <th scope="col">Next run</th>
              <th scope="col">
                <span className="ob-sr-only">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {shown.map(({ pipeline: p, rollup, next, health }) => (
              <tr key={p.id}>
                <td>
                  <Link className="ob-link-strong" to={paths.runs(p.id)}>
                    {p.name}
                  </Link>
                </td>
                <td>
                  {p.draft ? (
                    <Badge tone="queued">Draft</Badge>
                  ) : !p.enabled ? (
                    <Badge tone="cancelled">Paused</Badge>
                  ) : p.schedule ? (
                    <code className="ob-cron">{p.schedule}</code>
                  ) : (
                    <span className="ob-quiet">Manual</span>
                  )}
                </td>
                <td>
                  {p.last_run_status ? (
                    <span className="ob-inline">
                      <StatusBadge status={p.last_run_status} />
                      <span className="ob-small ob-quiet" title={formatDateTime(p.last_run_at)}>
                        {formatRelative(p.last_run_at, now)}
                      </span>
                    </span>
                  ) : (
                    <span className="ob-quiet">Never run</span>
                  )}
                </td>
                <td className="ob-small">
                  {rollup ? (
                    <>
                      {rollup.success} ok
                      {rollup.failed > 0 && <span className="ob-danger">, {rollup.failed} failed</span>}
                      {rollup.running > 0 && `, ${rollup.running} running`}
                    </>
                  ) : (
                    <span className="ob-quiet">No runs</span>
                  )}
                </td>
                <td>
                  {health === null ? (
                    <span className="ob-quiet">-</span>
                  ) : (
                    <span
                      className={cx('ob-health', `is-${rateTone(health)}`)}
                      title={`${p.runs_success} of ${p.runs_success + p.runs_failed} finished runs succeeded, among its newest 200 runs`}
                    >
                      <i>
                        <b style={{ width: `${health}%` }} />
                      </i>
                      {health}%
                    </span>
                  )}
                </td>
                <td className="ob-small">{next ? <span title={formatDateTime(next)}>{nextLabel(next, now)}</span> : <span className="ob-quiet">-</span>}</td>
                <td>
                  <span className="ob-row-actions is-visible">
                    {actions.canRun && (
                      <IconButton
                        size="sm"
                        label={p.draft ? `${p.name} is a draft and cannot run` : `Run ${p.name}`}
                        disabled={p.draft || actions.busy === `run:${p.id}`}
                        onClick={() => void actions.start(p.id, p.name)}
                      >
                        <Play size={14} aria-hidden="true" />
                      </IconButton>
                    )}
                    {session.can('pipelines.edit') && (
                      <Link className="bk-icon-button bk-icon-button-sm bk-icon-button-ghost" to={paths.editor(p.id)} aria-label={`Edit ${p.name}`} title={`Edit ${p.name}`}>
                        <PencilLine size={14} aria-hidden="true" />
                      </Link>
                    )}
                  </span>
                </td>
              </tr>
            ))}
            {!shown.length && (
              <tr>
                <td colSpan={7} className="ob-quiet">
                  No pipeline matches.
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
      {!search && filtered.length > COLLAPSED && (
        <Button size="sm" variant="ghost" onClick={() => setExpanded((v) => !v)}>
          {expanded ? 'Show fewer' : `Show all ${filtered.length} pipelines`}
        </Button>
      )}
    </div>
  )
}
