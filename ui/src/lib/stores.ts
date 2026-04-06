import { writable, get } from "svelte/store";
import type { Pipeline, Run, WSEvent, RunStatus } from "./types";

export const pipelines = writable<Pipeline[]>([]);
export const currentPipeline = writable<Pipeline | null>(null);
export const runs = writable<Run[]>([]);
export const events = writable<WSEvent[]>([]);

// Live run status updates from WebSocket
export const liveRunStatuses = writable<Record<string, RunStatus>>({});
export const liveNodeStatuses = writable<Record<string, Record<string, RunStatus>>>({});

// Callbacks for pages to react to events
type EventCallback = (event: WSEvent) => void;
const callbacks: EventCallback[] = [];

export function onWSEvent(cb: EventCallback): () => void {
  callbacks.push(cb);
  return () => {
    const idx = callbacks.indexOf(cb);
    if (idx >= 0) callbacks.splice(idx, 1);
  };
}

export function addEvent(event: WSEvent) {
  events.update((list) => {
    const updated = [event, ...list];
    return updated.slice(0, 100);
  });

  // Update live statuses
  if (event.run_id) {
    if (event.type === "run.started" || event.type === "run.completed" || event.type === "run.failed") {
      liveRunStatuses.update((s) => {
        const status: RunStatus =
          event.type === "run.started" ? "running" :
          event.type === "run.completed" ? "success" : "failed";
        return { ...s, [event.run_id]: status };
      });
    }

    if (event.node_id && (event.type === "node.started" || event.type === "node.completed" || event.type === "node.failed")) {
      liveNodeStatuses.update((s) => {
        const runNodes = { ...(s[event.run_id] || {}) };
        const status: RunStatus =
          event.type === "node.started" ? "running" :
          event.type === "node.completed" ? "success" : "failed";
        runNodes[event.node_id!] = status;
        return { ...s, [event.run_id]: runNodes };
      });
    }
  }

  // Notify all registered callbacks
  for (const cb of callbacks) {
    try { cb(event); } catch { /* ignore */ }
  }
}
