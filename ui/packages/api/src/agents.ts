import { ApiError, request } from './client'

/*
 * Agent identities (ADR-046) and the triggers that start their work
 * (ADR-047): an account for an AI agent, with an owner who answers for
 * it, a role, tokens of its own, and triggers that tell it about runs.
 */

const enc = encodeURIComponent

export interface Agent {
  id: string
  name: string
  description?: string
  role: string
  owner_id: string
  owner_name?: string
  disabled: boolean
  created_at: string
  last_used_at?: string
  /** One of the agent avatars: its owner's choice, or one drawn from its id. */
  avatar?: string
}

export interface AgentToken {
  id: string
  agent_id: string
  name: string
  prefix: string
  on_behalf_of_owner: boolean
  created_at: string
  expires_at: string
  last_used_at?: string
  revoked_at?: string
}

export interface AgentTrigger {
  id: string
  agent_id: string
  workspace_id: string
  /** Empty: every pipeline in the workspace the agent may see. */
  pipeline_id?: string
  events: string[]
  delivery: 'webhook' | 'feed'
  url: string
  cooldown_seconds: number
  enabled: boolean
  created_by: string
  created_at: string
  updated_at: string
  secret_rotated_at?: string
}

export interface TriggerInput {
  delivery?: 'webhook' | 'feed'
  pipeline_id?: string
  events?: string[]
  url?: string
  cooldown_seconds?: number
  enabled?: boolean
}

export interface TriggerEventType {
  name: string
  description: string
  suggested_tool?: string
}

export type DeliveryStatus = 'pending' | 'delivered' | 'failed' | 'skipped' | 'expired'

export interface DeliveryAttempt {
  at: string
  status_code?: number
  duration_ms: number
  error?: string
  response?: string
}

export interface TriggerDelivery {
  id: string
  trigger_id: string
  agent_id: string
  pipeline_id: string
  run_id: string
  event: string
  status: DeliveryStatus
  attempts: number
  last_status?: number
  last_error?: string
  reason?: string
  created_at: string
  next_attempt_at?: string
  finished_at?: string
  attempts_log?: DeliveryAttempt[]
}

/** An agent told about a run (GET /runs/{id}/agents). */
export interface RunNotice {
  delivery_id: string
  trigger_id: string
  agent_id: string
  agent_name: string
  agent_avatar?: string
  event: string
  status: DeliveryStatus
  reason?: string
  created_at: string
  finished_at?: string
  proposal?: { id: string; title: string; status: string }
}

/** A trigger seen from a pipeline it watches (GET /pipelines/{id}/triggers). */
export interface PipelineTrigger {
  id: string
  agent_id: string
  agent_name: string
  agent_avatar?: string
  events: string[]
  delivery: 'webhook' | 'feed'
  scope: 'pipeline' | 'workspace'
  enabled: boolean
  cooldown_seconds: number
  url?: string
  may_manage: boolean
}

/** Null when the server does not offer agent identities. */
function unlessMissing<T>(p: Promise<T>): Promise<T | null> {
  return p.catch((e: unknown) => {
    if (e instanceof ApiError && e.status === 404) return null
    throw e
  })
}

export const agentsApi = {
  /** Null when agent identities are not available on this server. */
  list: () => unlessMissing(request<Agent[] | null>('/agents').then((d) => d ?? [])),
  get: (id: string) => request<Agent>(`/agents/${enc(id)}`),
  create: (body: { name: string; role: string; description: string; owner_id?: string }) =>
    request<Agent>('/agents', { json: body }),
  /** avatar may be set by the agent's owner; "" goes back to the default. */
  update: (id: string, body: { role?: string; description?: string; avatar?: string }) =>
    request<Agent>(`/agents/${enc(id)}`, { method: 'PATCH', json: body }),
  disable: (id: string) => request<void>(`/agents/${enc(id)}`, { method: 'DELETE' }),
  tokens: (id: string) =>
    request<AgentToken[] | null>(`/agents/${enc(id)}/tokens`).then((d) => d ?? []),
  createToken: (
    id: string,
    body: { name: string; expires_in_days: number; on_behalf_of_owner: boolean },
  ) => request<{ token: string; record: AgentToken }>(`/agents/${enc(id)}/tokens`, { json: body }),
  revokeToken: (id: string, tokenId: string) =>
    request<void>(`/agents/${enc(id)}/tokens/${enc(tokenId)}`, { method: 'DELETE' }),
}

