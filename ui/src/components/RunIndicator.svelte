<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { getSodpClient } from "../lib/sodp";
  import { dashboardKey } from "../lib/auth";
  import type { RunStatus } from "../lib/types";

  // Subset of dashboard.{org} we read here. Mirrors the field names in
  // pkg/sodp/bridge.go:recomputeDashboard.
  interface DashboardSnapshot {
    runs_running: number;
    running_run_ids: string[];
    runs_24h_success: number;
    runs_24h_failed: number;
    recent_runs: Array<{
      run_id: string;
      pipeline_id: string;
      status: string;
      started_at: string;
      finished_at: string | null;
    }>;
  }

  let expanded = false;
  let flashStatus: RunStatus | "" = "";
  let flashTimer: ReturnType<typeof setTimeout>;

  let runningCount = 0;
  let runningIds: string[] = [];
  let recentRuns: Array<{ id: string; pipeline_id: string; status: string; started_at: string }> = [];

  // Track previous-vs-current to derive the "flash on completion/failure"
  // visual effect without needing an event stream. We compare the 24h
  // success/fail counters between snapshots — any positive delta means a
  // run terminated since the last update.
  let prevSuccess = -1;
  let prevFailed = -1;

  let unsubDashboard: (() => void) | null = null;

  onMount(() => {
    const client = getSodpClient();
    unsubDashboard = client.watch<DashboardSnapshot>(dashboardKey(), (snap) => {
      if (!snap) return;

      // Detect transitions for the flash effect (skip the very first snapshot
      // we ever see, otherwise the indicator would flash on every page load
      // if there's any history at all).
      if (prevSuccess >= 0 && prevFailed >= 0) {
        if ((snap.runs_24h_success ?? 0) > prevSuccess) flash("success");
        if ((snap.runs_24h_failed ?? 0) > prevFailed) flash("failed");
      }
      prevSuccess = snap.runs_24h_success ?? 0;
      prevFailed = snap.runs_24h_failed ?? 0;

      runningCount = snap.runs_running ?? 0;
      runningIds = snap.running_run_ids ?? [];
      recentRuns = (snap.recent_runs ?? []).map(r => ({
        id: r.run_id,
        pipeline_id: r.pipeline_id,
        status: r.status,
        started_at: r.started_at,
      }));
    });
  });

  onDestroy(() => { unsubDashboard?.(); });

  $: hasActivity = recentRuns.length > 0 || runningCount > 0;

  function flash(status: RunStatus) {
    flashStatus = status;
    clearTimeout(flashTimer);
    flashTimer = setTimeout(() => flashStatus = "", 3000);
  }

  $: indicatorColor = flashStatus === "success" ? "var(--success)"
    : flashStatus === "failed" ? "var(--failed)"
    : runningCount > 0 ? "var(--running)"
    : "var(--text-dim)";

  $: indicatorGlow = flashStatus === "success" ? "var(--success-glow)"
    : flashStatus === "failed" ? "rgba(239, 68, 68, 0.4)"
    : runningCount > 0 ? "rgba(59, 130, 246, 0.4)"
    : "transparent";

  function statusLabel(s: RunStatus): string {
    switch (s) {
      case "running": return "Running";
      case "success": return "Success";
      case "failed": return "Failed";
      case "pending": return "Pending";
      default: return s;
    }
  }

  function statusColor(s: RunStatus): string {
    switch (s) {
      case "running": return "var(--running)";
      case "success": return "var(--success)";
      case "failed": return "var(--failed)";
      case "pending": return "var(--warning)";
      default: return "var(--text-dim)";
    }
  }

  function timeAgo(ts: string): string {
    if (!ts) return "";
    const s = Math.floor((Date.now() - new Date(ts).getTime()) / 1000);
    if (s < 60) return `${s}s`;
    if (s < 3600) return `${Math.floor(s / 60)}m`;
    return `${Math.floor(s / 3600)}h`;
  }
</script>

