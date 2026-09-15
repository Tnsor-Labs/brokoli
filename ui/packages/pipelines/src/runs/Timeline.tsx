import type { NodeRun, PipelineNode, Run } from '@brokoli/api'
import { StatusBadge, cx, formatDuration, formatNumber, statusMeta, toDate } from '@brokoli/ui'
import { attemptsByNode, isActive } from './model'

/*
 * Execution timeline. One row per node of the pipeline, including nodes that
 * have not started yet (shown as waiting rather than hidden). A running
 * node's bar grows with elapsed time instead of sitting at a minimum width.
 * Earlier attempts of a retried node are drawn as faded segments on the
 * same track.
 */
export function Timeline({
  run,
  nodes,
  now,
  selected,
  onSelect,
}: {
  run: Run
  nodes: PipelineNode[]
  now: number
  selected: string | null
  onSelect: (nodeId: string) => void
}) {
  const attempts = attemptsByNode(run.node_runs)
  const span = (nr: NodeRun) => {
    const start = toDate(nr.started_at)?.getTime()
    if (start === undefined) return null
    const duration = nr.duration_ms > 0 ? nr.duration_ms : isActive(nr.status) ? Math.max(0, now - start) : 0
    return { start, end: start + duration, duration }
  }
  const spans = (run.node_runs ?? []).map(span).filter((s): s is NonNullable<typeof s> => Boolean(s))
  const runStart = toDate(run.started_at)?.getTime() ?? Math.min(...spans.map((s) => s.start))
  const runEnd = Math.max(toDate(run.finished_at)?.getTime() ?? 0, ...spans.map((s) => s.end), isActive(run.status) ? now : 0)
  const total = Math.max(1, runEnd - runStart)

  const known = new Set(nodes.map((n) => n.id))
  const rows = [
    ...nodes.map((n) => ({ id: n.id, name: n.name || n.id })),
    // Node runs for nodes no longer in the definition (the run used an older version).
    ...[...attempts.keys()].filter((id) => !known.has(id)).map((id) => ({ id, name: id })),
  ].sort((a, b) => {
    const sa = attempts.get(a.id)?.[0]
    const sb = attempts.get(b.id)?.[0]
    const ta = toDate(sa?.started_at)?.getTime() ?? Infinity
    const tb = toDate(sb?.started_at)?.getTime() ?? Infinity
    return ta - tb
  })

  if (!rows.length) return <p className="bk-muted">This pipeline has no nodes.</p>
  return (
    <div className="bk-timeline" role="list" aria-label="Execution timeline">
      <div className="bk-timeline-scale" aria-hidden="true">
        <span />
        <div>
          <span>0</span>
          <span>{formatDuration(total / 2)}</span>
          <span>{formatDuration(total)}</span>
        </div>
        <span />
      </div>
      {rows.map((row) => {
        const list = attempts.get(row.id) ?? []
        const primary = list[list.length - 1]
        const s = primary ? span(primary) : null
        return (
          <button
            key={row.id}
            type="button"
            role="listitem"
            className={cx('bk-timeline-row', selected === row.id && 'is-selected', primary?.status === 'failed' && 'is-failed')}
            onClick={() => onSelect(row.id)}
            aria-pressed={selected === row.id}
          >
            <span className="bk-timeline-label">
              <strong>{row.name}</strong>
              {list.length > 1 && <em title={`${list.length} attempts`}>x{list.length}</em>}
            </span>
            <span className="bk-timeline-track">
              {list.slice(0, -1).map((a, i) => {
                const as = span(a)
                if (!as) return null
                return (
                  <i
                    key={i}
                    className={`bk-timeline-bar is-earlier bk-tone-${statusMeta(a.status).tone}`}
                    style={{ left: `${((as.start - runStart) / total) * 100}%`, width: `max(3px, ${(as.duration / total) * 100}%)` }}
                    title={`Attempt ${(a.attempt ?? 0) + 1}: ${statusMeta(a.status).label}, ${formatDuration(a.duration_ms)}`}
                  />
                )
              })}
              {s ? (
                <i
                  className={cx('bk-timeline-bar', `bk-tone-${statusMeta(primary!.status).tone}`, isActive(primary!.status) && 'is-live')}
                  style={{ left: `${((s.start - runStart) / total) * 100}%`, width: `max(3px, ${(s.duration / total) * 100}%)` }}
                />
              ) : (
                <span className="bk-timeline-waiting">{primary ? statusMeta(primary.status).label : isActive(run.status) ? 'Waiting' : 'Did not run'}</span>
              )}
            </span>
            <span className="bk-timeline-meta">
              {primary && <StatusBadge status={primary.status} />}
              <span className="bk-mono">{s ? formatDuration(s.duration) : ''}</span>
              <span className="bk-mono bk-muted">{primary?.row_count ? `${formatNumber(primary.row_count, { compact: true })} rows` : ''}</span>
            </span>
          </button>
        )
      })}
    </div>
  )
}
