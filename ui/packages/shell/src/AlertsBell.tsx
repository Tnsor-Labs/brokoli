import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { Bell } from 'lucide-react'
import { observeApi, type Alert } from '@brokoli/api'
import { paths, useNow, useThrottledActivity } from '@brokoli/pipelines'
import { Button, Callout, Dot, Drawer, EmptyState, Skeleton, cx, errorMessage, formatDateTime, formatRelative, useToast, type Tone } from '@brokoli/ui'

const LIMIT = 30
const KEY = ['observe', 'alerts'] as const

const severityTone = (s: string): Tone => (s === 'critical' ? 'danger' : s === 'warning' ? 'warning' : 'accent')
const firstLine = (text?: string) => (text ?? '').split('\n', 1)[0].slice(0, 200)

/*
 * Alert inbox. Read and dismissed are stored on the alert itself, so they
 * are shared by the whole organisation; the drawer says so. After every
 * action the list is fetched again rather than adjusted locally, so the
 * unread count cannot drift from the server.
 */
function AlertsDrawer({ onClose, incidents = false, currentUserId }: { onClose: () => void; incidents?: boolean; currentUserId?: string }) {
  const toast = useToast()
  const queryClient = useQueryClient()
  const now = useNow(30_000)
  const query = useQuery({ queryKey: KEY, queryFn: () => observeApi.alerts(LIMIT) })
  const [busy, setBusy] = useState<string | null>(null)
  const act = async (id: string, run: () => Promise<unknown>, failure: string) => {
    setBusy(id)
    try {
      await run()
    } catch (e) {
      toast.error(failure, e)
    } finally {
      setBusy(null)
      void queryClient.invalidateQueries({ queryKey: KEY })
    }
  }
  const alerts: Alert[] = query.data?.alerts ?? []
  const unread = query.data?.unread ?? 0
  return (
    <Drawer
      title="Alerts"
      description="Read and dismissed apply to everyone in the organisation, not only to you."
      onClose={onClose}
      footer={
        unread > 0 ? (
          <Button loading={busy === 'all'} onClick={() => void act('all', observeApi.readAllAlerts, 'Could not mark all read')}>
            Mark all {unread} read
          </Button>
        ) : undefined
      }
    >
      {query.isError ? (
        <Callout tone="danger" title="Could not load alerts" action={<Button size="sm" onClick={() => void query.refetch()}>Try again</Button>}>
          {errorMessage(query.error)}
        </Callout>
      ) : query.isPending ? (
        <Skeleton height={64} />
      ) : !alerts.length ? (
        <EmptyState title="No alerts">An alert is raised when a run fails.</EmptyState>
      ) : (
        <ul className="sh-alerts">
          {alerts.map((a) => (
            <li key={a.id} className={cx('sh-alert', !a.read_at && 'is-unread')}>
              <Dot tone={severityTone(a.severity)} />
              <div className="sh-alert-body">
                <strong>{a.title}</strong>
                <span className="sh-quiet" title={formatDateTime(a.created_at)}>
                  {formatRelative(a.created_at, now)}
                  {!a.read_at && ', unread'}
                </span>
                {a.body && (
                  <p className="sh-error-line" title={a.body}>
                    {firstLine(a.body)}
                  </p>
                )}
                {incidents && (
                  <span className={cx('sh-alert-incident', a.resolved_at && 'is-resolved')}>
                    {a.resolved_at ? (
                      `Resolved ${formatRelative(a.resolved_at, now)}`
                    ) : (
                      <>
                        {a.assignee_user_id ? (a.assignee_user_id === currentUserId ? 'Assigned to you' : 'Assigned') : 'Unassigned'}
                        {a.acknowledged_at && `, acknowledged ${formatRelative(a.acknowledged_at, now)}`}
                      </>
                    )}
                  </span>
                )}
                <div className="sh-alert-actions">
                  {a.pipeline_id && (
                    <Link className="bk-button bk-button-ghost bk-button-sm" to={paths.runs(a.pipeline_id, a.run_id)} onClick={onClose}>
                      View run
                    </Link>
                  )}
                  {incidents && !a.resolved_at && (
                    <>
                      {a.assignee_user_id === currentUserId ? (
                        <Button size="sm" variant="ghost" disabled={busy !== null} onClick={() => void act(a.id, () => observeApi.assignAlert(a.id, null), 'Could not unassign')}>
                          Unassign
                        </Button>
                      ) : (
                        currentUserId && (
                          <Button size="sm" variant="ghost" disabled={busy !== null} onClick={() => void act(a.id, () => observeApi.assignAlert(a.id, currentUserId), 'Could not assign')}>
                            Assign to me
                          </Button>
                        )
                      )}
                      {!a.acknowledged_at && (
                        <Button size="sm" variant="ghost" disabled={busy !== null} onClick={() => void act(a.id, () => observeApi.acknowledgeAlert(a.id), 'Could not acknowledge')}>
                          Acknowledge
                        </Button>
                      )}
                      <Button size="sm" variant="ghost" disabled={busy !== null} onClick={() => void act(a.id, () => observeApi.resolveAlert(a.id), 'Could not resolve')}>
                        Resolve
                      </Button>
                    </>
                  )}
                  {!a.read_at && (
                    <Button size="sm" variant="ghost" disabled={busy !== null} onClick={() => void act(a.id, () => observeApi.readAlert(a.id), 'Could not mark read')}>
                      Mark read
                    </Button>
                  )}
                  <Button size="sm" variant="ghost" disabled={busy !== null} onClick={() => void act(a.id, () => observeApi.dismissAlert(a.id), 'Could not dismiss')}>
                    Dismiss
                  </Button>
                </div>
              </div>
            </li>
          ))}
          {unread > alerts.filter((a) => !a.read_at).length && <li className="sh-quiet">Showing the newest {LIMIT}. Older unread alerts are counted but not listed.</li>}
        </ul>
      )}
    </Drawer>
  )
}

/*
 * The bell lives in the sidebar so unread alerts are visible on every page;
 * the previous interface showed it only on the dashboard. Alerts are raised
 * when a run fails, so the count refreshes on run activity, at most every
 * 30 seconds.
 */
export function AlertsBell({ collapsed, incidents = false, currentUserId }: { collapsed: boolean; incidents?: boolean; currentUserId?: string }) {
  const [open, setOpen] = useState(false)
  const query = useQuery({ queryKey: KEY, queryFn: () => observeApi.alerts(LIMIT) })
  useThrottledActivity(() => void query.refetch(), 30_000)
  const unread = query.data?.unread ?? 0
  const label = unread ? `Alerts, ${unread} unread` : 'Alerts'
  return (
    <>
      <button type="button" className={cx('sh-status-button', unread > 0 && 'has-unread')} onClick={() => setOpen(true)} title={label} aria-label={label}>
        <Bell size={15} aria-hidden="true" />
        {!collapsed && <span>Alerts</span>}
        {unread > 0 && <span className="sh-count is-danger">{unread > 99 ? '99+' : unread}</span>}
      </button>
      {open && <AlertsDrawer onClose={() => setOpen(false)} incidents={incidents} currentUserId={currentUserId} />}
    </>
  )
}
