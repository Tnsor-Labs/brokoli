import { SodpClient, type WatchMeta } from '@sodp/client'
import { ApiError, currentToken } from './client'
import { authApi } from './endpoints'

/*
 * Live state over SODP (one websocket per tab at /api/ws, authenticated by
 * the session cookie at upgrade time).
 *
 * Two fixes over the Svelte wrapper:
 * - watchKey() reference-counts callbacks per key and sends UNWATCH when the
 *   last one goes away. The Svelte UI only dropped the callback, so every
 *   run a user expanded kept a server watch alive until the 64-watch session
 *   limit silently stopped live logs.
 * - Protocol errors (403 access denied, 429 watch limit) are reported to the
 *   console with the key involved instead of disappearing.
 */

export type { WatchMeta }

let client: SodpClient | null = null
let connected = false
let validating = false
let onSessionLost: () => void = () => {}
const connectionListeners = new Set<(connected: boolean) => void>()
const refs = new Map<string, number>()

function setConnected(value: boolean) {
  connected = value
  connectionListeners.forEach((listener) => listener(value))
}

export function setLiveSessionLostHandler(handler: () => void) {
  onSessionLost = handler
}

/** Subscribe to connection state; the listener is called immediately with the current value. */
export function onLiveConnection(listener: (connected: boolean) => void) {
  connectionListeners.add(listener)
  listener(connected)
  return () => {
    connectionListeners.delete(listener)
  }
}

async function handleDisconnect() {
  setConnected(false)
  if (validating) return
  validating = true
  try {
    await authApi.me()
    // Session still valid. If a bearer is in play, make sure the cookie the socket needs exists.
    if (currentToken()) await authApi.session().catch(() => {})
  } catch (e) {
    if (e instanceof ApiError && (e.status === 401 || e.status === 403)) {
      closeLiveClient()
      onSessionLost()
    }
    // Anything else is an outage; the client keeps reconnecting with backoff.
  } finally {
    validating = false
  }
}

export function getLiveClient(): SodpClient {
  if (client) return client
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  client = new SodpClient(`${protocol}//${window.location.host}/api/ws`, {
    reconnect: true,
    reconnectDelay: 1000,
    maxReconnectDelay: 30_000,
    onConnect: () => setConnected(true),
    onDisconnect: () => void handleDisconnect(),
    onError: (error) => {
      if (error.source === 'transport') return
      console.warn(`[brokoli-ui] live update ${error.source} error${error.code ? ` ${error.code}` : ''}: ${error.message}`)
    },
  })
  return client
}

export function closeLiveClient() {
  client?.close()
  client = null
  refs.clear()
  setConnected(false)
}

/**
 * Watch a state key. Returns an unsubscribe function; when the last watcher
 * of a key unsubscribes, the server subscription is released too.
 */
export function watchKey<T>(key: string, callback: (value: T | null, meta: WatchMeta) => void): () => void {
  const live = getLiveClient()
  refs.set(key, (refs.get(key) ?? 0) + 1)
  const off = live.watch<T>(key, callback)
  let active = true
  return () => {
    if (!active) return
    active = false
    off()
    const remaining = (refs.get(key) ?? 1) - 1
    if (remaining > 0) refs.set(key, remaining)
    else {
      refs.delete(key)
      // The client may have been replaced (logout, reconnect after session loss).
      if (client === live) live.unwatch(key)
    }
  }
}

export function dashboardKey(orgId?: string | null) {
  return `dashboard.${orgId || 'default'}`
}

export function runLogsKey(runId: string) {
  return `runs.${runId}.logs`
}
