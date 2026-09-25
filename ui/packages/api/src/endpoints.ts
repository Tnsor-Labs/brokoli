import {
  ApiError,
  adoptWorkspaceOwner,
  clearSession,
  request,
  setToken,
  setWorkspace,
  storedWorkspace,
  type RequestOptions,
} from './client'
import type {
  AuthClaims,
  AuthMethods,
  AuthUser,
  AlertList,
  BackfillPlan,
  CalendarDay,
  DashboardStats,
  DeadLetter,
  DependencyGraph,
  DependencyStatus,
  LineageGraph,
  NodeStats,
  Connection,
  ConnectionTestResult,
  ConnectionTypeMeta,
  ConnectionUsage,
  DryRunResponse,
  LogEntry,
  NodeIssue,
  NodePreview,
  NodeProfile,
  PhysicalInstance,
  PhysicalPlan,
  Pipeline,
  PipelineCreate,
  PipelineGrid,
  PipelineSummary,
  PipelineTemplate,
  PipelineVersion,
  Plugin,
  PluginIndex,
  PluginInstallResult,
  Run,
  RunEvent,
  RunPage,
  RunTriggerResult,
  SchedulePreview,
  NotificationSettings,
  SchedulerEntry,
  SystemInfo,
  User,
  Variable,
  Workspace,
} from './types'

const enc = encodeURIComponent

/** Accepts both the bare-array and the {items} envelope shapes some list endpoints return. */
function items<T>(data: T[] | { items?: T[] | null } | null | undefined): T[] {
  if (Array.isArray(data)) return data
  if (data && Array.isArray(data.items)) return data.items
  if (data === null || data === undefined) return []
  throw new ApiError('Unexpected response shape from the server', 200, data)
}

export function claimsToUser(c: AuthClaims): AuthUser {
  return {
    id: c.sub,
    username: c.username,
    display_name: c.display_name,
    email: c.email,
    role: c.role,
    org_id: c.org_id,
  }
}

/*
 * Workspace selection. Core has no /api/workspaces route, so a 404 there is
 * the normal OSS answer and simply means "no header". Any other failure is
 * reported rather than treated as OSS, because in an enterprise deployment
 * it means requests will land in the wrong workspace.
 */
export async function resolveWorkspace(userId: string, hintIsFresh = false): Promise<Workspace[]> {
  adoptWorkspaceOwner(userId, hintIsFresh)
  let list: Workspace[]
  try {
    list = await request<Workspace[]>('/workspaces', { retries: 0 })
  } catch (e) {
    if (e instanceof ApiError && (e.status === 404 || e.status === 405)) return []
    throw e
  }
  if (!Array.isArray(list) || !list.length) return []
  const stored = storedWorkspace()
  const selected = list.find((w) => w.id === stored) ?? list.find((w) => w.id === 'default') ?? list[0]
  setWorkspace(selected.id)
  return list
}

export const authApi = {
  setup: () => request<{ needs_setup: boolean }>('/auth/setup', { retries: 1, allowUnauthorized: true }),
  methods: () => request<AuthMethods>('/auth/methods', { retries: 1, allowUnauthorized: true }),
  /** Identify the current session. Cookie-only unless a bearer is explicitly in play. */
  me: (opts: Pick<RequestOptions, 'cookieOnly' | 'headers'> = {}) =>
    request<AuthClaims>('/auth/me', { retries: 0, allowUnauthorized: true, ...opts }).then(claimsToUser),
  /** Copies the bearer into the httpOnly session cookie (the websocket needs the cookie). */
  session: () => request<void>('/auth/session', { method: 'POST', allowUnauthorized: true }),
  login: async (username: string, password: string) => {
    const data = await request<{ token: string }>('/auth/login', {
      json: { username, password },
      allowUnauthorized: true,
    })
    setToken(data.token)
    return authApi.me()
  },
  createFirstUser: async (username: string, password: string) => {
    await request('/auth/users', { json: { username, password, role: 'admin' }, allowUnauthorized: true })
    return authApi.login(username, password)
  },
  logout: async () => {
    clearSession()
    await request('/auth/logout', { method: 'POST', allowUnauthorized: true }).catch(() => {
      /* The cookie is httpOnly; if the server is unreachable it expires on its own. */
    })
  },
  permissions: async () => {
    const data = await request<string[] | { permissions?: string[] }>('/auth/me/permissions')
    if (Array.isArray(data)) return data
    if (data && Array.isArray(data.permissions)) return data.permissions
    throw new ApiError('Unexpected permissions response', 200, data)
  },
}

