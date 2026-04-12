<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { api } from "../lib/api";
  import { notify } from "../lib/toast";
  import { pipelines } from "../lib/stores";
  import { authHeaders } from "../lib/auth";
  import { getSodpClient } from "../lib/sodp";
  import StatusBadge from "../components/StatusBadge.svelte";
  import Skeleton from "../components/Skeleton.svelte";
  import type { Pipeline, Run } from "../lib/types";

  // Shape of the dashboard.{org} state key the bridge maintains. Mirrors
  // the field names emitted by pkg/sodp/bridge.go:recomputeDashboard.
  interface DashboardSnapshot {
    updated_at: string;
    runs_today: number;
    runs_yesterday: number;
    runs_running: number;
    running_run_ids: string[];
    runs_24h_total: number;
    runs_24h_success: number;
    runs_24h_failed: number;
    success_rate_24h: number;
    recent_runs: Array<{
      run_id: string;
      pipeline_id: string;
      status: string;
      started_at: string;
      finished_at: string | null;
    }>;
    top_failing: Array<{ pipeline_id: string; fail_count: number }>;
    trends: Array<{ date: string; success: number; failed: number; total: number }>;
  }

  let recentRuns: { pipeline: Pipeline; run: Run }[] = [];
  let loading = true;
  let unsubDashboard: (() => void) | null = null;

  // Stats — populated reactively from the SODP-watched dashboard snapshot
  let totalPipelines = 0;
  let activePipelines = 0;
  let pausedPipelines = 0;
  let runsToday = 0;
  let runsYesterday = 0;
  let successRate = 100;
  let failedLast24h = 0;
  let currentlyRunning = 0;

  // Onboarding step tracking
  let totalConnections = 0;
  let totalRuns = 0;
  $: onboardingSteps = [
    { label: "Connect a Data Source", done: totalConnections > 0, href: "#/connections" },
    { label: "Build a Pipeline", done: totalPipelines > 0, href: "#/pipelines" },
    { label: "Run your Pipeline", done: totalRuns > 0, href: "#/pipelines" },
  ];
  $: onboardingDone = onboardingSteps.filter(s => s.done).length;
  $: onboardingPct = Math.round((onboardingDone / onboardingSteps.length) * 100);
  $: onboardingComplete = onboardingDone === onboardingSteps.length;

  // Scheduler
  let nextScheduled: { name: string; next: string }[] = [];

  // Failed pipelines needing attention
  let failedPipelines: { pipeline: Pipeline; run: Run }[] = [];

  // Trends (7-day)
  let trends: { date: string; success: number; failed: number; total: number }[] = [];
  let topFailing: { pipeline_id: string; name: string; fail_count: number }[] = [];
  let hoveredDay: number = -1;
  $: trendMax = Math.max(...(trends.length ? trends.map(t => t.total) : [1]), 1);

  // Map of pipeline_id → Pipeline metadata, populated from REST and used to
  // attach pipeline display info to entries in the SODP snapshot's recent_runs.
  let pipelineMap: Map<string, Pipeline> = new Map();

  // applySnapshot is called every time the bridge writes a new
  // dashboard.{org} value. It pulls the aggregates and the recent_runs list
  // from the snapshot and updates the local state. Counters always reflect
  // the server's current view — there's no client-side derivation.
  function applySnapshot(snap: DashboardSnapshot | null) {
    if (!snap) return;

    runsToday        = snap.runs_today        ?? 0;
    runsYesterday    = snap.runs_yesterday    ?? 0;
    successRate      = snap.success_rate_24h  ?? 100;
    failedLast24h    = snap.runs_24h_failed   ?? 0;
    currentlyRunning = snap.runs_running      ?? 0;
    trends           = snap.trends            ?? [];
    topFailing       = (snap.top_failing ?? []).map(t => ({
      pipeline_id: t.pipeline_id,
      name: pipelineMap.get(t.pipeline_id)?.name ?? t.pipeline_id,
      fail_count: t.fail_count,
    }));

    // Stitch the snapshot's recent_runs (which carries IDs) with the
    // pipeline metadata loaded via REST so the UI gets names and tags.
    recentRuns = (snap.recent_runs ?? []).map(r => ({
      pipeline: pipelineMap.get(r.pipeline_id) ?? ({ id: r.pipeline_id, name: r.pipeline_id } as Pipeline),
      run: {
        id: r.run_id,
        pipeline_id: r.pipeline_id,
        status: r.status as any,
        started_at: r.started_at,
        finished_at: r.finished_at,
      } as Run,
    }));

    // Onboarding: "you've run a pipeline" only needs to be true once
    if (recentRuns.length > 0) totalRuns = Math.max(totalRuns, recentRuns.length);

    // Failed pipelines list for the "Needs attention" widget
    const seenFailed = new Set<string>();
    const failed: { pipeline: Pipeline; run: Run }[] = [];
    for (const r of recentRuns) {
      if (r.run.status === "failed" && !seenFailed.has(r.pipeline.id)) {
        seenFailed.add(r.pipeline.id);
        failed.push(r);
      }
    }
    failedPipelines = failed;
  }

  // loadStaticData fetches the data the SODP snapshot doesn't carry: the
  // pipeline list (for names/tags/enabled state), scheduler info, and the
  // connection count for the onboarding widget. These don't need realtime
  // updates — pipelines.summary changes are infrequent compared to runs.
  async function loadStaticData() {
    try {
      const [pipesRes, schedRes, connRes] = await Promise.all([
        fetch("/api/pipelines/summary", {
          headers: { ...authHeaders(), "X-Workspace-ID": localStorage.getItem("brokoli-workspace") || "default" },
        }),
        fetch("/api/scheduler/status", { headers: authHeaders() }),
        fetch("/api/connections", { headers: authHeaders() }),
      ]);

      if (pipesRes.ok) {
        const pipelineList: Pipeline[] = await pipesRes.json();
        pipelines.set(pipelineList);
        pipelineMap = new Map(pipelineList.map(p => [p.id, p]));
        totalPipelines = pipelineList.length;
        activePipelines = pipelineList.filter(p => p.enabled).length;
        pausedPipelines = totalPipelines - activePipelines;
      }

      if (schedRes.ok) {
        const schedData = await schedRes.json();
        nextScheduled = schedData
          .filter((s: any) => s.next_run)
          .sort((a: any, b: any) => a.next_run.localeCompare(b.next_run))
          .slice(0, 5)
          .map((s: any) => ({ name: s.pipeline_name, next: s.next_run }));
      }

      if (connRes.ok) {
        const connData = await connRes.json();
        totalConnections = Array.isArray(connData) ? connData.length : 0;
      }
    } catch {
      notify.error("Failed to load dashboard");
    }
  }

  onMount(async () => {
    await loadStaticData();
    loading = false;

    // Subscribe to the SODP-maintained dashboard snapshot. Every change to
    // any run state triggers a recompute on the server side and a delta on
    // this watch. No event-stream, no client-side aggregation, no
    // reconciliation — the value we receive IS the current state.
    //
    // The first callback fires with `null` if the key hasn't been written yet
    // (no runs ever) — applySnapshot handles that case as a no-op.
    const client = getSodpClient();
    unsubDashboard = client.watch<DashboardSnapshot>("dashboard.default", (value) => {
      applySnapshot(value);
    });
  });

  onDestroy(() => { unsubDashboard?.(); });

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

  // Clock
  let localTime = "";
  let serverTz = Intl.DateTimeFormat().resolvedOptions().timeZone;

  function updateClock() {
    const now = new Date();
    localTime = now.toLocaleTimeString("en-US", { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false, timeZoneName: "short" });
  }
  updateClock();
  const clockInterval = setInterval(updateClock, 1000);
  onDestroy(() => clearInterval(clockInterval));
