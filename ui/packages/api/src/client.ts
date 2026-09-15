/*
 * Transport for the Brokoli HTTP API.
 *
 * Differences from the Svelte client, each fixing a defect it had:
 *
 * - Only idempotent reads (GET/HEAD) are ever retried, and only on a network
 *   failure or a 502/503/504. The Svelte client retried every 4xx three
 *   times and re-sent POSTs after a timeout, which could trigger a pipeline
 *   run twice.
 * - Every request goes through here, so every request carries the workspace
 *   header. Raw fetches that skipped it wrote into the wrong EE workspace.
 * - Non-JSON error bodies (the login rate limiter answers in plain text) are
 *   reported as what they say, not as "Connection error".
 * - Content-Type is only sent with a body.
 */

export const API_BASE = '/api'

const TOKEN_KEY = 'brokoli-token'
const WORKSPACE_KEY = 'brokoli-workspace'
const WORKSPACE_OWNER_KEY = 'brokoli-workspace-user'

export class ApiError extends Error {
  readonly status: number
  readonly body: unknown
  constructor(message: string, status: number, body: unknown) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.body = body
  }
}

/** True when the server was never reached (offline, DNS, CORS, abort). */
export function isNetworkError(error: unknown) {
  return error instanceof ApiError && error.status === 0
}

type Session = {
  /** Bearer for the rest of this page load after a password login. */
  token: string | null
  workspace: string | null
  onUnauthorized: () => void
}

const session: Session = {
  token: null,
  workspace: null,
  onUnauthorized: () => {},
}

function storage(): Storage | null {
  try {
    return window.localStorage
  } catch {
    return null
  }
}

export function setUnauthorizedHandler(handler: () => void) {
  session.onUnauthorized = handler
}

/** In-memory bearer. Pass persist=true only for SSO callback tokens. */
export function setToken(value: string | null, persist = false) {
  session.token = value
  const s = storage()
  if (!s) return
  if (value && persist) s.setItem(TOKEN_KEY, value)
  if (!value) s.removeItem(TOKEN_KEY)
}

export function storedToken(): string | null {
  return storage()?.getItem(TOKEN_KEY) ?? null
}

export function currentToken() {
  return session.token
}

export function authHeaders(): Record<string, string> {
  return session.token ? { Authorization: `Bearer ${session.token}` } : {}
}

export function workspaceHeaders(): Record<string, string> {
  return session.workspace ? { 'X-Workspace-ID': session.workspace } : {}
}

export function currentWorkspace() {
  return session.workspace
}

export function setWorkspace(id: string | null) {
  session.workspace = id
  const s = storage()
  if (!s) return
  if (id) s.setItem(WORKSPACE_KEY, id)
}

/** Forget the workspace a previous user selected, so user B never sends user A's header. */
export function adoptWorkspaceOwner(userId: string, hintIsFresh: boolean) {
  session.workspace = null
  const s = storage()
  if (!s) return
  const previous = s.getItem(WORKSPACE_OWNER_KEY)
  if (!hintIsFresh && previous && previous !== userId) s.removeItem(WORKSPACE_KEY)
  s.setItem(WORKSPACE_OWNER_KEY, userId)
}

/** An SSO callback may name the workspace to open; it is only a hint until resolveWorkspace confirms it. */
export function rememberWorkspaceHint(id: string) {
  storage()?.setItem(WORKSPACE_KEY, id)
}

export function storedWorkspace() {
  return storage()?.getItem(WORKSPACE_KEY) ?? null
}

export function clearSession() {
  session.token = null
  session.workspace = null
  storage()?.removeItem(TOKEN_KEY)
}

export type Query = Record<string, string | number | boolean | null | undefined>

export type RequestOptions = {
  method?: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE' | 'HEAD'
  /** Serialised as JSON. */
  json?: unknown
  /** Sent as-is (file imports). */
  body?: BodyInit
  contentType?: string
  query?: Query
  headers?: Record<string, string>
  /** Per-attempt timeout in ms. 0 disables it (long-running server work). */
  timeout?: number
  signal?: AbortSignal
  /** Extra attempts for idempotent reads. Ignored for writes. */
  retries?: number
  /** Skip the 401 handler (auth probes expect 401 as a normal answer). */
  allowUnauthorized?: boolean
  /** Send no bearer, cookie only. */
  cookieOnly?: boolean
  /** Response handling. */
  as?: 'json' | 'text' | 'response'
}

