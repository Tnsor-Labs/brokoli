import { useMemo, useState } from 'react'
import { Check, Copy, Database, ShieldAlert } from 'lucide-react'
import type { RunEvent } from '@brokoli/api'
import { EmptyState, IconButton, cx, useToast } from '@brokoli/ui'
import { classifyStatement, groupQueries, isGeneratedSqlNode, statementReason } from './queries'
import { highlightSql } from './sqlHighlight'

/**
 * The exact SQL each node ran, grouped by node and attempt. Sourced from the run
 * events endpoint (`attempt.query` events) — no separate call. Shaping lives in
 * ./queries; this renders it, handling the four display states honestly.
 *
 * Statements a sink node synthesized (the engine-generated writes) are hidden —
 * the panel is for the SQL you wrote, with its variables resolved. See
 * brokoli#667; `nodeTypes` maps a node id to its type so we can tell them apart.
 */
export function Queries({ events, nodeNames, nodeTypes }: { events: RunEvent[]; nodeNames: Record<string, string>; nodeTypes: Record<string, string> }) {
  const { groups, hidden } = useMemo(() => {
    const authored = events.filter((e) => e.event_type !== 'attempt.query' || !isGeneratedSqlNode(nodeTypes[e.node_id ?? '']))
    const hidden = events.filter((e) => e.event_type === 'attempt.query' && isGeneratedSqlNode(nodeTypes[e.node_id ?? ''])).length
    return { groups: groupQueries(authored), hidden }
  }, [events, nodeTypes])

  if (groups.length === 0)
    return (
      <EmptyState title="No SQL recorded">
        {hidden > 0
          ? 'Only engine-generated writes ran on this run. The SQL you write — a source query, a transform — appears here once a node executes one.'
          : 'The exact statement each node runs appears here as SQL nodes execute. Some writes stream rows in bulk and have no statement to show.'}
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
      {hidden > 0 && (
        <p className="bk-query-hidden-note">
          {hidden} engine-generated write{hidden === 1 ? '' : 's'} (table creation, inserts) {hidden === 1 ? 'is' : 'are'} not shown.
        </p>
      )}
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
