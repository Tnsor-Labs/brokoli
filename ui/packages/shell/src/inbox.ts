import type { InboxMessage, MessageLevel, MessageLink } from '@brokoli/api'
import type { Tone } from '@brokoli/ui'

/** The colour a message's level is shown in. */
export function levelTone(level: MessageLevel): Tone {
  return level === 'urgent' ? 'danger' : level === 'attention' ? 'warning' : 'neutral'
}

export const LEVEL_LABEL: Record<MessageLevel, string> = {
  info: 'Info',
  attention: 'Needs attention',
  urgent: 'Urgent',
}

/** Where a message's link goes in the app. Links are the server's, never the agent's URLs. */
export function linkPath(l: MessageLink): string | null {
  const pid = l.pipeline_id ?? (l.kind === 'pipeline' ? l.id : '')
  if (!pid) return null
  const p = encodeURIComponent(pid)
  switch (l.kind) {
    case 'pipeline':
      return `/pipelines/${p}/runs`
    case 'run':
      return `/pipelines/${p}/runs?run=${encodeURIComponent(l.id)}`
    case 'change_request':
      return `/pipelines/${p}/changes/${encodeURIComponent(l.id)}`
    default:
      return null
  }
}

/** What a link says: its label, or what it is. */
export function linkLabel(l: MessageLink): string {
  if (l.label) return l.label
  if (l.kind === 'run') return `Run ${l.id.slice(0, 8)}`
  if (l.kind === 'change_request') return 'Change request'
  return l.id
}

/** The bell's count: unread alerts, and messages that ask something of you. */
export function inboxCount(unreadAlerts: number, needsYou: number): number {
  return unreadAlerts + needsYou
}

/** The first line of a message, for the list. */
export function firstLine(text: string, max = 160): string {
  const line = text.split('\n', 1)[0]
  return line.length > max ? `${line.slice(0, max - 1)}…` : line
}

/** Unread messages first among those that need you, then newest first, as the server sends them. */
export function unreadNeedingYou(messages: InboxMessage[]): number {
  return messages.filter((m) => !m.read_at && !m.muted && m.level !== 'info').length
}