const RETRYABLE_STATUS = new Set([502, 503, 504])
const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms))

export function buildUrl(path: string, query?: Query) {
  const qs = query
    ? Object.entries(query)
        .filter(([, v]) => v !== undefined && v !== null && v !== '')
        .map(([k, v]) => `${encodeURIComponent(k)}=${encodeURIComponent(String(v))}`)
        .join('&')
    : ''
  return `${API_BASE}${path}${qs ? `${path.includes('?') ? '&' : '?'}${qs}` : ''}`
}

async function readError(response: Response): Promise<ApiError> {
  const text = await response.text().catch(() => '')
  let body: unknown = text
  let message = ''
  if (text) {
    try {
      body = JSON.parse(text)
      const b = body as { error?: unknown; message?: unknown }
      message = typeof b.error === 'string' ? b.error : typeof b.message === 'string' ? b.message : ''
    } catch {
      message = text.trim().slice(0, 300)
    }
  }
  if (response.status === 429 && (!message || message === 'rate limit exceeded'))
    message = 'Too many requests. Wait a moment and try again.'
  return new ApiError(message || `Request failed (HTTP ${response.status})`, response.status, body)
}

export async function request<T = unknown>(path: string, options: RequestOptions = {}): Promise<T> {
  const method = options.method ?? (options.json !== undefined || options.body !== undefined ? 'POST' : 'GET')
  const idempotent = method === 'GET' || method === 'HEAD'
  const maxRetries = idempotent ? (options.retries ?? 2) : 0
  const timeout = options.timeout ?? 30_000
  const url = buildUrl(path, options.query)

  for (let attempt = 0; ; attempt++) {
    const controller = new AbortController()
    const onAbort = () => controller.abort(options.signal?.reason)
    options.signal?.addEventListener('abort', onAbort, { once: true })
    const timer = timeout > 0 ? setTimeout(() => controller.abort(new DOMException('timeout', 'TimeoutError')), timeout) : null
    const headers: Record<string, string> = { ...workspaceHeaders(), ...(options.cookieOnly ? {} : authHeaders()), ...options.headers }
    let body: BodyInit | undefined
    if (options.json !== undefined) {
      body = JSON.stringify(options.json)
      headers['Content-Type'] = 'application/json'
    } else if (options.body !== undefined) {
      body = options.body
      if (options.contentType) headers['Content-Type'] = options.contentType
    }

    let response: Response
    try {
      response = await fetch(url, { method, headers, body, credentials: 'same-origin', signal: controller.signal })
    } catch (cause) {
      if (options.signal?.aborted) throw cause
      const timedOut = controller.signal.aborted
      if (attempt < maxRetries) {
        await sleep(500 * 3 ** attempt)
        continue
      }
      throw new ApiError(
        timedOut
          ? idempotent
            ? 'The server took too long to respond.'
            : 'The server took too long to respond. The action may still complete; refresh before retrying.'
          : 'Could not reach the Brokoli server.',
        0,
        null,
      )
    } finally {
      if (timer) clearTimeout(timer)
      options.signal?.removeEventListener('abort', onAbort)
    }

    if (response.status === 401 && !options.allowUnauthorized && !path.startsWith('/auth/')) {
      session.onUnauthorized()
      throw new ApiError('Your session has expired. Sign in again.', 401, null)
    }
    if (!response.ok) {
      if (RETRYABLE_STATUS.has(response.status) && attempt < maxRetries) {
        await sleep(500 * 3 ** attempt)
        continue
      }
      throw await readError(response)
    }
    if (options.as === 'response') return response as T
    if (response.status === 204 || method === 'HEAD') return undefined as T
    if (options.as === 'text') return (await response.text()) as T
    const text = await response.text()
    return (text ? JSON.parse(text) : undefined) as T
  }
}
