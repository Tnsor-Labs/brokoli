/**
 * Derived stores for computed data — avoids prop drilling and repeated filtering.
 */

import { derived } from "svelte/store";
import { pipelines } from "./stores";

/** Pipelines that have failed their last run */
export const failedPipelines = derived(pipelines, ($p) =>
  $p.filter((p: any) => p.last_run_status === "failed")
);

/** Pipelines that are currently running */
export const runningPipelines = derived(pipelines, ($p) =>
  $p.filter((p: any) => p.last_run_status === "running")
);

/** Pipelines that are paused (disabled) */
export const pausedPipelines = derived(pipelines, ($p) =>
  $p.filter((p: any) => !p.enabled)
);

/** Active (enabled) pipelines */
export const activePipelines = derived(pipelines, ($p) =>
  $p.filter((p: any) => p.enabled)
);

/** All unique tags across all pipelines */
export const allTags = derived(pipelines, ($p) =>
  [...new Set($p.flatMap((p: any) => p.tags || []))].sort()
);

/** Pipeline count stats */
export const pipelineStats = derived(pipelines, ($p) => ({
  total: $p.length,
  active: $p.filter((p: any) => p.enabled).length,
  paused: $p.filter((p: any) => !p.enabled).length,
  failed: $p.filter((p: any) => p.last_run_status === "failed").length,
  running: $p.filter((p: any) => p.last_run_status === "running").length,
}));
