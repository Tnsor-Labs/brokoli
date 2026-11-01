import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { Bell } from 'lucide-react'
import { messagesApi, observeApi, type Alert, type InboxMessage } from '@brokoli/api'
import { paths, useNow, useThrottledActivity } from '@brokoli/pipelines'
import {
  AgentAvatar,
  Badge,
  Button,
  Callout,
  Dot,
  Drawer,
  EmptyState,
  SegmentedControl,
  Skeleton,
  Switch,
  cx,
  errorMessage,
  formatDateTime,
  formatRelative,
  useToast,
  type Tone,
} from '@brokoli/ui'
import {
  LEVEL_LABEL,
  firstLine as messageLine,
  inboxCount,
  levelTone,
  linkLabel,
  linkPath,
} from './inbox'

const LIMIT = 30
const KEY = ['observe', 'alerts'] as const
const MESSAGES_KEY = ['messages', 'inbox'] as const

const severityTone = (s: string): Tone =>
  s === 'critical' ? 'danger' : s === 'warning' ? 'warning' : 'accent'
const firstLine = (text?: string) => (text ?? '').split('\n', 1)[0].slice(0, 200)

/*
 * Alert inbox. Read and dismissed are stored on the alert itself, so they
 * are shared by the whole organisation; the drawer says so. After every
 * action the list is fetched again rather than adjusted locally, so the
 * unread count cannot drift from the server.
 */
function AlertsPanel({
  onClose,
  incidents = false,
  currentUserId,
}: {
  onClose: () => void
  incidents?: boolean
  currentUserId?: string
}) {
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
    <>
      <div className="sh-inbox-bar">
        <span className="sh-quiet">
          Read and dismissed apply to everyone in the organisation, not only to you.
        </span>
        {unread > 0 && (
          <Button
            size="sm"
            loading={busy === 'all'}
            onClick={() => void act('all', observeApi.readAllAlerts, 'Could not mark all read')}
          >
            Mark all {unread} read
          </Button>
        )}
      </div>
      {query.isError ? (
        <Callout
          tone="danger"
          title="Could not load alerts"
          action={
            <Button size="sm" onClick={() => void query.refetch()}>
              Try again
            </Button>
          }
        >
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
                        {a.assignee_user_id
                          ? a.assignee_user_id === currentUserId
                            ? 'Assigned to you'
                            : 'Assigned'
                          : 'Unassigned'}
                        {a.acknowledged_at &&
                          `, acknowledged ${formatRelative(a.acknowledged_at, now)}`}
                      </>
                    )}
                  </span>
                )}
                <div className="sh-alert-actions">
                  {a.pipeline_id && (
                    <Link
                      className="bk-button bk-button-ghost bk-button-sm"
                      to={paths.runs(a.pipeline_id, a.run_id)}
                      onClick={onClose}
                    >
                      View run
                    </Link>
                  )}
                  {incidents && !a.resolved_at && (
                    <>
                      {a.assignee_user_id === currentUserId ? (
                        <Button
                          size="sm"
                          variant="ghost"
                          disabled={busy !== null}
                          onClick={() =>
                            void act(
                              a.id,
                              () => observeApi.assignAlert(a.id, null),
                              'Could not unassign',
                            )
                          }
                        >
                          Unassign
                        </Button>
                      ) : (
                        currentUserId && (
                          <Button
                            size="sm"
                            variant="ghost"
                            disabled={busy !== null}
                            onClick={() =>
                              void act(
                                a.id,
                                () => observeApi.assignAlert(a.id, currentUserId),
                                'Could not assign',
                              )
                            }
                          >
                            Assign to me
                          </Button>
                        )
                      )}
                      {!a.acknowledged_at && (
                        <Button
                          size="sm"
                          variant="ghost"
                          disabled={busy !== null}
                          onClick={() =>
                            void act(
                              a.id,
                              () => observeApi.acknowledgeAlert(a.id),
                              'Could not acknowledge',
                            )
                          }
                        >
                          Acknowledge
                        </Button>
                      )}
                      <Button
                        size="sm"
                        variant="ghost"
                        disabled={busy !== null}
                        onClick={() =>
                          void act(a.id, () => observeApi.resolveAlert(a.id), 'Could not resolve')
                        }
                      >
                        Resolve
                      </Button>
                    </>
                  )}
                  {!a.read_at && (
                    <Button
                      size="sm"
                      variant="ghost"
                      disabled={busy !== null}
                      onClick={() =>
                        void act(a.id, () => observeApi.readAlert(a.id), 'Could not mark read')
                      }
                    >
                      Mark read
                    </Button>
                  )}
                  <Button
                    size="sm"
                    variant="ghost"
                    disabled={busy !== null}
                    onClick={() =>
                      void act(a.id, () => observeApi.dismissAlert(a.id), 'Could not dismiss')
                    }
                  >
                    Dismiss
                  </Button>
                </div>
              </div>
            </li>
          ))}
          {unread > alerts.filter((a) => !a.read_at).length && (
            <li className="sh-quiet">
              Showing the newest {LIMIT}. Older unread alerts are counted but not listed.
            </li>
          )}
        </ul>
      )}
    </>
  )
}