</script>

<div class="dashboard animate-in">
  <header class="page-header">
    <div class="header-left">
      <h1>Dashboard</h1>
      <span class="page-sub">Last 24 hours overview</span>
    </div>
    <div class="header-clock">
      <span class="clock-time">{localTime}</span>
      <span class="clock-tz">{serverTz}</span>
    </div>
  </header>

  {#if loading}
    <div class="skeleton-grid">
      {#each Array(5) as _}
        <Skeleton variant="card" height="80px" />
      {/each}
    </div>
    <div class="skeleton-grid three" style="margin-top: 16px">
      {#each Array(3) as _}
        <Skeleton variant="card" height="200px" />
      {/each}
    </div>
  {:else if !onboardingComplete}
    <!-- Welcome hero for new users -->
    <div class="welcome-hero">
      <div class="welcome-icon">
        <svg width="56" height="56" viewBox="0 0 32 32" fill="none">
          <path d="M16 19v9" stroke="var(--accent)" stroke-width="2.5" stroke-linecap="round"/>
          <circle cx="16" cy="11" r="4.5" fill="var(--accent)"/>
          <circle cx="9" cy="7" r="3.5" fill="#16a34a"/>
          <circle cx="23" cy="7" r="3.5" fill="#16a34a"/>
          <circle cx="6" cy="2" r="2.5" fill="#22c55e"/>
          <circle cx="16" cy="2" r="3" fill="#22c55e"/>
          <circle cx="26" cy="2" r="2.5" fill="#22c55e"/>
        </svg>
      </div>
      <h2 class="welcome-title">Let's build your first pipeline</h2>
      <p class="welcome-sub">Brokoli lets you build, schedule, and monitor data pipelines visually. Follow the steps below to get running in minutes.</p>

      <!-- Progress bar -->
      <div class="onboarding-progress">
        <div class="progress-header">
          <span class="progress-label">{onboardingDone} of {onboardingSteps.length} steps complete</span>
          <span class="progress-pct">{onboardingPct}%</span>
        </div>
        <div class="progress-track">
          <div class="progress-fill" style="width: {onboardingPct}%"></div>
        </div>
      </div>

      <div class="quick-start-grid">
        {#each onboardingSteps as step, i}
          <a href={step.href} class="quick-card" class:done={step.done}>
            <div class="qc-step" class:done={step.done}>
              {#if step.done}
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12"/></svg>
              {:else}
                {i + 1}
              {/if}
            </div>
            <div class="qc-icon">
              {#if i === 0}
                <svg width="28" height="28" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M12 2v4M12 18v4M4.93 4.93l2.83 2.83M16.24 16.24l2.83 2.83M2 12h4M18 12h4M4.93 19.07l2.83-2.83M16.24 7.76l2.83-2.83"/></svg>
              {:else if i === 1}
                <svg width="28" height="28" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="3" width="6" height="6" rx="1"/><rect x="15" y="3" width="6" height="6" rx="1"/><rect x="9" y="15" width="6" height="6" rx="1"/><path d="M6 9v3a3 3 0 003 3h0M18 9v3a3 3 0 01-3 3h0"/></svg>
              {:else}
                <svg width="28" height="28" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><polygon points="5 3 19 12 5 21 5 3"/></svg>
              {/if}
            </div>
            <span class="qc-title">{step.label}</span>
            <span class="qc-desc">
              {#if i === 0}PostgreSQL, MySQL, APIs, CSV files, and more
              {:else if i === 1}Drag-and-drop nodes to design your data flow
              {:else}Execute instantly or set a cron schedule
              {/if}
            </span>
          </a>
        {/each}
      </div>

      <div class="quick-alt">
        <span class="quick-alt-text">Or start from a template:</span>
        <a href="#/pipelines" class="quick-alt-link">Hello World</a>
        <span class="quick-alt-sep">&middot;</span>
        <a href="#/pipelines" class="quick-alt-link">API Fetch</a>
        <span class="quick-alt-sep">&middot;</span>
        <a href="#/pipelines" class="quick-alt-link">Data Quality Check</a>
      </div>
    </div>
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

    <!-- Row 2: 3-column overview -->
    <div class="overview-grid">
      <!-- Trend -->
      {#if trends.length > 0}
        <div class="overview-card trend-card">
          <div class="trend-header">
            <h2 class="section-title">7-Day Trend</h2>
            <div class="trend-legend">
              <span class="legend-item"><span class="legend-dot success"></span> success</span>
              <span class="legend-item"><span class="legend-dot failed"></span> failed</span>
            </div>
          </div>

          <!-- Hovered day tooltip -->
          {#if hoveredDay >= 0 && trends[hoveredDay]}
            <div class="trend-tooltip">
              <span class="tt-date">{trends[hoveredDay].date}</span>
              <span class="tt-stat"><span class="tt-dot success"></span>{trends[hoveredDay].success} succeeded</span>
              <span class="tt-stat"><span class="tt-dot failed"></span>{trends[hoveredDay].failed} failed</span>
              <span class="tt-stat tt-total">{trends[hoveredDay].total} total</span>
            </div>
          {/if}

          <!-- Interactive bar chart -->
          <!-- svelte-ignore a11y_no_static_element_interactions -->
          <div class="trend-chart" on:mouseleave={() => hoveredDay = -1}>
            {#each trends as day, i}
              {@const successH = day.total > 0 ? Math.max(6, Math.round((day.success / trendMax) * 120)) : 0}
              {@const failedH = day.total > 0 ? Math.max(day.failed > 0 ? 6 : 0, Math.round((day.failed / trendMax) * 120)) : 0}
              <!-- svelte-ignore a11y_no_static_element_interactions -->
              <div
                class="trend-col"
                class:hovered={hoveredDay === i}
                on:mouseenter={() => hoveredDay = i}
              >
                <div class="trend-col-bars">
                  {#if day.total > 0}
                    <div class="t-bar success" style="height: {successH}px"></div>
                    {#if day.failed > 0}
                      <div class="t-bar failed" style="height: {failedH}px"></div>
                    {/if}
                  {:else}
                    <div class="t-bar empty" style="height: 4px"></div>
                  {/if}
                </div>
                <span class="trend-count">{day.total}</span>
                <span class="trend-date">{day.date.slice(5)}</span>
              </div>
            {/each}
          </div>
        </div>
      {/if}

      <!-- Needs Attention -->
      <div class="overview-card">
        <h2 class="section-title section-title-red">Needs Attention</h2>
        {#if failedPipelines.length === 0}
          <div class="empty-hint">All pipelines healthy</div>
        {:else}
          <div class="attention-list">
            {#each failedPipelines.slice(0, 8) as { pipeline, run }}
              <a href="#/pipelines/{pipeline.id}/runs" class="attention-item">
                <span class="attention-dot"></span>
                <span class="attention-name">{pipeline.name}</span>
                <span class="attention-time mono">{timeAgo(run.started_at)}</span>
              </a>
            {/each}
          </div>
        {/if}
      </div>

      <!-- Upcoming Scheduled -->
      <div class="overview-card">
        <h2 class="section-title">Upcoming</h2>
        {#if nextScheduled.length === 0}
          <div class="empty-hint">No scheduled pipelines</div>
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
      </div>
    </div>

    <!-- Row 3: Recent Runs + Activity Feed -->
    <div class="bottom-grid">
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

    <!-- Activity Feed: derived from the dashboard.{org} recent_runs slice -->
    <section class="section activity-section">
      <h2 class="section-title">Activity</h2>
      {#if recentRuns.length === 0}
        <div class="empty-hint">Activity will appear here as pipelines run.</div>
      {:else}
        <div class="activity-feed">
          {#each recentRuns as { pipeline, run }}
            <div class="activity-item">
              <span class="activity-dot"
                class:dot-success={run.status === "success" || run.status === "completed"}
                class:dot-failed={run.status === "failed"}
                class:dot-running={run.status === "running"}
              ></span>
              <span class="activity-text">
                <strong>{pipeline.name}</strong>
                {run.status}
              </span>
              <span class="activity-time mono">{run.started_at ? timeAgo(run.started_at) : ""}</span>
            </div>
          {/each}
        </div>
      {/if}
    </section>
    </div>
  {/if}
</div>

<style>
  .page-header { margin-bottom: var(--space-lg); display: flex; align-items: center; justify-content: space-between; }
  .header-left { display: flex; align-items: baseline; gap: 12px; }
  .page-header h1 { font-size: 1.5rem; font-weight: 600; letter-spacing: -0.02em; }
  .page-sub { font-size: 12px; color: var(--text-muted); }
  .header-clock {
    display: flex; flex-direction: column; align-items: flex-end; gap: 1px;
  }
  .clock-time {
    font-family: var(--font-mono); font-size: 18px; font-weight: 600;
    color: var(--text-primary); letter-spacing: 0.02em;
  }
  .clock-tz {
    font-size: 10px; color: var(--text-ghost); font-family: var(--font-mono);
  }

  /* Stats */
  .stats-grid {
    display: grid;
    grid-template-columns: repeat(5, 1fr);
    gap: 10px;
    margin-bottom: var(--space-lg);
  }
  .stat-card {
    background: var(--bg-secondary);
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius-xl, 14px);
    padding: 18px 20px;
    display: flex; flex-direction: column; gap: 4px;
    box-shadow: var(--shadow-card);
    transition: border-color 200ms ease, box-shadow 200ms ease;
  }
  .stat-card:hover {
    border-color: var(--border);
    box-shadow: var(--shadow-card-hover);
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

  .skeleton-grid { display: grid; grid-template-columns: repeat(5, 1fr); gap: 10px; }
  .skeleton-grid.three { grid-template-columns: repeat(3, 1fr); }
  .empty-state {
    background: var(--bg-secondary); border: 1px solid var(--border);
    border-radius: var(--radius-lg); padding: var(--space-lg);
    text-align: center; color: var(--text-secondary);
  }
  .empty-state.small { padding: var(--space-md); }
  .hint { color: var(--text-muted); font-size: 12px; }

  /* Recent Runs table */
  .runs-table {
    background: var(--bg-secondary); border: 1px solid var(--border-subtle);
    border-radius: var(--radius-xl, 14px); overflow: hidden;
    box-shadow: var(--shadow-card);
  }
  .table-header, .table-row {
    display: grid;
    grid-template-columns: 1fr 90px 70px 80px;
    padding: 8px 14px; align-items: center;
  }
  .table-header {
    background: transparent;
    font-size: 11px; color: var(--text-muted);
    text-transform: uppercase; letter-spacing: 0.06em; font-weight: 600;
    border-bottom: 2px solid var(--border-subtle);
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

  /* Bottom grid: runs + activity */
  .bottom-grid { display: grid; grid-template-columns: 1fr 320px; gap: 16px; align-items: start; }
  .activity-section .section-title { margin-bottom: 8px; }
  .activity-feed {
    background: var(--bg-secondary); border: 1px solid var(--border-subtle);
    border-radius: var(--radius-xl, 14px); overflow: hidden; box-shadow: var(--shadow-card);
  }
  .activity-item {
    display: flex; align-items: center; gap: 8px;
    padding: 9px 14px; border-bottom: 1px solid var(--border-subtle);
    font-size: 12px;
  }
  .activity-item:last-child { border-bottom: none; }
  .activity-dot {
    width: 6px; height: 6px; border-radius: 50%; flex-shrink: 0;
    background: var(--text-dim);
  }
  .activity-dot.dot-success { background: var(--success); }
  .activity-dot.dot-failed { background: var(--failed); }
  .activity-dot.dot-running { background: var(--running); animation: pulse-run 1s ease-in-out infinite; }
  .activity-text { flex: 1; color: var(--text-secondary); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .activity-text strong { font-weight: 600; color: var(--text-primary); }
  .activity-time { flex-shrink: 0; font-size: 10px; }
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

  /* ── Overview Grid ── */
  .overview-grid {
    display: grid;
    grid-template-columns: 1fr 1fr 1fr;
    gap: 10px;
    margin-bottom: var(--space-lg);
  }
  .overview-grid { align-items: stretch; }
  .overview-card {
    background: var(--bg-secondary); border: 1px solid var(--border-subtle);
    border-radius: var(--radius-xl, 14px); padding: var(--space-lg);
    display: flex; flex-direction: column;
    box-shadow: var(--shadow-card);
    transition: border-color 200ms ease;
  }
  .overview-card:hover { border-color: var(--border); }
  .overview-card .section-title { margin-bottom: var(--space-sm); }
  .empty-hint { font-size: 12px; color: var(--text-dim); padding: 12px 0; }

  /* ── 7-Day Trend Chart ── */
  .trend-card { overflow: hidden; }
  .trend-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 4px; }
  .trend-legend { display: flex; gap: 12px; }
  .legend-item { font-size: 10px; color: var(--text-dim); display: flex; align-items: center; gap: 4px; }
  .legend-dot { width: 8px; height: 3px; border-radius: 2px; }
  .legend-dot.success { background: #22c55e; }
  .legend-dot.failed { background: #ef4444; }

  .trend-tooltip {
    display: flex; align-items: center; gap: 12px;
    padding: 6px 10px; margin-bottom: 6px;
    background: var(--bg-tertiary); border-radius: 6px;
    font-size: 11px;
  }
  .tt-date { font-weight: 600; color: var(--text-primary); font-family: var(--font-mono); }
  .tt-stat { color: var(--text-muted); display: flex; align-items: center; gap: 4px; }
  .tt-total { color: var(--text-dim); margin-left: auto; }
  .tt-dot { width: 6px; height: 6px; border-radius: 50%; flex-shrink: 0; }
  .tt-dot.success { background: #22c55e; }
  .tt-dot.failed { background: #ef4444; }

  .trend-chart {
    flex: 1; min-height: 0;
    display: flex; gap: 3px;
    align-items: flex-end;
    padding-top: 8px;
  }
  .trend-col {
    flex: 1; display: flex; flex-direction: column;
    align-items: center; gap: 4px;
    cursor: pointer; padding: 6px 2px;
    border-radius: 6px;
    transition: background 100ms ease;
  }
  .trend-col:hover, .trend-col.hovered { background: var(--bg-tertiary); }
  .trend-col-bars {
    width: 80%;
    display: flex; flex-direction: column-reverse;
    align-items: stretch; gap: 2px;
  }
  .t-bar {
    border-radius: 4px 4px 1px 1px;
    transition: height 300ms ease;
  }
  .t-bar.success { background: #22c55e; }
  .t-bar.failed { background: #ef4444; }
  .t-bar.empty { background: var(--border-subtle); }
  .trend-count {
    font-size: 10px; font-weight: 600; color: var(--text-muted);
    font-family: var(--font-mono);
  }
  .trend-date {
    font-size: 9px; color: var(--text-ghost);
    font-family: var(--font-mono);
  }

  /* Welcome Hero */
  .welcome-hero {
    display: flex; flex-direction: column; align-items: center;
    text-align: center; padding: 48px 24px 40px;
    background: radial-gradient(ellipse at 50% 0%, rgba(13, 148, 136, 0.08) 0%, transparent 60%);
    border-radius: var(--radius-xl, 14px);
    margin: -8px -8px 0;
  }
  .welcome-icon {
    margin-bottom: 24px;
    filter: drop-shadow(0 4px 12px rgba(13, 148, 136, 0.25));
  }
  .welcome-title {
    font-size: 1.75rem; font-weight: 700; letter-spacing: -0.03em;
    margin-bottom: 10px;
    background: linear-gradient(135deg, var(--text-primary) 0%, var(--text-secondary) 100%);
    -webkit-background-clip: text; -webkit-text-fill-color: transparent;
    background-clip: text;
  }
  .welcome-sub {
    font-size: 14px; color: var(--text-muted); max-width: 480px;
    margin-bottom: 40px; line-height: 1.7;
  }
  .quick-start-grid {
    display: grid; grid-template-columns: repeat(3, 1fr);
    gap: 16px; width: 100%; max-width: 780px;
  }
  .quick-card {
    position: relative;
    display: flex; flex-direction: column; align-items: center;
    gap: 12px; padding: 36px 24px 28px;
    background: var(--bg-secondary);
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius-xl, 14px);
    text-decoration: none; color: inherit;
    transition: all 250ms cubic-bezier(0.16, 1, 0.3, 1);
    box-shadow: var(--shadow-card);
  }
  .quick-card:hover {
    border-color: var(--accent);
    background: linear-gradient(135deg, var(--accent-glow) 0%, var(--bg-secondary) 100%);
    transform: translateY(-4px);
    box-shadow: var(--shadow-card-hover), 0 0 20px var(--accent-glow);
  }
  .qc-step {
    position: absolute; top: -13px; left: 50%; transform: translateX(-50%);
    width: 26px; height: 26px; border-radius: 50%;
    background: linear-gradient(135deg, var(--accent), var(--accent-hover));
    color: white;
    font-size: 12px; font-weight: 700;
    display: flex; align-items: center; justify-content: center;
    box-shadow: 0 2px 10px rgba(13, 148, 136, 0.4);
  }
  .qc-step.done {
    background: linear-gradient(135deg, #22c55e, #16a34a);
    box-shadow: 0 2px 10px rgba(34, 197, 94, 0.4);
  }
  .quick-card.done {
    border-color: rgba(34, 197, 94, 0.3);
    opacity: 0.7;
  }
  .quick-card.done .qc-title { text-decoration: line-through; color: var(--text-muted); }
  .qc-icon { color: var(--accent); opacity: 0.85; }
  .quick-card:hover .qc-icon { opacity: 1; }
  .qc-title { font-size: 14px; font-weight: 600; }
  .qc-desc { font-size: 11.5px; color: var(--text-muted); line-height: 1.5; }

  /* Onboarding progress bar */
  .onboarding-progress {
    width: 100%; max-width: 420px; margin-bottom: 32px;
  }
  .progress-header {
    display: flex; justify-content: space-between; align-items: center;
    margin-bottom: 8px;
  }
  .progress-label { font-size: 12px; font-weight: 500; color: var(--text-secondary); }
  .progress-pct { font-size: 12px; font-weight: 700; color: var(--accent); font-family: var(--font-mono); }
  .progress-track {
    height: 6px; border-radius: 3px;
    background: var(--bg-tertiary);
    overflow: hidden;
  }
  .progress-fill {
    height: 100%; border-radius: 3px;
    background: linear-gradient(90deg, var(--accent), #22c55e);
    transition: width 500ms cubic-bezier(0.16, 1, 0.3, 1);
  }
  .quick-alt {
    margin-top: 32px; display: flex; align-items: center; gap: 8px;
    font-size: 12px;
    padding: 10px 20px;
    background: var(--bg-secondary);
    border: 1px solid var(--border-subtle);
    border-radius: 999px;
  }
  .quick-alt-text { color: var(--text-dim); }
  .quick-alt-link {
    color: var(--accent); text-decoration: none; font-weight: 500;
    transition: color 150ms ease;
  }
  .quick-alt-link:hover { color: var(--accent-hover); }
  .quick-alt-sep { color: var(--text-ghost); }

  @media (max-width: 768px) {
    .stats-grid { grid-template-columns: repeat(2, 1fr); }
    .overview-grid { grid-template-columns: 1fr; }
    .bottom-grid { grid-template-columns: 1fr; }
    .main-grid { grid-template-columns: 1fr; }
    .run-row { grid-template-columns: 1fr 70px 60px; }
    .page-header h1 { font-size: 1.2rem; }
    .quick-start-grid { grid-template-columns: 1fr; }
  }
  @media (max-width: 1100px) and (min-width: 769px) {
    .overview-grid { grid-template-columns: 1fr 1fr; }
  }
</style>
