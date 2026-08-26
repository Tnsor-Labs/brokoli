import { writable } from "svelte/store";

export interface Workspace {
  id: string;
  name: string;
  slug: string;
  description: string;
}

export interface WorkspaceMember {
  workspace_id: string;
  user_id: string;
  username: string;
  role: string;
  joined_at: string;
}

export interface APIToken {
  id: string;
  name: string;
  token?: string;
  workspace_id: string;
  role: string;
  expires_at: string;
  created_at: string;
}

export const workspaces = writable<Workspace[]>([]);
export const currentWorkspace = writable<Workspace | null>(null);

// Increments on every workspace switch — pages subscribe to this to reload data
export const workspaceVersion = writable(0);

const STORAGE_KEY = "brokoli-workspace";
const OWNER_KEY = "brokoli-workspace-user";

/**
 * The workspace id this session is willing to put on the wire.
 *
 * localStorage is per-origin, not per-instance: the same `http://localhost:8088`
 * serves whichever build was last run there, so a stored id routinely names a
 * workspace the server has never heard of. Sending one unchecked is #231 —
 * every scoped list comes back an empty 200 and the product reads as brand
 * new, or, where the id is checkable, every request 403s with no way to
 * recover from inside the app.
 *
 * So a stored id is a *hint* until a workspace list confirms it, and this
 * stays null until then. Null means "send no header", which is not a
 * degraded mode: the server resolves an absent header to a workspace the
 * caller is entitled to, which is the correct answer whenever we cannot
 * name a better one.
 */
let validatedID: string | null = null;

/** Headers for a workspace-scoped request. Empty until an id is confirmed. */
export function workspaceHeaders(): Record<string, string> {
  return validatedID ? { "X-Workspace-ID": validatedID } : {};
}

/** The confirmed id, or null. Exposed for tests and for display. */
export function validatedWorkspaceId(): string | null {
  return validatedID;
}

/**
 * Confirms the stored workspace against the ones this user actually has, and
 * repairs or drops it. Call once, before anything fetches — `initAuth` awaits
 * it ahead of `authReady`, and the app renders nothing until then, so no page
 * can race it with a request carrying an unconfirmed id.
 *
 * Every failure path leaves `validatedID` null rather than falling back to the
 * stored value. A build with no workspace list (the endpoint 404s) has nothing
 * to confirm against, and that is precisely the case where the stored id is
 * likeliest to be someone else's.
 */
export async function resolveWorkspace(headers: Record<string, string>): Promise<void> {
  validatedID = null;

  let list: Workspace[];
  try {
    const res = await fetch("/api/workspaces", { headers });
    if (!res.ok) return;
    list = await res.json();
  } catch {
    return;
  }
  if (!Array.isArray(list)) return;

  workspaces.set(list);
  const stored = localStorage.getItem(STORAGE_KEY);
  const current =
    list.find((w) => w.id === stored) || list.find((w) => w.id === "default") || list[0] || null;

  currentWorkspace.set(current);
  if (current) {
    localStorage.setItem(STORAGE_KEY, current.id);
    validatedID = current.id;
  } else {
    localStorage.removeItem(STORAGE_KEY);
  }
}

/**
 * Drops a workspace stored for a different user.
 *
 * One browser profile signs into more than one account, and a workspace id is
 * an identity-scoped value: carrying one across a change of user asks for
 * someone else's scope. `hintIsFresh` marks an id this same navigation
 * received from an SSO callback — that one belongs to the user just resolved,
 * so it is adopted rather than discarded.
 */
export function adoptWorkspaceForUser(userID: string, hintIsFresh: boolean) {
  const previousOwner = localStorage.getItem(OWNER_KEY);
  if (!hintIsFresh && previousOwner && previousOwner !== userID) {
    localStorage.removeItem(STORAGE_KEY);
  }
  localStorage.setItem(OWNER_KEY, userID);
}

export function switchWorkspace(ws: Workspace) {
  const prev = localStorage.getItem(STORAGE_KEY);
  if (prev === ws.id) return;

  // Save new workspace to localStorage FIRST (resolveWorkspace reads it from
  // there on the next load, and confirms it before anything is sent)
  localStorage.setItem(STORAGE_KEY, ws.id);
  currentWorkspace.set(ws);

  // Full page reload — only reliable way to reset all SPA state,
  // cached stores, and re-fetch everything with new X-Workspace-ID header
  window.location.reload();
}