/*
 * Messages from agents (ADR-048). Opening one shows its conversation and
 * marks it read; a conversation can be muted, after which the agent's
 * messages arrive quietly and do not count on the bell.
 */
function MessagesPanel({ onClose }: { onClose: () => void }) {
  const toast = useToast()
  const queryClient = useQueryClient()
  const now = useNow(30_000)
  const query = useQuery({ queryKey: MESSAGES_KEY, queryFn: () => messagesApi.inbox(LIMIT) })
  const [open, setOpen] = useState<string | null>(null)
  const refresh = () => queryClient.invalidateQueries({ queryKey: MESSAGES_KEY })
  const messages: InboxMessage[] = query.data?.messages ?? []
  const unread = messages.filter((m) => !m.read_at).length

  const openMessage = async (m: InboxMessage) => {
    setOpen(open === m.id ? null : m.id)
    if (!m.read_at) {
      try {
        await messagesApi.read(m.id)
        await refresh()
      } catch (e) {
        toast.error('Could not mark read', e)
      }
    }
  }
  if (query.data === null)
    return (
      <EmptyState title="No messages">This server does not carry messages from agents.</EmptyState>
    )
  return (
    <>
      <div className="sh-inbox-bar">
        <span className="sh-quiet">Messages your agents and your team's agents sent you.</span>
        {unread > 0 && (
          <Button
            size="sm"
            onClick={() =>
              void messagesApi
                .read()
                .then(refresh)
                .catch((e) => toast.error('Could not mark all read', e))
            }
          >
            Mark all {unread} read
          </Button>
        )}
      </div>
      {query.isError ? (
        <Callout
          tone="danger"
          title="Could not load messages"
          action={
            <Button size="sm" onClick={() => void query.refetch()}>
              Try again
            </Button>
          }
        >
          {errorMessage(query.error)}
        </Callout>
      ) : query.isPending ? (
        <Skeleton height={64} />
      ) : !messages.length ? (
        <EmptyState title="No messages yet">
          When an agent has something to tell you, such as a run it looked into or a decision it
          needs, it appears here.
        </EmptyState>
      ) : (
        <ul className="sh-alerts">
          {messages.map((m) => (
            <li key={m.id} className={cx('sh-alert', 'sh-message', !m.read_at && 'is-unread')}>
              <AgentAvatar avatar={m.agent_avatar} size={32} />
              <div className="sh-alert-body">
                <button
                  type="button"
                  className="sh-message-head"
                  aria-expanded={open === m.id}
                  onClick={() => void openMessage(m)}
                >
                  <strong className="bk-mono">{m.agent_name || 'An agent'}</strong>
                  {m.shown_level !== 'info' && (
                    <Badge tone={levelTone(m.shown_level)}>{LEVEL_LABEL[m.shown_level]}</Badge>
                  )}
                  <span className="sh-quiet" title={formatDateTime(m.created_at)}>
                    {formatRelative(m.created_at, now)}
                    {m.muted && ', muted'}
                  </span>
                </button>
                {m.topic && <span className="sh-message-topic">{m.topic}</span>}
                {open === m.id ? (
                  <p className="sh-conversation-text">{m.text}</p>
                ) : (
                  <p className="sh-message-line">{messageLine(m.text)}</p>
                )}
                {m.links.length > 0 && <MessageLinks links={m.links} onClose={onClose} />}
                {open === m.id && (
                  <MessageControls conversationId={m.conversation_id} onChanged={refresh} />
                )}
              </div>
            </li>
          ))}
        </ul>
      )}
    </>
  )
}

