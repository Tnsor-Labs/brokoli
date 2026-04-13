import { writable, get } from "svelte/store";

export interface AuthUser {
  id: string;
  username: string;
  role: "admin" | "editor" | "viewer";
  org_id?: string;
}

/**
 * Returns the SODP key for the current user's dashboard aggregate snapshot.
 *
 * The bridge maintains one `dashboard.{orgID}` key per tenant. In OSS
 * open-mode (no auth) this is "default". In EE multi-tenant, it's the
 * org_id from the user's JWT claims. Watching the wrong key means deltas
 * never arrive and the UI only updates on page refresh — which is the
 * whole class of "realtime broken in production" bugs.
 *
 * Callers must subscribe *after* `authReady` is true so `authUser` has
 * been populated from /api/auth/me.
 */
export function dashboardKey(): string {
  const u = get(authUser);
  return "dashboard." + (u?.org_id || "default");
}

// Auth state is managed via httpOnly session cookie (set by server on login).
// No tokens in localStorage — prevents XSS token theft.
export const authToken = writable<string | null>(null);
export const authUser = writable<AuthUser | null>(null);
export const needsSetup = writable<boolean>(false);
export const authReady = writable<boolean>(false);

export function setToken(token: string | null) {
  authToken.set(token);
  // Token is only held in memory for the auth-callback flow (OAuth/signup).
  // The httpOnly cookie is the real auth credential — set by the server.
}

export function logout() {
  authToken.set(null);
  authUser.set(null);
  // Clear the httpOnly cookie via server endpoint
  fetch("/api/auth/logout", { method: "POST" }).catch(() => {});
  // Also clean up any legacy localStorage tokens
  localStorage.removeItem("brokoli-token");
  window.location.hash = "#/login";
}

export function authHeaders(): Record<string, string> {
  // httpOnly cookie is sent automatically by the browser on same-origin requests.
  // Only use Authorization header for in-memory tokens (OAuth callback flow).
  const token = get(authToken);
  if (token) {
    return { Authorization: `Bearer ${token}` };
  }
  return {};
}

// Permissions store — loaded after login
export const userPermissions = writable<string[]>([]);

export async function loadPermissions() {
  try {
    const res = await fetch("/api/auth/me/permissions", { headers: authHeaders() });
    if (res.ok) {
      const data = await res.json();
      userPermissions.set(data.permissions || []);
    }
  } catch {}
}

/** Check if current user has a specific permission */
export function userCan(permission: string): boolean {
  const perms = get(userPermissions);
  // If no permissions loaded yet, allow everything (backward compat)
  if (perms.length === 0) return true;
  return perms.includes(permission);
}

/** Check auth status on app load */
export async function initAuth() {
  try {
    // Check if setup is needed
    const setupRes = await fetch("/api/auth/setup");
    if (setupRes.ok) {
      const data = await setupRes.json();
      needsSetup.set(data.needs_setup);
      if (data.needs_setup) {
        authReady.set(true);
        return;
      }
    } else {
      // Auth endpoints don't exist — open mode
      authReady.set(true);
      return;
    }

    // Pick up token from URL fragment (e.g. #/auth-callback?token=xxx&ws=yyy)
    // Fragments are never sent to the server — safer than query params
    const hash = window.location.hash;
    if (hash.includes("/auth-callback")) {
      const hashParams = new URLSearchParams(hash.split("?")[1] || "");
      const urlToken = hashParams.get("token");
      const wsId = hashParams.get("ws");
      if (urlToken) {
        setToken(urlToken);
        if (wsId) {
          localStorage.setItem("brokoli-workspace", wsId);
        }
        // Store onboarding flag for new signups
        const isNew = hashParams.get("new");
        if (isNew === "1") {
          localStorage.setItem("brokoli-onboarding", JSON.stringify({
            show_welcome: true,
            steps: [
              { id: "create_connection", label: "Add your first data source", completed: false },
              { id: "create_pipeline", label: "Build your first pipeline", completed: false },
              { id: "first_run", label: "Run your pipeline", completed: false },
              { id: "invite_member", label: "Invite a team member", completed: false },
            ],
          }));
        }
        // Clean URL immediately — remove token from browser history
        window.history.replaceState({}, "", window.location.pathname + "#/");
      }
    }

    // Also handle legacy ?token= query param (e.g. from OAuth callbacks)
    const queryParams = new URLSearchParams(window.location.search);
    const queryToken = queryParams.get("token");
    if (queryToken) {
      setToken(queryToken);
      window.history.replaceState({}, "", window.location.pathname + "#/");
    }

    // Validate session via httpOnly cookie (sent automatically by browser)
    const meRes = await fetch("/api/auth/me");
    if (meRes.ok) {
      const claims = await meRes.json();
      authUser.set({
        id: claims.sub,
        username: claims.username,
        role: claims.role,
        org_id: claims.org_id,
      });
    }
  } catch {
    // Server might not have auth — open mode
  }
  authReady.set(true);
}

export async function login(username: string, password: string): Promise<string | null> {
  try {
    const res = await fetch("/api/auth/login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ username, password }),
    });
    if (!res.ok) {
      const data = await res.json();
      return data.error || "Login failed";
    }
    const data = await res.json();
    // Server sets httpOnly cookie via withSessionCookie wrapper.
    // Keep token in memory only for the current page load (used by authHeaders
    // until the next request picks up the cookie).
    authToken.set(data.token);
    authUser.set(data.user);
    return null;
  } catch {
    return "Connection error";
  }
}

export async function createFirstUser(username: string, password: string): Promise<string | null> {
  try {
    const res = await fetch("/api/auth/users", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ username, password, role: "admin" }),
    });
    if (!res.ok) {
      const data = await res.json();
      return data.error || "Failed to create user";
    }
    needsSetup.set(false);
    return await login(username, password);
  } catch {
    return "Connection error";
  }
}