export type DeleteResolve = 'abort' | 'cascade' | 'decouple'

export const pipelineApi = {
  summary: () => request<PipelineSummary[] | null>('/pipelines/summary').then((d) => d ?? []),
  list: () => request<Pipeline[] | { items: Pipeline[] }>('/pipelines').then(items),
  get: (id: string) => request<Pipeline>(`/pipelines/${enc(id)}`),
  create: (body: PipelineCreate) => request<Pipeline>('/pipelines', { json: body }),
  /** The body must be the object the server returned, with edits applied. */
  update: (id: string, body: Pipeline) =>
    request<Pipeline>(`/pipelines/${enc(id)}`, { method: 'PUT', json: body }),
  remove: (id: string, resolve?: DeleteResolve) =>
    request<void>(`/pipelines/${enc(id)}`, { method: 'DELETE', query: { resolve } }),
  clone: (id: string) => request<Pipeline>(`/pipelines/${enc(id)}/clone`, { method: 'POST' }),
  exportYaml: async (id: string) => {
    const res = await request<Response>(`/pipelines/${enc(id)}/export`, { as: 'response' })
    const disposition = res.headers.get('Content-Disposition') ?? ''
    const filename = /filename="?([^";]+)"?/.exec(disposition)?.[1] ?? `${id}.yaml`
    return { filename, text: await res.text() }
  },
  importDocument: (text: string, filename: string) =>
    request<Pipeline>('/pipelines/import', {
      method: 'POST',
      body: text,
      contentType: filename.toLowerCase().endsWith('.json') ? 'application/json' : 'application/x-yaml',
    }),
  run: (id: string, params?: Record<string, string>) =>
    request<RunTriggerResult>(`/pipelines/${enc(id)}/run`, { json: params ? { params } : {}, timeout: 0 }),
  versions: (id: string) => request<PipelineVersion[] | null>(`/pipelines/${enc(id)}/versions`).then((d) => d ?? []),
  rollback: (id: string, version: number) =>
    request<Pipeline>(`/pipelines/${enc(id)}/rollback`, { json: { version } }),
  validateNodes: (id: string) =>
    request<{ issues: NodeIssue[] | null }>(`/pipelines/${enc(id)}/validate-nodes`, { method: 'POST' }).then(
      (d) => d.issues ?? [],
    ),
  dryRun: (id: string) => request<DryRunResponse>(`/pipelines/${enc(id)}/dry-run`, { method: 'POST', timeout: 0 }),
  plan: (id: string) => request<PhysicalPlan>(`/pipelines/${enc(id)}/plan`),
  grid: (id: string, runs = 30) => request<PipelineGrid>(`/pipelines/${enc(id)}/grid`, { query: { runs } }),
  runs: (id: string) => request<Run[] | null>(`/pipelines/${enc(id)}/runs`).then((d) => d ?? []),
  runsPage: (id: string, after?: string, limit = 50) =>
    request<RunPage>(`/pipelines/${enc(id)}/runs`, { query: { after, limit } }),
  backfill: (id: string, startDate: string, endDate: string, force = false) =>
    request<BackfillPlan>(`/pipelines/${enc(id)}/backfill`, {
      json: { start_date: startDate, end_date: endDate, force },
      timeout: 0,
    }),
}

export const templateApi = {
  list: () => request<PipelineTemplate[] | null>('/templates').then((d) => d ?? []),
}

export const schedulerApi = {
  status: () => request<SchedulerEntry[] | null>('/scheduler/status').then((d) => d ?? []),
  preview: (input: string, timezone: string, signal?: AbortSignal) =>
    request<SchedulePreview>('/schedule/preview', { json: { input, timezone, count: 3 }, signal }),
}

