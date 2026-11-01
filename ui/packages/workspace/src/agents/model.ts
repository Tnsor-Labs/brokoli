import type { AgentToken, AgentTrigger, DeliveryStatus, TriggerDelivery } from '@brokoli/api'
import type { Tone } from '@brokoli/ui'

/** Roles an agent can be given. Never admin. */
export const AGENT_ROLES = [
  {
    value: 'agent',
    label: 'Agent',
    help: 'Sees pipelines, runs and lineage, runs pipelines, and proposes changes a person applies',
  },
  { value: 'viewer', label: 'Viewer', help: 'Reads, and nothing more' },
  { value: 'operator', label: 'Operator', help: 'Runs and monitors pipelines' },
  {
    value: 'editor',
    label: 'Editor',
    help: 'Edits pipelines too: use only when a person reviews what it does',
  },
]

export function roleLabel(role: string): string {
  return AGENT_ROLES.find((r) => r.value === role)?.label ?? role
}

/** An agent name the server accepts. */
export const AGENT_NAME = /^[a-z][a-z0-9-]{1,47}$/

/** Where a token stands now. */
export function tokenState(t: AgentToken, now = Date.now()): 'active' | 'expired' | 'revoked' {
  if (t.revoked_at) return 'revoked'
  if (Date.parse(t.expires_at) <= now) return 'expired'
  return 'active'
}

/** How to connect an MCP client with a new token. */
export function connectCommand(origin: string, workspaceId: string, token: string): string {
  return `claude mcp add --transport http brokoli ${origin}/api/mcp --header "Authorization: Bearer ${token}"${
    workspaceId ? ` --header "X-Workspace-ID: ${workspaceId}"` : ''
  }`
}

/** The cooldowns offered, in seconds; the server takes 0 to 7 days. */
export const COOLDOWNS = [0, 300, 900, 1800, 3600, 6 * 3600, 24 * 3600]

/** A cooldown in words: "none", "30 minutes", "1 hour", "90 seconds". */
export function cooldownLabel(seconds: number): string {
  if (seconds <= 0) return 'none'
  const unit = (n: number, word: string) => `${n} ${word}${n === 1 ? '' : 's'}`
  if (seconds % 86400 === 0) return unit(seconds / 86400, 'day')
  if (seconds % 3600 === 0) return unit(seconds / 3600, 'hour')
  if (seconds % 60 === 0) return unit(seconds / 60, 'minute')
  return unit(seconds, 'second')
}

/** Where a trigger's events go, in words. */
export function deliveryTarget(t: Pick<AgentTrigger, 'delivery' | 'url'>): string {
  return t.delivery === 'feed' ? 'Its feed, which the agent reads' : t.url
}

const STATUS: Record<DeliveryStatus, { label: string; tone: Tone }> = {
  delivered: { label: 'Delivered', tone: 'success' },
  pending: { label: 'Pending', tone: 'queued' },
  failed: { label: 'Failed', tone: 'danger' },
  skipped: { label: 'Skipped', tone: 'neutral' },
  expired: { label: 'Expired', tone: 'warning' },
}

/**
 * A delivery's state in words. A feed delivery that is pending is waiting
 * for its agent to read it, not for a retry.
 */
export function deliveryStatus(
  d: Pick<TriggerDelivery, 'status'>,
  delivery: AgentTrigger['delivery'],
): { label: string; tone: Tone } {
  if (d.status === 'pending' && delivery === 'feed')
    return { label: 'Waiting to be read', tone: 'queued' }
  if (d.status === 'delivered' && delivery === 'feed') return { label: 'Read', tone: 'success' }
  return STATUS[d.status] ?? { label: d.status, tone: 'neutral' }
}

/** A test event's run id starts so; no run happened. */
export function isTestDelivery(d: Pick<TriggerDelivery, 'run_id'>): boolean {
  return d.run_id.startsWith('test-')
}

/** The run a delivery is about, or null for a test, a message, or an added event with no run. */
export function deliveryRun(d: Pick<TriggerDelivery, 'run_id'>): string | null {
  if (isTestDelivery(d) || d.run_id.startsWith('raised:') || d.run_id.startsWith('message:'))
    return null
  return d.run_id
}

/** Whether a delivery can be sent again: a webhook's, once it is finished. */
export function canRedeliver(
  d: Pick<TriggerDelivery, 'status'>,
  delivery: AgentTrigger['delivery'],
): boolean {
  return delivery === 'webhook' && d.status !== 'pending'
}

/** What the last attempt answered, for the log. */
export function lastOutcome(
  d: Pick<TriggerDelivery, 'last_status' | 'last_error' | 'reason'>,
): string {
  if (d.reason) return d.reason
  if (d.last_error) return d.last_error
  if (d.last_status) return `HTTP ${d.last_status}`
  return ''
}

/** Why a trigger form cannot be saved yet, or ''. */
export function triggerProblem(input: {
  delivery: 'webhook' | 'feed'
  url: string
  events: string[]
}): string {
  if (!input.events.length) return 'Choose at least one event.'
  if (input.delivery === 'webhook') {
    const url = input.url.trim()
    if (!url) return 'Give the URL deliveries are posted to.'
    try {
      const u = new URL(url)
      if (u.protocol !== 'https:' && u.protocol !== 'http:')
        return 'The URL must start with https://.'
    } catch {
      return 'That is not a URL.'
    }
  }
  return ''
}