<div class="run-indicator" class:expanded>
  <!-- Floating button -->
  <button class="indicator-btn" on:click={() => expanded = !expanded}
    style="--color: {indicatorColor}; --glow: {indicatorGlow}">
    <span class="indicator-ring" class:spinning={runningCount > 0}></span>
    {#if runningCount > 0}
      <span class="indicator-count">{runningCount}</span>
    {:else if flashStatus === "success"}
      <span class="indicator-icon check">&#10003;</span>
    {:else if flashStatus === "failed"}
      <span class="indicator-icon fail">!</span>
    {:else}
      <svg class="indicator-idle" width="16" height="16" viewBox="0 0 24 24" fill="none">
        <polygon points="5 3 19 12 5 21 5 3" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
      </svg>
    {/if}
  </button>

  <!-- Expanded panel -->
  {#if expanded}
    <div class="indicator-panel">
      <div class="panel-header">
        <span class="panel-title">Recent Runs</span>
        <button class="panel-close" on:click={() => expanded = false}>&times;</button>
      </div>
      <div class="panel-list">
        {#each recentRuns as run}
          {@const status = run.status as RunStatus}
          <a href="#/pipelines/{run.pipeline_id}/runs" class="panel-item" on:click={() => expanded = false}>
            <span class="item-dot" class:dot-pulse={status === "running"} style="background: {statusColor(status)}"></span>
            <span class="item-name">{run.pipeline_id ? run.pipeline_id.slice(0, 8) : run.id.slice(0, 8)}</span>
            <span class="item-status" style="color: {statusColor(status)}">{statusLabel(status)}</span>
            <span class="item-time">{timeAgo(run.started_at)}</span>
          </a>
        {/each}
        {#if recentRuns.length === 0}
          <div class="panel-empty">No runs yet. Trigger a pipeline to see activity here.</div>
        {/if}
      </div>
    </div>
  {/if}
</div>

<style>
  .run-indicator {
    position: fixed; bottom: 24px; right: 24px; z-index: 95;
  }

  .indicator-btn {
    width: 44px; height: 44px; border-radius: 50%;
    background: var(--bg-secondary); border: 2px solid var(--color);
    display: flex; align-items: center; justify-content: center;
    cursor: pointer; position: relative;
    box-shadow: 0 4px 16px rgba(0,0,0,0.3), 0 0 12px var(--glow);
    transition: all 200ms ease;
  }
  .indicator-btn:hover {
    transform: scale(1.08);
    box-shadow: 0 6px 20px rgba(0,0,0,0.4), 0 0 20px var(--glow);
  }

  .indicator-ring {
    position: absolute; inset: -4px; border-radius: 50%;
    border: 2px solid transparent; border-top-color: var(--color);
  }
  .indicator-ring.spinning {
    animation: spin-ring 1.2s linear infinite;
  }
  @keyframes spin-ring {
    to { transform: rotate(360deg); }
  }

  .indicator-count {
    font-size: 14px; font-weight: 700; color: var(--color);
    font-family: var(--font-mono);
  }
  .indicator-icon {
    font-size: 16px; font-weight: 700; color: var(--color);
  }
  .indicator-icon.check { font-size: 18px; }
  .indicator-icon.fail { font-size: 18px; }
  .indicator-idle { color: var(--text-dim); }
  .item-dot.dot-pulse { animation: dot-blink 1.2s ease-in-out infinite; }
  @keyframes dot-blink { 0%,100% { opacity: 1; } 50% { opacity: 0.3; } }

  /* Panel — anchored to the button's right edge so it expands leftward into
     the viewport instead of overflowing past the right edge. The button itself
     lives at right: 24px in the viewport, so a left-anchored panel would
     extend 300px further right and clip off-screen. */
  .indicator-panel {
    position: absolute; bottom: 56px; right: 0;
    width: 300px; background: var(--bg-secondary);
    border: 1px solid var(--border); border-radius: var(--radius-xl, 14px);
    box-shadow: 0 12px 40px rgba(0,0,0,0.4);
    overflow: hidden;
    animation: panel-in 200ms cubic-bezier(0.16, 1, 0.3, 1);
  }
  @keyframes panel-in {
    from { opacity: 0; transform: translateY(8px) scale(0.96); }
    to { opacity: 1; transform: translateY(0) scale(1); }
  }

  .panel-header {
    display: flex; justify-content: space-between; align-items: center;
    padding: 12px 16px; border-bottom: 1px solid var(--border-subtle);
  }
  .panel-title { font-size: 13px; font-weight: 600; }
  .panel-close {
    font-size: 18px; color: var(--text-dim); padding: 2px 6px;
    border-radius: 4px; line-height: 1;
  }
  .panel-close:hover { color: var(--text-primary); background: var(--bg-tertiary); }

  .panel-list { max-height: 280px; overflow-y: auto; }
  .panel-item {
    display: flex; align-items: center; gap: 8px;
    padding: 10px 16px; border-bottom: 1px solid var(--border-subtle);
    text-decoration: none; color: inherit; font-size: 12px;
    transition: background 150ms ease;
  }
  .panel-item:last-child { border-bottom: none; }
  .panel-item:hover { background: rgba(255,255,255,0.02); }

  .item-dot {
    width: 7px; height: 7px; border-radius: 50%; flex-shrink: 0;
  }
  .item-name {
    flex: 1; font-family: var(--font-mono); font-weight: 500;
    color: var(--text-primary); font-size: 11px;
  }
  .item-status { font-size: 11px; font-weight: 500; }
  .item-time { font-size: 10px; color: var(--text-dim); font-family: var(--font-mono); }

  .panel-empty {
    padding: 20px; text-align: center; font-size: 12px; color: var(--text-dim);
  }

  @media (max-width: 768px) {
    .run-indicator { bottom: 72px; right: 16px; }
    .indicator-panel { width: 260px; }
  }
</style>
