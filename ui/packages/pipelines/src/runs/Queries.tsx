import { useMemo, useState } from 'react'
import { Check, Copy, Database, ShieldAlert } from 'lucide-react'
import type { RunEvent } from '@brokoli/api'
import { EmptyState, IconButton, cx, useToast } from '@brokoli/ui'
import { classifyStatement, groupQueries, statementReason } from './queries'
import { highlightSql } from './sqlHighlight'

/**
 * The exact SQL each node ran, grouped by node and attempt. Sourced from the run
 * events endpoint (`attempt.query` events) — no separate call. The recorder
 * records author-written SQL only (brokoli#665, #669), so the panel shows every
 * statement it receives, on whatever node ran it; shaping lives in ./queries.
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

  const tokens = highlightSql(statement)
  return (
    <li className={cx('bk-query-item', truncated && 'is-truncated')}>
      {total > 1 && (
        <span className="bk-query-index bk-mono" aria-hidden="true">
          {index}
        </span>
      )}
      <div className="bk-query-code">
        <pre className="bk-query-sql">
          <code>
            {tokens.map((t, i) => (t.cls ? <span key={i} className={`bk-sql-${t.cls}`}>{t.text}</span> : t.text))}
          </code>
        </pre>
        <CopyButton statement={statement} />
      </div>
    </li>
  )
}

function CopyButton({ statement }: { statement: string }) {
  const toast = useToast()
  const [copied, setCopied] = useState(false)
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(statement)
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    } catch (e) {
      toast.error('Could not copy', e)
    }
  }
  return (
    <IconButton className="bk-query-copy" size="sm" variant="ghost" label={copied ? 'Copied' : 'Copy statement'} onClick={() => void copy()}>
      {copied ? <Check size={14} aria-hidden="true" /> : <Copy size={14} aria-hidden="true" />}
    </IconButton>
  )
}
