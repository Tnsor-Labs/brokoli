import { useMemo } from 'react'
import { Database, ShieldAlert } from 'lucide-react'
import type { RunEvent } from '@brokoli/api'
import { EmptyState, cx } from '@brokoli/ui'
import { classifyStatement, groupQueries, statementReason } from './queries'

/**
 * The exact SQL each node ran, grouped by node and attempt. Sourced from the run
 * events endpoint (`attempt.query` events) — no separate call. Shaping lives in
 * ./queries; this renders it, handling the four display states honestly.
 */
export function Queries({ events, nodeNames }: { events: RunEvent[]; nodeNames: Record<string, string> }) {
  const groups = useMemo(() => groupQueries(events), [events])

  if (groups.length === 0)
    return (
      <EmptyState title="No SQL recorded">
        The exact statement each node runs appears here as SQL nodes execute. Some writes stream rows in bulk and have no statement to show.
      </EmptyState>
    )

  return (
    <div className="bk-queries">
      {groups.map((group) => (
        <section key={`${group.nodeId ?? 'run'}-${group.attempt}`} className="bk-query-group">
          <header className="bk-query-group-head">
            <strong>{group.nodeId ? nodeNames[group.nodeId] ?? group.nodeId : 'Run'}</strong>
            <span>attempt {group.attempt + 1}</span>
            <span className="bk-query-count bk-mono">
              {group.statements.length} statement{group.statements.length === 1 ? '' : 's'}
            </span>
          </header>
          <ol className="bk-query-list">
            {group.statements.map((s, i) => (
              <StatementBlock key={String(s.id)} statement={s.statement} index={i + 1} total={group.statements.length} />
            ))}
          </ol>
        </section>
      ))}
    </div>
  )
}

function StatementBlock({ statement, index, total }: { statement: string; index: number; total: number }) {
  const { kind, truncated } = classifyStatement(statement)

  if (kind !== 'sql') {
    const Icon = kind === 'withheld' ? ShieldAlert : Database
    const title = kind === 'withheld' ? 'Statement withheld' : 'No SQL statement'
    return (
      <li className="bk-query-note">
        <Icon size={16} aria-hidden="true" />
        <div>
          <span className="bk-query-note-title">{title}</span>
          <p>{statementReason(statement)}</p>
        </div>
      </li>
    )
  }

  return (
    <li className={cx('bk-query-item', truncated && 'is-truncated')}>
      {total > 1 && (
        <span className="bk-query-index bk-mono" aria-hidden="true">
          {index}
        </span>
      )}
      <pre className="bk-query-sql">
        <code>{statement}</code>
      </pre>
      {truncated && <span className="bk-query-tag">truncated</span>}
    </li>
  )
}