function MessageLinks({ links, onClose }: { links: InboxMessage['links']; onClose: () => void }) {
  return (
    <div className="sh-message-links">
      {links.map((l) => {
        const to = linkPath(l)
        return to ? (
          <Link key={l.kind + l.id} to={to} onClick={onClose}>
            {linkLabel(l)}
          </Link>
        ) : (
          <span key={l.kind + l.id}>{linkLabel(l)}</span>
        )
      })}
    </div>
  )
}

/** Who an opened message is from, and muting that agent's conversation. */
function MessageControls({
  conversationId,
  onChanged,
}: {
  conversationId: string
  onChanged: () => unknown
}) {
  const toast = useToast()
  const queryClient = useQueryClient()
  const key = ['messages', 'conversation', conversationId]
  const query = useQuery({ queryKey: key, queryFn: () => messagesApi.conversation(conversationId) })
  if (!query.data) return null
  const { conversation, agent } = query.data
  const name = agent.name ?? 'The agent'
  const setMuted = async (muted: boolean) => {
    try {
      await messagesApi.mute(conversationId, muted)
      await queryClient.invalidateQueries({ queryKey: key })
      await onChanged()
      toast.success(
        muted ? `${name} is muted` : `${name} is unmuted`,
        muted ? 'Its messages still arrive, quietly, and do not count on the bell.' : undefined,
      )
    } catch (e) {
      toast.error('Could not change it', e)
    }
  }
  return (
    <div className="sh-conversation-foot">
      <span className="sh-quiet">
        From an agent{agent.owner_name ? `, owned by ${agent.owner_name}` : ''}. Replying comes in a
        later release.
      </span>
      <Switch
        checked={conversation.muted}
        label={conversation.muted ? `Unmute ${name}` : `Mute ${name}`}
        onChange={(next) => void setMuted(next)}
      />
      <span className="sh-quiet">{conversation.muted ? 'Muted' : 'Mute'}</span>
    </div>
  )
}

/** The inbox: messages from agents, and alerts. */
function InboxDrawer({
  onClose,
  incidents,
  currentUserId,
  needsYou,
}: {
  onClose: () => void
  incidents?: boolean
  currentUserId?: string
  needsYou: number
}) {
  const [tab, setTab] = useState<'messages' | 'alerts'>('messages')
  return (
    <Drawer
      title="Inbox"
      description="What your agents told you, and the alerts runs raised."
      onClose={onClose}
    >
      <div className="sh-inbox-tabs">
        <SegmentedControl
          label="Show"
          value={tab}
          onChange={setTab}
          options={[
            { value: 'messages', label: 'Messages', count: needsYou || undefined },
            { value: 'alerts', label: 'Alerts' },
          ]}
        />
      </div>
      {tab === 'messages' ? (
        <MessagesPanel onClose={onClose} />
      ) : (
        <AlertsPanel onClose={onClose} incidents={incidents} currentUserId={currentUserId} />
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
export function AlertsBell({
  collapsed,
  incidents = false,
  currentUserId,
}: {
  collapsed: boolean
  incidents?: boolean
  currentUserId?: string
}) {
  const [open, setOpen] = useState(false)
  const query = useQuery({ queryKey: KEY, queryFn: () => observeApi.alerts(LIMIT) })
  // Messages are checked every minute: an agent can write at any time, not
  // only when a run ends.
  const messages = useQuery({
    queryKey: MESSAGES_KEY,
    queryFn: () => messagesApi.inbox(LIMIT),
    refetchInterval: 60_000,
  })
  useThrottledActivity(() => void query.refetch(), 30_000)
  const needsYou = messages.data?.needsYou ?? 0
  const count = inboxCount(query.data?.unread ?? 0, needsYou)
  const label = count ? `Inbox, ${count} need you` : 'Inbox'
  return (
    <>
      <button
        type="button"
        className={cx('sh-status-button', count > 0 && 'has-unread')}
        onClick={() => setOpen(true)}
        title={label}
        aria-label={label}
      >
        <Bell size={15} aria-hidden="true" />
        {!collapsed && <span>Inbox</span>}
        {count > 0 && <span className="sh-count is-danger">{count > 99 ? '99+' : count}</span>}
      </button>
      {open && (
        <InboxDrawer
          onClose={() => setOpen(false)}
          incidents={incidents}
          currentUserId={currentUserId}
          needsYou={needsYou}
        />
      )}
    </>
  )
}
