import { useState } from 'react'
import { Link } from 'react-router-dom'
import { Play, Square } from 'lucide-react'
import type { DashboardRun, DashboardStats } from '@brokoli/api'
import { ACTIVE, FAILURE, paths } from '@brokoli/pipelines'
import { ConfirmDialog, Dot, IconButton, StatusBadge, durationBetween, formatDuration, formatRelative, statusMeta } from '@brokoli/ui'
import { useRunActions } from './actions'
import { firstLine, groupRecent } from './model'

/* The dashboard sends whole-second timestamps, so a finished run that took less than a second reads as 0. */
function runDuration(run: DashboardRun, now: number) {
  if (run.finished_at) {
    const ms = durationBetween(run.started_at, run.finished_at)
    return ms === 0 ? 'under 1s' : formatDuration(ms)
  }
  return ACTIVE.has(run.status.toLowerCase()) ? formatDuration(durationBetween(run.started_at, null, now)) : formatDuration(null)
}

/** The newest runs, one row per pipeline, with the status of its other recent runs as dots. */
export function RecentRuns({ stats, now }: { stats: DashboardStats; now: number }) {
  const actions = useRunActions()
  const [cancelling, setCancelling] = useState<{ runId: string; name: string } | null>(null)
  const { groups, hidden } = groupRecent(stats.recent_runs ?? [], 8)
  const rollups = new Map((stats.pipeline_rollups ?? []).map((r) => [r.pipeline_id, r]))
  if (!groups.length) return <p className="ob-empty-line">No runs yet. They appear here as soon as a pipeline starts.</p>
  return (
    <>
      <ul className="ob-groups">
        {groups.map((g) => {
          const head = g.runs[0]
          const status = head.status.toLowerCase()
          const rollup = rollups.get(g.pipelineId)
          return (
            <li key={g.pipelineId} className="ob-group">
              <div className="ob-group-main">
                <Link className="ob-link-strong" to={paths.runs(g.pipelineId, head.run_id)}>
                  {g.name}
                </Link>
                <span className="ob-history" role="img" aria-label={`Its ${Math.min(12, g.runs.length)} newest runs: ${g.runs.slice(0, 12).map((r) => statusMeta(r.status).label).join(', ')}`}>
                  {g.runs.slice(0, 12).map((r) => (
                    <span key={r.run_id} title={`${statusMeta(r.status).label}, ${formatRelative(r.started_at, now)}`}>
                      <Dot tone={statusMeta(r.status).tone} />
                    </span>
                  ))}
                </span>
                {rollup && (
                  <span className="ob-small ob-quiet">
                    {rollup.success} ok{rollup.failed ? `, ${rollup.failed} failed` : ''} in 24 hours
                  </span>
                )}
              </div>
              <div className="ob-group-meta">
                <StatusBadge status={head.status} />
                <span className="bk-mono ob-small">{runDuration(head, now)}</span>
                <span className="ob-small ob-quiet ob-when">{formatRelative(head.started_at, now)}</span>
                <span className="ob-row-actions">
                  {ACTIVE.has(status) && actions.canCancel && (
                    <IconButton size="sm" label={`Cancel the run of ${g.name}`} onClick={() => setCancelling({ runId: head.run_id, name: g.name })}>
                      <Square size={14} aria-hidden="true" />
                    </IconButton>
                  )}
                  {actions.canRun && (
                    <IconButton size="sm" label={`Run ${g.name} again`} disabled={actions.busy === `run:${g.pipelineId}`} onClick={() => void actions.start(g.pipelineId, g.name)}>
                      <Play size={14} aria-hidden="true" />
                    </IconButton>
                  )}
                </span>
              </div>
              {FAILURE.has(status) && head.error && (
                <p className="ob-error-line" title={head.error}>
                  {firstLine(head.error)}
                </p>
              )}
            </li>
          )
        })}
      </ul>
      {hidden > 0 && (
        <Link className="ob-more" to="/pipelines">
          {hidden} more pipeline{hidden === 1 ? '' : 's'} ran recently
        </Link>
      )}
      {cancelling && (
        <ConfirmDialog
          title={`Cancel the run of ${cancelling.name}?`}
          tone="danger"
          confirmLabel="Cancel run"
          cancelLabel="Keep running"
          onCancel={() => setCancelling(null)}
          onConfirm={async () => {
            await actions.cancel(cancelling.runId, cancelling.name)
            setCancelling(null)
          }}
        >
          The server stops the run and records it as cancelled.
        </ConfirmDialog>
      )}
    </>
  )
}
