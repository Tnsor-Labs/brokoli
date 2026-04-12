import { writable } from "svelte/store";
import type { Pipeline, Run } from "./types";

// Page-level data stores. Pages that need realtime updates do so via
// `getSodpClient().watch(...)` against specific state keys (e.g.
// dashboard.{org}, runs.{id}, runs.{id}.logs) — there is no global event
// store anymore. Stale: addEvent, onWSEvent, events, liveRunStatuses,
// liveNodeStatuses — all deleted as part of the SODP-native UI refactor.
export const pipelines = writable<Pipeline[]>([]);
export const currentPipeline = writable<Pipeline | null>(null);
export const runs = writable<Run[]>([]);
