/**
 * SODP client singleton.
 *
 * Pages call `getSodpClient()` to obtain a lazily-created SodpClient bound to
 * the current page's WebSocket origin. The client is created once on first
 * use and reused thereafter — there's exactly one WebSocket per browser tab.
 *
 * Authentication: the SODP server's HTTP middleware validates the JWT cookie
 * at upgrade time and propagates `org_id` into the session context, so we
 * don't need to send a token through SODP's AUTH frame on same-origin
 * connections. The browser carries the cookie automatically.
 *
 * Lifecycle: see App.svelte. The connection is opened when $authUser becomes
 * truthy and closed on logout. Pages that subscribe BEFORE the client opens
 * (e.g. mounted before login completes) get queued — `@sodp/client.watch()`
 * is safe to call before `client.ready` resolves; it sends the WATCH frame
 * the moment the connection is established.
 */
import { writable } from "svelte/store";
import { SodpClient } from "@sodp/client";
import { authHeaders, invalidateAuth } from "./auth";

export const wsConnected = writable(false);

let client: SodpClient | null = null;
let validatingSession = false;

async function handleDisconnect(): Promise<void> {
  wsConnected.set(false);
  if (validatingSession) return;
  validatingSession = true;
  try {
    const headers = authHeaders();
    const res = await fetch("/api/auth/me", { headers });
    if (res.status === 401 || res.status === 403) {
      const failedClient = client;
      client = null;
      failedClient?.close();
      invalidateAuth();
      window.location.hash = "#/login";
    } else if (res.ok && headers.Authorization) {
      // Repair a missing cookie for a still-valid legacy bearer session.
      await fetch("/api/auth/session", { method: "POST", headers });
    }
  } catch {
    // A network outage is recoverable; leave exponential reconnect enabled.
  } finally {
    validatingSession = false;
  }
}

/**
 * Returns the singleton SodpClient, creating it on first call. Subsequent
 * calls return the same instance. If the client was previously closed
 * (e.g. by logout), this creates a fresh one.
 */
export function getSodpClient(): SodpClient {
  if (client) return client;

  const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
  const url = `${protocol}//${window.location.host}/api/ws`;

  client = new SodpClient(url, {
    reconnect: true,
    reconnectDelay: 1000,
    maxReconnectDelay: 30000,
    onConnect: () => wsConnected.set(true),
    onDisconnect: () => void handleDisconnect(),
  });

  return client;
}

/**
 * Closes the current client and clears the singleton. Called from App.svelte
 * on logout. Subsequent getSodpClient() calls will create a fresh client.
 */
export function closeSodpClient(): void {
  if (client) {
    client.close();
    client = null;
    wsConnected.set(false);
  }
}
