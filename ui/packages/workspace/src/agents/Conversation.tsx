import { useEffect, useRef, useState, type FormEvent, type KeyboardEvent } from 'react'
import { Link } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { messagesApi, type AgentMessage, type MessageLink } from '@brokoli/api'
import {
  AgentAvatar,
  Badge,
  Button,
  Skeleton,
  Switch,
  Textarea,
  cx,
  errorMessage,
  formatDateTime,
  useToast,
  type Tone,
} from '@brokoli/ui'
import '../workspace.css'
import './agents.css'

/*
 * A conversation between a person and an agent (ADR-048): the thread,
 * oldest first, and a box to answer in. The agent hears an answer through
 * its triggers for message.received, or reads it when it next checks.
 * Exported for the inbox and a distribution's own pages.
 */

const levelTone = (level: string): Tone =>
  level === 'urgent' ? 'danger' : level === 'attention' ? 'warning' : 'neutral'
const LEVEL: Record<string, string> = { attention: 'Needs attention', urgent: 'Urgent' }

export const conversationKey = (id: string) => ['messages', 'conversation', id] as const

/** Where a message's link goes in the app. */
function linkPath(l: MessageLink): string | null {
  const pid = l.pipeline_id ?? (l.kind === 'pipeline' ? l.id : '')
  if (!pid) return null
  const p = encodeURIComponent(pid)
  if (l.kind === 'pipeline') return `/pipelines/${p}/runs`
  if (l.kind === 'run') return `/pipelines/${p}/runs?run=${encodeURIComponent(l.id)}`
  if (l.kind === 'change_request') return `/pipelines/${p}/changes/${encodeURIComponent(l.id)}`
  return null
}

function linkLabel(l: MessageLink): string {
  if (l.label) return l.label
  if (l.kind === 'run') return `Run ${l.id.slice(0, 8)}`
  return l.kind === 'change_request' ? 'Change request' : l.id
}

export function Conversation({
  conversationId,
  onChanged,
  onNavigate,
  mute = true,
}: {
  conversationId: string
  /** Called after a reply or a mute, so the page can refresh what it counts. */
  onChanged?: () => unknown
  /** Called when a link is followed, so a drawer can close. */
  onNavigate?: () => void
  /** Offer the switch to mute the agent. */
  mute?: boolean
}) {
  const toast = useToast()
  const queryClient = useQueryClient()
  const key = conversationKey(conversationId)
  const query = useQuery({ queryKey: key, queryFn: () => messagesApi.conversation(conversationId) })
  const [text, setText] = useState('')
  const [busy, setBusy] = useState(false)
  const [hint, setHint] = useState('')
  // The thread scrolls in its own box; keep the newest message in view.
  const threadRef = useRef<HTMLOListElement>(null)
  const count = query.data?.messages.length ?? 0
  useEffect(() => {
    const el = threadRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [count])

  if (query.isPending) return <Skeleton height={64} />
  if (query.isError) return <p className="ag-convo-error">{errorMessage(query.error)}</p>
  const { conversation, agent, messages } = query.data
  const name = agent.name ?? 'The agent'

  const send = async (e?: FormEvent) => {
    e?.preventDefault()
    if (!text.trim() || busy) return
    setBusy(true)
    try {
      const res = await messagesApi.reply(conversationId, text)
      setText('')
      setHint(res.note ?? '')
      if (res.redacted)
        toast.success(
          'Sent',
          'Something that looked like a credential was replaced before it was stored.',
        )
      await queryClient.invalidateQueries({ queryKey: key })
      await onChanged?.()
    } catch (err) {
      toast.error('Not sent', err)
    }
    setBusy(false)
  }
  const onKey = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) void send()
  }
  const setMuted = async (muted: boolean) => {
    try {
      await messagesApi.mute(conversationId, muted)
      await queryClient.invalidateQueries({ queryKey: key })
      await onChanged?.()
      toast.success(
        muted ? `${name} is muted` : `${name} is unmuted`,
        muted ? 'Its messages still arrive, quietly, and do not count on the bell.' : undefined,
      )
    } catch (err) {
      toast.error('Could not change it', err)
    }
  }

  return (
    <div className="ag-convo">
      {messages.length === 0 ? (
        <p className="ws-muted">No messages yet. Write the first one below.</p>
      ) : (
        <ol className="ag-convo-thread" ref={threadRef}>
          {messages.map((m: AgentMessage) => (
            <li
              key={m.id}
              className={cx('ag-convo-message', m.from === 'person' ? 'is-person' : 'is-agent')}
            >
              {m.from === 'agent' && <AgentAvatar avatar={agent.avatar} size={24} />}
              <div className="ag-convo-bubble">
                <div className="ag-convo-meta">
                  <strong className={m.from === 'agent' ? 'bk-mono' : undefined}>
                    {m.from === 'agent' ? name : 'You'}
                  </strong>
                  {m.from === 'agent' && LEVEL[m.level] && (
                    <Badge tone={levelTone(m.muted ? 'info' : m.level)}>
                      {m.muted ? 'Muted' : LEVEL[m.level]}
                    </Badge>
                  )}
                  <span className="ws-muted">{formatDateTime(m.created_at)}</span>
                </div>
                {m.topic && <strong className="ag-convo-topic">{m.topic}</strong>}
                <p className="ag-convo-text">{m.text}</p>
                {m.links.length > 0 && (
                  <div className="ag-convo-links">
                    {m.links.map((l) => {
                      const to = linkPath(l)
                      return to ? (
                        <Link key={l.kind + l.id} to={to} onClick={onNavigate}>
                          {linkLabel(l)}
                        </Link>
                      ) : (
                        <span key={l.kind + l.id}>{linkLabel(l)}</span>
                      )
                    })}
                  </div>
                )}
              </div>
            </li>
          ))}
        </ol>
      )}
      {agent.disabled ? (
        <p className="ws-muted">{name} is disabled; nothing you write reaches it.</p>
      ) : (
        <form className="ag-convo-reply" onSubmit={send}>
          <Textarea
            id={`reply-${conversationId}`}
            aria-label={`Write to ${name}`}
            rows={2}
            maxLength={8192}
            placeholder={`Write to ${name}`}
            value={text}
            onChange={(e) => setText(e.target.value)}
            onKeyDown={onKey}
          />
          <div className="ag-convo-actions">
            <span className="ws-muted">
              {hint ||
                `${name} reads this when it is told, or when it next checks. Ctrl+Enter sends.`}
            </span>
            <Button
              type="submit"
              size="sm"
              variant="primary"
              loading={busy}
              disabled={!text.trim()}
            >
              Send
            </Button>
          </div>
        </form>
      )}
      {mute && (
        <div className="ag-convo-foot">
          <span className="ws-muted">
            From an agent{agent.owner_name ? `, owned by ${agent.owner_name}` : ''}.
          </span>
          <Switch
            checked={conversation.muted}
            label={conversation.muted ? `Unmute ${name}` : `Mute ${name}`}
            onChange={(next) => void setMuted(next)}
          />
          <span className="ws-muted">{conversation.muted ? 'Muted' : 'Mute'}</span>
        </div>
      )}
    </div>
  )
}
