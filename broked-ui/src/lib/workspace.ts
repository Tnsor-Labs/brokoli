import { writable, get } from "svelte/store";
import { authHeaders } from "./auth";

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

export async function loadWorkspaces() {
  try {
    const res = await fetch("/api/workspaces", { headers: authHeaders() });
    if (res.ok) {
      const list = await res.json();
      workspaces.set(list);
      const savedId = localStorage.getItem("brokoli-workspace");
      const current = list.find((w: Workspace) => w.id === savedId) || list.find((w: Workspace) => w.id === "default") || list[0];
      if (current) currentWorkspace.set(current);
    }
  } catch {}
}

export function switchWorkspace(ws: Workspace) {
  const prev = localStorage.getItem("brokoli-workspace");
  if (prev === ws.id) return;

  // Save new workspace to localStorage FIRST (api.ts reads it from there)
  localStorage.setItem("brokoli-workspace", ws.id);
  currentWorkspace.set(ws);

  // Full page reload — only reliable way to reset all SPA state,
  // cached stores, and re-fetch everything with new X-Workspace-ID header
  window.location.reload();
}