export const runApi = {
  get: (id: string) => request<Run>(`/runs/${enc(id)}`),
  logs: (id: string) => request<LogEntry[] | null>(`/runs/${enc(id)}/logs`).then((d) => d ?? []),
  events: (id: string) => request<RunEvent[] | null>(`/runs/${enc(id)}/events`).then((d) => d ?? []),
  instances: (id: string) => request<PhysicalInstance[] | null>(`/runs/${enc(id)}/instances`).then((d) => d ?? []),
  cancel: (id: string) => request<{ status: string }>(`/runs/${enc(id)}/cancel`, { method: 'POST' }),
  // With no fromNode this is the plain resume (a failed run, from its first
  // failed node). With one, that node and everything downstream of it re-run as
  // a new appended run; the rest of the earlier run's work is reused.
  resume: (id: string, fromNode?: string) => request<Run>(`/runs/${enc(id)}/resume`, fromNode ? { method: 'POST', json: { from_node: fromNode } } : { method: 'POST' }),
  preview: (runId: string, nodeId: string) => request<NodePreview>(`/runs/${enc(runId)}/nodes/${enc(nodeId)}/preview`),
  profile: (runId: string, nodeId: string) => request<NodeProfile>(`/runs/${enc(runId)}/nodes/${enc(nodeId)}/profile`),
  exportLogs: async (id: string) => {
    const res = await request<Response>(`/runs/${enc(id)}/logs/export`, { as: 'response' })
    const disposition = res.headers.get('Content-Disposition') ?? ''
    const filename = /filename="?([^";]+)"?/.exec(disposition)?.[1] ?? `run-${id.slice(0, 8)}-logs.txt`
    return { filename, text: await res.text() }
  },
}

export const connectionApi = {
  // The unpaged list masks credential refs; the paged form (?page=) does not, so it is deliberately not used.
  list: () => request<Connection[] | { items: Connection[] }>('/connections').then(items),
  get: (connId: string) => request<Connection>(`/connections/${enc(connId)}`),
  create: (body: Partial<Connection>) => request<Connection>('/connections', { json: body }),
  update: (connId: string, body: Partial<Connection>) => request<Connection>(`/connections/${enc(connId)}`, { method: 'PUT', json: body }),
  remove: (connId: string) => request<void>(`/connections/${enc(connId)}`, { method: 'DELETE' }),
  types: () => request<ConnectionTypeMeta[] | null>('/connection-types').then((d) => (Array.isArray(d) ? d : [])),
  usedBy: (connId: string) => request<ConnectionUsage[] | null>(`/connections/${enc(connId)}/used-by`).then((d) => d ?? []),
  test: (connId: string) => request<ConnectionTestResult>(`/connections/${enc(connId)}/test`, { method: 'POST', timeout: 60_000 }),
  testUri: (uri: string) => request<ConnectionTestResult>('/test-connection', { json: { uri }, timeout: 60_000 }),
}

export const variableApi = {
  list: () => request<Variable[] | { items: Variable[] }>('/variables').then(items),
  /** POST is an upsert on the server: it overwrites an existing key. Callers check for a clash first. */
  create: (body: Pick<Variable, 'key' | 'type' | 'value' | 'description'>) => request<Variable>('/variables', { json: body }),
  update: (body: Pick<Variable, 'key' | 'type' | 'value' | 'description'>) =>
    request<Variable>(`/variables/${enc(body.key)}`, { method: 'PUT', json: body }),
  remove: (key: string) => request<void>(`/variables/${enc(key)}`, { method: 'DELETE' }),
  usedBy: (key: string) => request<ConnectionUsage[] | null>(`/variables/${enc(key)}/used-by`).then((d) => d ?? []),
}

function installResult(data: Plugin | { plugin: Plugin; warning?: string }): PluginInstallResult {
  return 'plugin' in data ? { plugin: data.plugin, warning: data.warning } : { plugin: data }
}

export const pluginApi = {
  list: () => request<Plugin[] | null>('/plugins').then((d) => d ?? []),
  /** Fetched by the server from the configured index; 502 when it cannot reach or parse it. */
  index: () => request<PluginIndex>('/plugins/index', { retries: 0, timeout: 25_000 }),
  install: (file: File) =>
    request<Plugin | { plugin: Plugin; warning?: string }>('/plugins', { method: 'POST', body: file, timeout: 0 }).then(installResult),
  /** The server allows 90 seconds for download and verification; no client timeout, and POSTs are never retried. */
  installByName: (name: string) =>
    request<Plugin | { plugin: Plugin; warning?: string }>(`/plugins/index/${enc(name)}`, { method: 'POST', timeout: 0 }).then(installResult),
  remove: (name: string) => request<void>(`/plugins/${enc(name)}`, { method: 'DELETE' }),
}

