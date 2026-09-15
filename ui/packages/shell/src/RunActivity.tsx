import { useEffect, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { dashboardKey, observeApi, watchKey, type DashboardRun } from '@brokoli/api'
import { useSession } from '@brokoli/auth'
import { ACTIVE, paths, useNow, useThrottledActivity } from '@brokoli/pipelines'
import { Button, Callout, Dot, Drawer, Skeleton, StatusBadge, cx, durationBetween, errorMessage, formatDuration, formatRelative, useToast } from '@brokoli/ui'
import { finishedRuns, remember, type LiveDashboard } from './runs'

const firstLine = (text?: string) => (text ?? '').split('\n', 1)[0].slice(0, 160)

/*
 * Live running count and run-finished notices, from the organisation's
 * dashboard state. The first value is the baseline: nothing is announced for
 * runs that ended before the page opened. When several runs finish between
 * two updates they are announced as one notice.
 */
function useLiveRuns() {
  const { user } = useSession()
  const toast = useToast()
  const [running, setRunning] = useState<number | null>(null)
  const orgId = user?.org_id
  const signedIn = Boolean(user)
  const toastRef = useRef(toast)
  toastRef.current = toast
  useEffect(() => {
    if (!signedIn) return
    const seen = new Map<string, string>()
    const since = Date.now()
    let baseline = true
    return watchKey<LiveDashboard>(dashboardKey(orgId), (value) => {
      const runs = value?.recent_runs ?? []
      setRunning(value?.runs_running ?? 0)
      if (baseline) {
        baseline = false
        remember(seen, runs)
        return
      }
      const done = finishedRuns(seen, runs, since)
      remember(seen, runs)
      const say = toastRef.current
      if (done.length > 3) {
        const failed = done.filter((r) => r.status.toLowerCase() === 'failed').length
        say[failed ? 'warning' : 'success'](`${done.length} runs finished`, failed ? `${failed} failed` : undefined)
        return
      }
      for (const r of done) {
        const name = r.pipeline_name || 'A pipeline'
        const status = r.status.toLowerCase()
        if (status === 'failed') say.error(`${name} failed`, firstLine(r.error) || undefined)
        else if (status === 'cancelled' || status === 'canceled') say.info(`${name} was cancelled`)
        else say.success(`${name} finished`)
      }
    })
  }, [signedIn, orgId])
  return running
}

function duration(run: DashboardRun, now: number) {
  if (run.finished_at) {
    const ms = durationBetween(run.started_at, run.finished_at)
    return ms === 0 ? 'under 1s' : formatDuration(ms)
  }
  return ACTIVE.has(run.status.toLowerCase()) ? formatDuration(durationBetween(run.started_at, null, now)) : ''
}

function ActivityDrawer({ onClose }: { onClose: () => void }) {
  const now = useNow(5_000)
  // Shares the dashboard page's cache entry, so opening this panel there costs no extra request.
  const query = useQuery({ queryKey: ['observe', 'dashboard'], queryFn: observeApi.dashboard })
  useThrottledActivity(() => void query.refetch(), 5_000)
  const runs = [...(query.data?.recent_runs ?? [])].sort((a, b) => Number(ACTIVE.has(b.status.toLowerCase())) - Number(ACTIVE.has(a.status.toLowerCase()))).slice(0, 25)
  return (
    <Drawer
      title="Run activity"
      description="The newest runs across every pipeline, runs in flight first."
      onClose={onClose}
      width={460}
      footer={
        <Link className="bk-button bk-button-secondary bk-button-md" to="/dashboard" onClick={onClose}>
          Open the dashboard
        </Link>
      }
    >
      {query.isError ? (
        <Callout tone="danger" title="Recent runs could not be loaded" action={<Button size="sm" onClick={() => void query.refetch()}>Try again</Button>}>
          {errorMessage(query.error)}
        </Callout>
      ) : query.isPending ? (
        <Skeleton height={120} />
      ) : !runs.length ? (
        <p className="sh-quiet">No runs yet. Runs appear here as soon as a pipeline starts.</p>
      ) : (
        <ul className="sh-runs">
          {runs.map((r) => (
            <li key={r.run_id}>
              <Link to={paths.runs(r.pipeline_id, r.run_id)} onClick={onClose}>
                <strong>{r.pipeline_name || r.pipeline_id}</strong>
                <span className="sh-quiet">
                  {formatRelative(r.started_at, now)}
                  {duration(r, now) && `, ${duration(r, now)}`}
                </span>
              </Link>
              <StatusBadge status={r.status} />
            </li>
          ))}
        </ul>
      )}
    </Drawer>
  )
}

export function RunActivityButton({ collapsed }: { collapsed: boolean }) {
  const running = useLiveRuns()
  const [open, setOpen] = useState(false)
  const busy = (running ?? 0) > 0
  const text = running === null ? 'Run activity' : busy ? `${running} running` : 'No runs in flight'
  return (
    <>
      <button type="button" className={cx('sh-status-button', busy && 'is-busy')} onClick={() => setOpen(true)} title={`${text}. Show recent runs`} aria-label={`${text}. Show recent runs`}>
        <Dot tone={busy ? 'running' : 'neutral'} pulse={busy} />
        {collapsed ? busy && <span className="sh-count">{running}</span> : <span>{busy ? `${running} running` : 'Idle'}</span>}
      </button>
      {open && <ActivityDrawer onClose={() => setOpen(false)} />}
    </>
  )
}
