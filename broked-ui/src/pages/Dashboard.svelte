<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { api } from "../lib/api";
  import { notify } from "../lib/toast";
  import { pipelines, onWSEvent } from "../lib/stores";
  import { authHeaders } from "../lib/auth";
  import StatusBadge from "../components/StatusBadge.svelte";
  import type { Pipeline, Run } from "../lib/types";

  let recentRuns: { pipeline: Pipeline; run: Run }[] = [];
  let loading = true;
  let unsubWS: (() => void) | null = null;

  // Stats
  let totalPipelines = 0;
  let activePipelines = 0;
  let pausedPipelines = 0;
  let runsToday = 0;
  let runsYesterday = 0;
  let successRate = 0;
  let failedLast24h = 0;
  let currentlyRunning = 0;

  // Scheduler
  let nextScheduled: { name: string; next: string }[] = [];

  // Failed pipelines needing attention
  let failedPipelines: { pipeline: Pipeline; run: Run }[] = [];

  async function loadDashboard() {
    try {
      const pipelineList = await api.pipelines.list();
      pipelines.set(pipelineList);
      totalPipelines = pipelineList.length;
      activePipelines = pipelineList.filter(p => p.enabled).length;
      pausedPipelines = totalPipelines - activePipelines;

      const allRuns: { pipeline: Pipeline; run: Run }[] = [];
      for (const p of pipelineList) {
        const r = await api.runs.listByPipeline(p.id);
        for (const run of r.slice(0, 5)) {
          allRuns.push({ pipeline: p, run });
        }
      }

      // Sort by time
      allRuns.sort((a, b) => (b.run.started_at || "").localeCompare(a.run.started_at || ""));
      recentRuns = allRuns.slice(0, 12);

      // Today's stats
      const now = new Date();
      const todayStr = `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, "0")}-${String(now.getDate()).padStart(2, "0")}`;
      const yesterday = new Date(now);
      yesterday.setDate(yesterday.getDate() - 1);
      const yesterdayStr = `${yesterday.getFullYear()}-${String(yesterday.getMonth() + 1).padStart(2, "0")}-${String(yesterday.getDate()).padStart(2, "0")}`;

      const todayRuns = allRuns.filter(r => r.run.started_at?.startsWith(todayStr));
      const yesterdayRuns = allRuns.filter(r => r.run.started_at?.startsWith(yesterdayStr));
      runsToday = todayRuns.length;
      runsYesterday = yesterdayRuns.length;

      // Success rate (last 24h)
      const last24h = allRuns.filter(r => {
        if (!r.run.started_at) return false;
        return (now.getTime() - new Date(r.run.started_at).getTime()) < 86400000;
      });
      const succeeded = last24h.filter(r => r.run.status === "success").length;
      successRate = last24h.length ? Math.round((succeeded / last24h.length) * 100) : 100;
      failedLast24h = last24h.filter(r => r.run.status === "failed").length;
      currentlyRunning = allRuns.filter(r => r.run.status === "running").length;

      // Failed pipelines needing attention (most recent failed run per pipeline)
      const seenFailed = new Set<string>();
      failedPipelines = [];
      for (const r of allRuns) {
        if (r.run.status === "failed" && !seenFailed.has(r.pipeline.id)) {
          seenFailed.add(r.pipeline.id);
          failedPipelines.push(r);
        }
      }

      // Scheduler
      try {
        const res = await fetch("/api/scheduler/status", { headers: authHeaders() });
        if (res.ok) {
          const schedData = await res.json();
          nextScheduled = schedData
            .filter((s: any) => s.next_run)
            .sort((a: any, b: any) => a.next_run.localeCompare(b.next_run))
            .slice(0, 5)
            .map((s: any) => ({ name: s.pipeline_name, next: s.next_run }));
        }
      } catch {}

    } catch (e) {
      notify.error("Failed to load dashboard");
    } finally {
      loading = false;
    }
  }

  onMount(() => {
    loadDashboard();
    unsubWS = onWSEvent((event) => {
      if (event.type === "run.completed" || event.type === "run.failed" || event.type === "run.started") {
        loadDashboard();
      }
    });
  });

  onDestroy(() => { unsubWS?.(); });

  function timeAgo(dateStr: string | null): string {
    if (!dateStr) return "";
    const seconds = Math.floor((Date.now() - new Date(dateStr).getTime()) / 1000);
    if (seconds < 60) return `${seconds}s ago`;
    if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
    if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`;
    return `${Math.floor(seconds / 86400)}d ago`;
  }

  function formatNextRun(isoStr: string): string {
    const d = new Date(isoStr);
    const now = new Date();
    const diffMs = d.getTime() - now.getTime();
    if (diffMs < 0) return "overdue";
    const mins = Math.floor(diffMs / 60000);
    if (mins < 60) return `in ${mins}m`;
    const hrs = Math.floor(mins / 60);
    if (hrs < 24) return `in ${hrs}h ${mins % 60}m`;
    return `in ${Math.floor(hrs / 24)}d`;
  }

  function successRateColor(rate: number): string {
    if (rate >= 90) return "#22c55e";
    if (rate >= 70) return "#f59e0b";
    return "#ef4444";
  }

  function trendIcon(today: number, yesterday: number): string {
    if (today > yesterday) return "+";
    if (today < yesterday) return "-";
    return "=";
  }
</script>

<div class="dashboard animate-in">
  <header class="page-header">
    <h1>Dashboard</h1>
    <span class="page-sub">Last 24 hours overview</span>
  </header>

  {#if loading}
    <div class="empty-state">Loading...</div>
  {:else}
    <!-- Stats row -->
    <div class="stats-grid">
      <div class="stat-card">
        <div class="stat-top">
          <span class="stat-value">{totalPipelines}</span>
          <span class="stat-detail">{activePipelines} active, {pausedPipelines} paused</span>
        </div>
        <span class="stat-label">Pipelines</span>
      </div>

      <div class="stat-card">
        <div class="stat-top">
          <span class="stat-value">{runsToday}</span>
          <span class="stat-trend" class:up={runsToday > runsYesterday} class:down={runsToday < runsYesterday}>
            {trendIcon(runsToday, runsYesterday)} vs yesterday ({runsYesterday})
          </span>
        </div>
        <span class="stat-label">Runs Today</span>
      </div>

      <div class="stat-card">
        <div class="stat-top">
          <span class="stat-value" style="color: {successRateColor(successRate)}">{successRate}%</span>
        </div>
        <span class="stat-label">Success Rate (24h)</span>
      </div>

      <div class="stat-card">
        <div class="stat-top">
          <span class="stat-value" style="color: {currentlyRunning > 0 ? '#3b82f6' : ''}">{currentlyRunning}</span>
        </div>
        <span class="stat-label">Running Now</span>
      </div>

      <div class="stat-card">
        <div class="stat-top">
          <span class="stat-value" style="color: {failedLast24h > 0 ? '#ef4444' : ''}">{failedLast24h}</span>
        </div>
        <span class="stat-label">Failed (24h)</span>
      </div>
    </div>

    <div class="two-col">
      <!-- Left: Recent Runs -->
      <section class="section">
        <h2 class="section-title">Recent Runs</h2>
        {#if recentRuns.length === 0}
          <div class="empty-state"><p class="hint">No runs yet.</p></div>
        {:else}
          <div class="runs-table">
            <div class="table-header">
              <span class="col-pipeline">Pipeline</span>
              <span class="col-status">Status</span>
              <span class="col-duration">Duration</span>
              <span class="col-time">Started</span>
            </div>
            {#each recentRuns as { pipeline, run }}
              <a href="#/pipelines/{pipeline.id}/runs" class="table-row" class:row-failed={run.status === "failed"} class:row-running={run.status === "running"}>
                <span class="col-pipeline">{pipeline.name}</span>
                <span class="col-status"><StatusBadge status={run.status} size="sm" /></span>
                <span class="col-duration mono">
                  {#if run.finished_at && run.started_at}
                    {@const ms = new Date(run.finished_at).getTime() - new Date(run.started_at).getTime()}
                    {#if ms < 1000}
                      {ms}ms
                    {:else if ms < 60000}
                      {(ms / 1000).toFixed(1)}s
                    {:else}
                      {Math.floor(ms / 60000)}m {Math.floor((ms % 60000) / 1000)}s
                    {/if}
                  {:else if run.status === "running"}
                    <span class="running-dot"></span>
                  {:else}
                    -
                  {/if}
                </span>
                <span class="col-time mono">{timeAgo(run.started_at)}</span>
              </a>
            {/each}
          </div>
        {/if}
      </section>

      <!-- Right column -->
      <div class="right-col">
        <!-- Failed needing attention -->
        {#if failedPipelines.length > 0}
          <section class="section">
            <h2 class="section-title section-title-red">Needs Attention</h2>
            <div class="attention-list">
              {#each failedPipelines as { pipeline, run }}
                <a href="#/pipelines/{pipeline.id}/runs" class="attention-item">
                  <span class="attention-dot"></span>
                  <span class="attention-name">{pipeline.name}</span>
                  <span class="attention-time mono">{timeAgo(run.started_at)}</span>
                </a>
              {/each}
            </div>
          </section>
        {/if}

        <!-- Next scheduled -->
        <section class="section">
          <h2 class="section-title">Upcoming Scheduled</h2>
          {#if nextScheduled.length === 0}
            <div class="empty-state small"><p class="hint">No scheduled pipelines.</p></div>
          {:else}
            <div class="schedule-list">
              {#each nextScheduled as s}
                <div class="schedule-item">
                  <span class="schedule-name">{s.name}</span>
                  <span class="schedule-time mono">{formatNextRun(s.next)}</span>
                </div>
              {/each}
            </div>
          {/if}
        </section>
      </div>
    </div>
  {/if}
</div>

<style>
  .page-header { margin-bottom: var(--space-lg); display: flex; align-items: baseline; gap: 12px; }
  .page-header h1 { font-size: 1.5rem; font-weight: 600; letter-spacing: -0.02em; }
  .page-sub { font-size: 12px; color: var(--text-muted); }

  /* Stats */
  .stats-grid {
    display: grid;
    grid-template-columns: repeat(5, 1fr);
    gap: 10px;
    margin-bottom: var(--space-lg);
  }
  .stat-card {
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    padding: 16px 18px;
    display: flex; flex-direction: column; gap: 4px;
  }
  .stat-top { display: flex; align-items: baseline; gap: 8px; }
  .stat-value {
    font-size: 1.75rem; font-weight: 700;
    font-family: var(--font-mono); letter-spacing: -0.02em;
  }
  .stat-detail { font-size: 11px; color: var(--text-muted); }
  .stat-trend { font-size: 10px; color: var(--text-dim); font-family: var(--font-mono); }
  .stat-trend.up { color: #22c55e; }
  .stat-trend.down { color: #ef4444; }
  .stat-label {
    font-size: 10px; color: var(--text-muted);
    text-transform: uppercase; letter-spacing: 0.08em; font-weight: 600;
  }

  /* Two column layout */
  .two-col {
    display: grid;
    grid-template-columns: 1fr 320px;
    gap: var(--space-md);
    align-items: start;
  }

  .section { margin-bottom: var(--space-md); }
  .section-title {
    font-size: 11px; font-weight: 600; color: var(--text-muted);
    text-transform: uppercase; letter-spacing: 0.08em;
    margin-bottom: 8px;
  }
  .section-title-red { color: #ef4444; }

  .empty-state {
    background: var(--bg-secondary); border: 1px solid var(--border);
    border-radius: var(--radius-lg); padding: var(--space-lg);
    text-align: center; color: var(--text-secondary);
  }
  .empty-state.small { padding: var(--space-md); }
  .hint { color: var(--text-muted); font-size: 12px; }

  /* Recent Runs table */
  .runs-table {
    background: var(--bg-secondary); border: 1px solid var(--border);
    border-radius: var(--radius-lg); overflow: hidden;
  }
  .table-header, .table-row {
    display: grid;
    grid-template-columns: 1fr 90px 70px 80px;
    padding: 8px 14px; align-items: center;
  }
  .table-header {
    background: var(--bg-tertiary); font-size: 10px; color: var(--text-muted);
    text-transform: uppercase; letter-spacing: 0.08em; font-weight: 600;
    border-bottom: 1px solid var(--border);
  }
  .table-row {
    border-bottom: 1px solid var(--border-subtle);
    font-size: 13px; transition: background 150ms ease;
    text-decoration: none; color: inherit; display: grid;
  }
  .table-row:last-child { border-bottom: none; }
  .table-row:hover { background: var(--bg-tertiary); }
  .table-row.row-failed { border-left: 3px solid #ef4444; }
  .table-row.row-running { border-left: 3px solid #3b82f6; }
  .mono { font-family: var(--font-mono); font-size: 11px; color: var(--text-muted); }
  .running-dot {
    display: inline-block; width: 6px; height: 6px; border-radius: 50%;
    background: #3b82f6; animation: pulse-run 1s ease-in-out infinite;
  }
  @keyframes pulse-run { 0%, 100% { opacity: 1; } 50% { opacity: 0.3; } }

  /* Needs Attention */
  .attention-list {
    background: var(--bg-secondary); border: 1px solid rgba(239,68,68,0.2);
    border-radius: var(--radius-lg); overflow: hidden;
  }
  .attention-item {
    display: flex; align-items: center; gap: 8px;
    padding: 10px 14px; border-bottom: 1px solid var(--border-subtle);
    text-decoration: none; color: inherit; transition: background 150ms ease;
  }
  .attention-item:last-child { border-bottom: none; }
  .attention-item:hover { background: var(--bg-tertiary); }
  .attention-dot {
    width: 6px; height: 6px; border-radius: 50%;
    background: #ef4444; flex-shrink: 0;
    animation: pulse-dot 2s ease-in-out infinite;
  }
  @keyframes pulse-dot {
    0%, 100% { opacity: 1; }
    50% { opacity: 0.4; }
  }
  .attention-name { flex: 1; font-size: 13px; font-weight: 500; }
  .attention-time { font-size: 11px; }

  /* Scheduled */
  .schedule-list {
    background: var(--bg-secondary); border: 1px solid var(--border);
    border-radius: var(--radius-lg); overflow: hidden;
  }
  .schedule-item {
    display: flex; justify-content: space-between; align-items: center;
    padding: 10px 14px; border-bottom: 1px solid var(--border-subtle);
    font-size: 13px;
  }
  .schedule-item:last-child { border-bottom: none; }
  .schedule-name { font-weight: 500; }
  .schedule-time { font-size: 11px; color: var(--accent); }

  .right-col { display: flex; flex-direction: column; gap: var(--space-md); }
</style>