export const systemApi = {
  info: () => request<SystemInfo>('/system/info'),
  /** Deletes runs older than `days`; on a single-organisation server that is every run on the server. */
  purge: (days: number) => request<{ deleted: number; days: number; org_id?: string }>('/system/purge', { json: { days }, timeout: 0 }),
}

export const userApi = {
  list: () => request<User[] | null>('/auth/users').then((d) => d ?? []),
  create: (username: string, password: string, role: string) => request<User>('/auth/users', { json: { username, password, role } }),
  resetPassword: (userId: string, newPassword: string) =>
    request<{ status: string }>('/auth/admin-reset-password', { json: { user_id: userId, new_password: newPassword } }),
  changePassword: (currentPassword: string, newPassword: string) =>
    request<{ status: string }>('/auth/change-password', { json: { current_password: currentPassword, new_password: newPassword } }),
  /** Edit your own display name and email (core #605). Fields omitted are left unchanged; the change reaches the session on your next sign-in. */
  updateProfile: (body: { display_name?: string; email?: string }) => request<User>('/auth/me/profile', { method: 'PUT', json: body }),
}

/*
 * Notification settings. The server's PUT always writes `channel` and
 * `username` (blank when absent) and writes the two webhooks only when
 * non-empty, so every save must send the current channel and username.
 * DELETE clears Slack and Teams together; the API cannot clear only one.
 */
export const notificationApi = {
  get: () => request<NotificationSettings>('/settings/notifications'),
  update: (body: { webhook?: string; channel: string; username: string; teams_webhook?: string }) =>
    request<{ status: string }>('/settings/notifications', { method: 'PUT', json: body }),
  test: () => request<{ status: string }>('/settings/notifications/test', { method: 'POST', timeout: 20_000 }),
  clearAll: () => request<{ status: string }>('/settings/notifications', { method: 'DELETE' }),
}

export const observeApi = {
  dashboard: () => request<DashboardStats>('/dashboard'),
  /** The server accepts 1 to 365 days and silently answers with 90 for anything else, so the value is clamped here. */
  calendar: (days: number) =>
    request<CalendarDay[] | null>('/runs/calendar', { query: { days: Math.min(365, Math.max(1, Math.round(days))) } }).then((d) => d ?? []),
  deadLetters: (limit: number) => request<DeadLetter[] | null>('/dlq', { query: { limit } }).then((d) => d ?? []),
  resolveDeadLetter: (pipelineId: string, id: string) =>
    request<{ status: string }>(`/pipelines/${enc(pipelineId)}/dlq/${enc(id)}/resolve`, { method: 'POST' }),
  alerts: (limit: number) =>
    request<AlertList>('/alerts', { query: { limit } }).then((d) => ({ alerts: d?.alerts ?? [], unread: d?.unread_count ?? 0 })),
  readAlert: (id: string) => request<void>(`/alerts/${enc(id)}/read`, { method: 'POST' }),
  readAllAlerts: () => request<void>('/alerts/read-all', { method: 'POST' }),
  dismissAlert: (id: string) => request<void>(`/alerts/${enc(id)}`, { method: 'DELETE' }),
  /* Incident ownership on an alert (brokoli-ee#242). Assigning null clears the owner. */
  assignAlert: (id: string, userId: string | null) => request<void>(`/alerts/${enc(id)}/assign`, { json: { user_id: userId } }),
  acknowledgeAlert: (id: string) => request<void>(`/alerts/${enc(id)}/acknowledge`, { method: 'POST' }),
  resolveAlert: (id: string) => request<void>(`/alerts/${enc(id)}/resolve`, { method: 'POST' }),
  lineage: () => request<LineageGraph>('/lineage'),
  dependencyGraph: () => request<DependencyGraph>('/pipelines/dependency-graph'),
  dependencyStatus: (pipelineId: string) => request<DependencyStatus>(`/pipelines/${enc(pipelineId)}/deps`),
  nodeStats: (pipelineId: string, runs = 10) => request<NodeStats>(`/pipelines/${enc(pipelineId)}/node-stats`, { query: { runs } }),
}

/** Save a text payload as a file (exports). */
export function downloadText(filename: string, text: string, type = 'text/plain') {
  const url = URL.createObjectURL(new Blob([text], { type }))
  const a = document.createElement('a')
  a.href = url
  a.download = filename.replace(/[/\\?%*:|"<>]/g, '-')
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}