export const triggersApi = {
  /** Null when triggers are not available on this server. */
  eventTypes: () =>
    unlessMissing(
      request<TriggerEventType[] | null>('/agents/trigger-events').then((d) => d ?? []),
    ),
  list: (agentId: string) =>
    request<AgentTrigger[] | null>(`/agents/${enc(agentId)}/triggers`).then((d) => d ?? []),
  /** The signing secret is in this answer only, and only for a webhook. */
  create: (agentId: string, body: TriggerInput) =>
    request<{ trigger: AgentTrigger; secret?: string; note?: string }>(
      `/agents/${enc(agentId)}/triggers`,
      { json: body },
    ),
  update: (agentId: string, id: string, body: TriggerInput) =>
    request<AgentTrigger>(`/agents/${enc(agentId)}/triggers/${enc(id)}`, {
      method: 'PATCH',
      json: body,
    }),
  remove: (agentId: string, id: string) =>
    request<void>(`/agents/${enc(agentId)}/triggers/${enc(id)}`, { method: 'DELETE' }),
  rotateSecret: (agentId: string, id: string) =>
    request<{ secret: string; note?: string }>(
      `/agents/${enc(agentId)}/triggers/${enc(id)}/secret`,
      { method: 'POST' },
    ),
  deliveries: (agentId: string, id: string) =>
    request<TriggerDelivery[] | null>(
      `/agents/${enc(agentId)}/triggers/${enc(id)}/deliveries`,
    ).then((d) => d ?? []),
  redeliver: (agentId: string, id: string, deliveryId: string) =>
    request<{ status: string }>(
      `/agents/${enc(agentId)}/triggers/${enc(id)}/deliveries/${enc(deliveryId)}/redeliver`,
      { method: 'POST' },
    ),
  test: (agentId: string, id: string) =>
    request<{ delivery_id: string; pipeline_id: string; note?: string }>(
      `/agents/${enc(agentId)}/triggers/${enc(id)}/test`,
      { method: 'POST' },
    ),
  /** The agents told about a run; null when triggers are not available. */
  runNotices: (runId: string) =>
    unlessMissing(request<RunNotice[] | null>(`/runs/${enc(runId)}/agents`).then((d) => d ?? [])),
  /** The triggers that watch a pipeline; null when triggers are not available. */
  pipelineTriggers: (pipelineId: string) =>
    unlessMissing(
      request<PipelineTrigger[] | null>(`/pipelines/${enc(pipelineId)}/triggers`).then(
        (d) => d ?? [],
      ),
    ),
}

/* Messages from agents (ADR-048). */

export type MessageLevel = 'info' | 'attention' | 'urgent'

export interface MessageLink {
  kind: 'pipeline' | 'run' | 'change_request'
  id: string
  pipeline_id?: string
  label?: string
}

export interface AgentMessage {
  id: string
  conversation_id: string
  from: 'agent' | 'person'
  level: MessageLevel
  topic?: string
  text: string
  links: MessageLink[]
  muted?: boolean
  created_at: string
  read_at?: string
}

/** A message as the inbox shows it: from which agent, at which level. */
export interface InboxMessage extends AgentMessage {
  agent_id: string
  agent_name: string
  agent_avatar: string
  /** info when the conversation is muted. */
  shown_level: MessageLevel
}

export interface Conversation {
  id: string
  workspace_id: string
  agent_id: string
  person_id: string
  muted: boolean
  created_at: string
  last_message_at: string
}

export const messagesApi = {
  /** The caller's messages from agents; null when the server has no messages. */
  inbox: (limit = 50) =>
    unlessMissing(
      request<{ messages: InboxMessage[] | null; needs_you: number }>('/messages', {
        query: { limit },
      }).then((d) => ({
        messages: d?.messages ?? [],
        needsYou: d?.needs_you ?? 0,
      })),
    ),
  conversation: (id: string) =>
    request<{
      conversation: Conversation
      agent: { id: string; name?: string; avatar?: string; owner_name?: string; disabled?: boolean }
      messages: AgentMessage[] | null
    }>(`/messages/conversations/${enc(id)}`).then((d) => ({ ...d, messages: d.messages ?? [] })),
  /** Marks one message read, or every message when id is omitted. */
  read: (id?: string) => request<void>('/messages/read', { json: id ? { message_id: id } : {} }),
  mute: (conversationId: string, muted: boolean) =>
    request<Conversation>(`/messages/conversations/${enc(conversationId)}/mute`, {
      json: { muted },
    }),
  /** For an agent's owner: what it sent in the last week, and how much was muted. */
  summary: (agentId: string) =>
    request<{ days: number; sent: number; muted: number }>(
      `/agents/${enc(agentId)}/messages/summary`,
    ),
}
