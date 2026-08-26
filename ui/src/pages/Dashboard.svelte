<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { api } from "../lib/api";
  import { notify } from "../lib/toast";
  import { pipelines } from "../lib/stores";
  import { authHeaders, dashboardKey } from "../lib/auth";
  import { workspaceHeaders } from "../lib/workspace";
  import { getSodpClient } from "../lib/sodp";
  import PageHeader from "../components/PageHeader.svelte";
  import Skeleton from "../components/Skeleton.svelte";
  import NeedsAttention from "../components/dashboard/NeedsAttention.svelte";
  import KpiStrip from "../components/dashboard/KpiStrip.svelte";
  import RunGroups from "../components/dashboard/RunGroups.svelte";
  import TrendSparkline from "../components/dashboard/TrendSparkline.svelte";
  import AlertDrawer from "../components/dashboard/AlertDrawer.svelte";
  import Panel from "../components/dashboard/Panel.svelte";
  import PipelineHealth from "../components/dashboard/PipelineHealth.svelte";
  import ActivityHeatmap from "../components/dashboard/ActivityHeatmap.svelte";
  import DeadLetterPanel from "../components/dashboard/DeadLetterPanel.svelte";
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
  let loadError = "";
  let unsubDashboard: (() => void) | null = null;
  // Debounce handle for the snapshot-triggered refetch of /api/dashboard.
  let statsReloadTimer: ReturnType<typeof setTimeout> | null = null;

  // Per-pipeline 24h rollup from /api/dashboard. Counts are database-backed,
  // so a pipeline run 10,000 times reports 10,000 — not the size of the
  // recent_runs sample. Consumed by the grouped runs list.
  let pipelineRollups: Array<{
    pipeline_id: string;
    name: string;
    total: number;
    success: number;
    failed: number;
    running: number;
    last_status?: string;
    last_started_at?: string;
  }> = [];

  // Stats — populated reactively from the SODP-watched dashboard snapshot
  let totalPipelines = 0;
  let activePipelines = 0;
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
  $: onboardingDone = onboardingSteps.filter((s) => s.done).length;
  $: onboardingPct = Math.round((onboardingDone / onboardingSteps.length) * 100);
  $: onboardingComplete = onboardingDone === onboardingSteps.length;

  // Scheduler
  let nextScheduled: { name: string; next: string }[] = [];
  // Every scheduled pipeline by name, not just the five shown in the rail —
  // the fleet table needs a next-run for any row, not the soonest handful.
  let nextRunByName: Record<string, string> = {};

  // Full pipeline list for the fleet table. pipelineMap is keyed for lookup;
  // this keeps the ordered array the table iterates.
  let allPipelines: Pipeline[] = [];

  // Child component handles, so a run triggered anywhere on the page
  // refreshes the panels that fetch independently of /api/dashboard.
  let heatmap: ActivityHeatmap;
  let deadLetters: DeadLetterPanel;

  function refreshAll() {
    loadDashboardStats();
    heatmap?.reload();
    deadLetters?.reload();
  }

  // Failed pipelines needing attention
  let failedPipelines: { pipeline: Pipeline; run: Run }[] = [];

  // Trends (7-day)
  let trends: { date: string; success: number; failed: number; total: number }[] = [];
  let topFailing: { pipeline_id: string; name: string; fail_count: number }[] = [];

  // Map of pipeline_id → Pipeline metadata, populated from REST and used to
  // attach pipeline display info to entries in the SODP snapshot's recent_runs.
  let pipelineMap: Map<string, Pipeline> = new Map();

  // applyLiveSnapshot takes only what the SODP snapshot is actually
  // authoritative for: runs in flight. Eviction removes *completed* runs
  // from state, never running ones, so this figure is accurate and instant.
  //
  // Everything historical — today/24h counters, the trend, top failing,
  // recent runs — deliberately does NOT come from here. Those fields exist
  // on the snapshot but decay to zero as runs age past the eviction TTL
  // (Tnsor-Labs/brokoli#78); loadDashboardStats reads them from the
  // database instead.
  function applyLiveSnapshot(snap: DashboardSnapshot | null) {
    if (!snap) return;
    currentlyRunning = snap.runs_running ?? 0;
  }

  // recomputeFailedPipelines derives the "needs attention" list from the
  // most recent run per pipeline, one entry per pipeline.
  function recomputeFailedPipelines() {
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
  // loadDashboardStats fetches the database-backed aggregates from
  // GET /api/dashboard. This — not the SODP snapshot — is the source of
  // truth for every historical figure on this page.
  //
  // The snapshot's counters are computed by scanning live state, and
  // completed runs are evicted from that state after ~30 minutes, so its
  // "24h", "today" and "7-day" fields silently decay to zero (and to a
  // misleading 100% success rate) while the run history they claim to
  // summarise still exists. See Tnsor-Labs/brokoli#78 and the doc comment
  // on recomputeDashboard in pkg/sodp/bridge.go.
  async function loadDashboardStats(): Promise<boolean> {
    try {
      const res = await fetch("/api/dashboard", {
        headers: { ...authHeaders(), ...workspaceHeaders() },
      });
      if (!res.ok) return false;
      const d = await res.json();

      runsToday = d.runs_today ?? 0;
      runsYesterday = d.runs_yesterday ?? 0;
      successRate = d.success_rate_24h ?? 100;
      failedLast24h = d.runs_24h_failed ?? 0;
      trends = d.trends ?? [];
      topFailing = (d.top_failing ?? []).map((t: any) => ({
        pipeline_id: t.pipeline_id,
        name: t.name || pipelineMap.get(t.pipeline_id)?.name || t.pipeline_id,
        fail_count: t.fail_count,
      }));
      pipelineRollups = d.pipeline_rollups ?? [];

      // recent_runs from REST carries pipeline_name and error directly, so
      // no stitching against pipelineMap is needed — and unlike the map, it
      // still names a pipeline that has since been deleted.
      recentRuns = (d.recent_runs ?? []).map((r: any) => ({
        pipeline:
          pipelineMap.get(r.pipeline_id) ??
          ({ id: r.pipeline_id, name: r.pipeline_name || r.pipeline_id } as Pipeline),
        run: {
          id: r.run_id,
          pipeline_id: r.pipeline_id,
          status: r.status,
          error: r.error,
          started_at: r.started_at,
          finished_at: r.finished_at,
        } as Run,
      }));
      if (recentRuns.length > 0) totalRuns = Math.max(totalRuns, recentRuns.length);
      recomputeFailedPipelines();
      return true;
    } catch {
      // Leave the previous values in place rather than flashing zeros.
      return false;
    }
  }

  async function loadStaticData(): Promise<boolean> {
    try {
      const [pipesRes, schedRes, connRes] = await Promise.all([
        fetch("/api/pipelines/summary", {
          headers: { ...authHeaders(), ...workspaceHeaders() },
        }),
        fetch("/api/scheduler/status", { headers: authHeaders() }),
        fetch("/api/connections", { headers: authHeaders() }),
      ]);

      if (pipesRes.ok) {
        const pipelineList: Pipeline[] = await pipesRes.json();
        pipelines.set(pipelineList);
        allPipelines = pipelineList;
        pipelineMap = new Map(pipelineList.map((p) => [p.id, p]));
        totalPipelines = pipelineList.length;
        activePipelines = pipelineList.filter((p) => p.enabled).length;
      }

      if (schedRes.ok) {
        const schedData = await schedRes.json();
        const scheduled = schedData
          .filter((s: any) => s.next_run)
          .sort((a: any, b: any) => a.next_run.localeCompare(b.next_run));
        nextScheduled = scheduled
          .slice(0, 50)
          .map((s: any) => ({ name: s.pipeline_name, next: s.next_run }));
        nextRunByName = Object.fromEntries(
          scheduled.map((s: any) => [s.pipeline_name, s.next_run]),
        );
      }

      if (connRes.ok) {
        const connData = await connRes.json();
        totalConnections = Array.isArray(connData) ? connData.length : 0;
      }
      return pipesRes.ok && schedRes.ok && connRes.ok;
    } catch {
      notify.error("Failed to load dashboard");
      return false;
    }
  }

  async function loadDashboard() {
    loading = true;
    loadError = "";
    const staticLoaded = await loadStaticData();
    const statsLoaded = await loadDashboardStats();
    if (!staticLoaded || !statsLoaded) loadError = "Some organization data could not be retrieved.";
    loading = false;
  }

  onMount(async () => {
    await loadDashboard();

    // The snapshot is a tripwire, not a data source. Every run-state change
    // rewrites dashboard.{org}, which tells us something happened — we then
    // refetch the database-backed figures. Only the live running count is
    // read straight off the snapshot, because that is the one thing it is
    // authoritative for.
    //
    // This mirrors ui/src/pages/Pipelines.svelte, which already uses the
    // same key the same way for the same reason. Refetching on every change
    // is more requests than reading the snapshot's counters, but those
    // counters are wrong for anything older than the eviction TTL
    // (Tnsor-Labs/brokoli#78) and /api/dashboard is cheap.
    const client = getSodpClient();
    unsubDashboard = client.watch<DashboardSnapshot>(dashboardKey(), (value) => {
      applyLiveSnapshot(value);

      // Debounce: a single run emits several state writes (run + per-node),
      // and each one lands here. Collapse a burst into one refetch.
      if (statsReloadTimer) clearTimeout(statsReloadTimer);
      statsReloadTimer = setTimeout(() => {
        refreshAll();
        statsReloadTimer = null;
      }, 200);
    });
  });

  onDestroy(() => {
    unsubDashboard?.();
    if (statsReloadTimer) clearTimeout(statsReloadTimer);
  });

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

  // Clock
  let localTime = "";
  let serverTz = Intl.DateTimeFormat().resolvedOptions().timeZone;

  function updateClock() {
    const now = new Date();
    localTime = now.toLocaleTimeString("en-US", {
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
      hour12: false,
      timeZoneName: "short",
    });
  }
  updateClock();
  const clockInterval = setInterval(updateClock, 1000);
  onDestroy(() => clearInterval(clockInterval));
</script>

<div class="dashboard animate-in">
  <PageHeader
    brandIcon="dashboard"
    kicker="Organization control center"
    title="Dashboard"
    description="Live operational posture with a 24-hour reliability window."
  >
    <!-- The clock used to be the largest, brightest element on the page,
         outcompeting the failure count for attention while carrying no
         operational value. It stays — it's useful for reading timestamps
         against — but at the weight of the metadata it is. -->
    <svelte:fragment slot="extra-action">
      <span class="clock" title={serverTz}>{localTime}</span>
      <AlertDrawer />
    </svelte:fragment>
  </PageHeader>

  {#if loading}
    <div class="loading-copy" aria-live="polite">
      <strong>Assembling organization health</strong><span
        >Loading pipelines, schedules, run outcomes, and intervention queues.</span
      >
    </div>
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
  {:else if loadError}
    <div class="load-error" role="alert">
      <span>Control center unavailable</span>
      <h2>We could not assemble a trustworthy organization view</h2>
      <p>{loadError} Existing pipelines and runs have not been changed.</p>
      <button on:click={loadDashboard}>Try again</button>
    </div>
  {:else if !onboardingComplete}
    <!-- Welcome hero for new users -->
    <div class="welcome-hero">
      <div class="welcome-icon">
        <img src="/favicon.svg" width="40" height="49" alt="Brokoli" />
      </div>
      <h2 class="welcome-title">Let's build your first pipeline</h2>
      <p class="welcome-sub">
        Brokoli lets you build, schedule, and monitor data pipelines visually. Follow the steps
        below to get running in minutes.
      </p>

      <!-- Progress bar -->
      <div class="onboarding-progress">
        <div class="progress-header">
          <span class="progress-label"
            >{onboardingDone} of {onboardingSteps.length} steps complete</span
          >
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
                <svg
                  width="14"
                  height="14"
                  viewBox="0 0 24 24"
                  fill="none"
                  stroke="currentColor"
                  stroke-width="3"
                  stroke-linecap="round"
                  stroke-linejoin="round"><polyline points="20 6 9 17 4 12" /></svg
                >
              {:else}
                {i + 1}
              {/if}
            </div>
            <div class="qc-icon">
              {#if i === 0}
                <svg
                  width="28"
                  height="28"
                  viewBox="0 0 24 24"
                  fill="none"
                  stroke="currentColor"
                  stroke-width="1.5"
                  stroke-linecap="round"
                  stroke-linejoin="round"
                  ><path
                    d="M12 2v4M12 18v4M4.93 4.93l2.83 2.83M16.24 16.24l2.83 2.83M2 12h4M18 12h4M4.93 19.07l2.83-2.83M16.24 7.76l2.83-2.83"
                  /></svg
                >
              {:else if i === 1}
                <svg
                  width="28"
                  height="28"
                  viewBox="0 0 24 24"
                  fill="none"
                  stroke="currentColor"
                  stroke-width="1.5"
                  stroke-linecap="round"
                  stroke-linejoin="round"
                  ><rect x="3" y="3" width="6" height="6" rx="1" /><rect
                    x="15"
                    y="3"
                    width="6"
                    height="6"
                    rx="1"
                  /><rect x="9" y="15" width="6" height="6" rx="1" /><path
                    d="M6 9v3a3 3 0 003 3h0M18 9v3a3 3 0 01-3 3h0"
                  /></svg
                >
              {:else}
                <svg
                  width="28"
                  height="28"
                  viewBox="0 0 24 24"
                  fill="none"
                  stroke="currentColor"
                  stroke-width="1.5"
                  stroke-linecap="round"
                  stroke-linejoin="round"><polygon points="5 3 19 12 5 21 5 3" /></svg
                >
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
    <!-- Triage-first ordering: what is broken, then what is happening,
         then the detail. The attention band is the hero when it matters and
         collapses to a single line when it does not. -->
    <NeedsAttention items={failedPipelines} on:changed={refreshAll} />

    <KpiStrip
      failed={failedLast24h}
      running={currentlyRunning}
      {successRate}
      {runsToday}
      {runsYesterday}
      {totalPipelines}
      {activePipelines}
    >
      <TrendSparkline {trends} />
    </KpiStrip>

    <div class="main-grid">
      <section class="col">
        <!-- Consecutive runs of one pipeline collapse into a single row with
             real 24h counts, so running something 500 times doesn't bury the
             one pipeline that broke. The panel caps and scrolls on top of
             that, because expanded failure sub-rows have no fixed height. -->
        <Panel title="Runs" href="#/pipelines" maxHeight="360px" wide fill>
          <RunGroups runs={recentRuns} rollups={pipelineRollups} on:changed={refreshAll} />
        </Panel>
      </section>

      <aside class="col col-side">
        {#if topFailing.length > 0}
          <Panel title="Top failing (7d)" maxHeight="200px">
            {#each topFailing as t (t.pipeline_id)}
              <a class="mini-row" href="#/pipelines/{t.pipeline_id}/runs">
                <span class="mini-name">{t.name}</span>
                <span class="mini-count">{t.fail_count}</span>
              </a>
            {/each}
          </Panel>
        {/if}

        {#if nextScheduled.length > 0}
          <Panel title="Upcoming" maxHeight="200px">
            {#each nextScheduled as s}
              <div class="mini-row">
                <span class="mini-name">{s.name}</span>
                <span class="mini-when">{formatNextRun(s.next)}</span>
              </div>
            {/each}
          </Panel>
        {/if}

        <!-- Records the engine gave up on. These need a human, and until now
             the org-wide queue had no surface anywhere in the product.
             Wrapped so it absorbs the rail's leftover height and the rail
             ends level with the runs list beside it. The wrapper stays even
             when the queue is empty and the panel renders nothing. -->
        <div class="rail-fill">
          <DeadLetterPanel bind:this={deadLetters} fill />
        </div>
      </aside>
    </div>

    <!-- Fleet state. The panels above answer "what just happened"; this
         answers "what is the state of everything I own", which the dashboard
         never did — you had to leave for the pipelines page to find out. -->
    <div class="stack">
      <PipelineHealth
        pipelines={allPipelines}
        rollups={pipelineRollups}
        nextRuns={nextRunByName}
        on:changed={refreshAll}
      />

      <!-- Trend over a window long enough to show a pattern. The KPI strip's
           sparkline covers 7 days; recurring weekly failures and stretches
           where nothing ran at all only become visible over months. -->
      <ActivityHeatmap bind:this={heatmap} />
    </div>
  {/if}
</div>

<style>
  /* One rhythm for the whole page. The bands used to be positioned by
     ad-hoc margins on some children and nothing on others, so the attention
     band, the KPI strip and the runs list ran together into a single slab
     with no breathing room between them. */
  .dashboard {
    display: flex;
    flex-direction: column;
    gap: var(--space-lg);
    min-width: 0;
  }

  /* Metadata weight, not hero weight — see the markup comment. */
  .clock {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--text-dim);
    letter-spacing: 0.02em;
  }
  .loading-copy {
    display: flex;
    flex-direction: column;
    gap: 3px;
    padding: 2px 0;
  }
  .loading-copy strong {
    color: var(--text-primary);
    font-size: 12px;
  }
  .loading-copy span {
    color: var(--text-muted);
    font-size: 10px;
  }
  .load-error {
    display: grid;
    min-height: 360px;
    place-content: center;
    justify-items: center;
    padding: 42px 24px;
    border: 1px solid var(--border-subtle);
    border-radius: 10px;
    background:
      radial-gradient(
        circle at 50% 30%,
        color-mix(in srgb, var(--failed), transparent 92%),
        transparent 38%
      ),
      var(--bg-secondary);
    text-align: center;
    box-shadow: var(--shadow-card);
  }
  .load-error > span {
    color: var(--accent);
    font: 650 9px var(--font-mono);
    letter-spacing: 0.13em;
    text-transform: uppercase;
  }
  .load-error h2 {
    max-width: 580px;
    margin-top: 8px;
    color: var(--text-primary);
    font-size: 20px;
    letter-spacing: -0.025em;
  }
  .load-error p {
    max-width: 520px;
    margin: 8px 0 20px;
    color: var(--text-muted);
    font-size: 12px;
    line-height: 1.6;
  }
  .load-error button {
    min-height: 34px;
    padding: 0 14px;
    border: 1px solid var(--border);
    border-radius: 6px;
    color: var(--text-secondary);
    font-size: 11px;
  }
  .load-error button:hover {
    border-color: var(--accent);
    color: var(--accent);
  }

  /* Layout: runs take the width, the rail carries what isn't a duplicate
     of them. The old bottom row put Recent Runs beside an Activity feed
     rendering the same array. */
  .main-grid {
    display: grid;
    /* minmax(0, ...) not 1fr: a bare `1fr` is minmax(auto, 1fr), whose
       minimum is min-content, so any row that refuses to shrink drags the
       whole grid wider than the viewport. This is what pushed the panels
       off-screen on a phone. */
    grid-template-columns: minmax(0, 1fr) 300px;
    /* Columns end on the same line because they *stretch to each other*,
       not because the row is pinned to a number. `align-items: stretch`
       sizes the row to the taller column's content and stretches the
       shorter one to match — so with three runs the row is short, and it
       only grows when there is something to show.
       A fixed row height here reserved the space whether or not anything
       filled it, which is the empty-desktop problem.
       The cap belongs on the container: the row can grow to fill it, never
       past it, and each panel body scrolls once it does. */
    grid-template-rows: auto;
    max-height: 420px;
    gap: var(--space-md);
    align-items: stretch;
  }
  .col {
    display: flex;
    flex-direction: column;
    gap: var(--space-sm);
    min-height: 0;
  }

  /* Takes the rail's remaining height below the fixed-size panels above it. */
  .rail-fill {
    flex: 1 1 auto;
    min-height: 0;
    display: flex;
    flex-direction: column;
  }
  .col-side {
    gap: var(--space-md);
    min-height: 0;
  }
  /* Each rail panel caps itself, so the rail can no longer become taller
     than the runs list it sits next to. */

  /* Full-width sections below the grid, in decreasing urgency. */
  .stack {
    display: flex;
    flex-direction: column;
    gap: var(--space-lg);
  }

  .mini-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-sm);
    padding: 7px 12px;
    border-bottom: 1px solid var(--border-subtle);
    text-decoration: none;
    font-size: 0.75rem;
  }
  .mini-row:last-child {
    border-bottom: none;
  }
  a.mini-row:hover {
    background: var(--bg-tertiary);
  }

  .mini-name {
    color: var(--text-secondary);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .mini-count {
    font-family: var(--font-mono);
    font-size: 0.6875rem;
    font-weight: 700;
    color: var(--failed);
    flex-shrink: 0;
  }
  .mini-when {
    font-family: var(--font-mono);
    font-size: 0.6875rem;
    color: var(--text-dim);
    flex-shrink: 0;
  }

  /* Welcome Hero */
  .welcome-hero {
    display: flex;
    flex-direction: column;
    align-items: center;
    text-align: center;
    padding: 48px 24px 40px;
    background: radial-gradient(ellipse at 50% 0%, var(--surface-wash) 0%, transparent 60%);
    border-radius: var(--radius-xl, 14px);
    margin: -8px -8px 0;
  }
  .welcome-icon {
    margin-bottom: 24px;
    filter: drop-shadow(0 4px 12px rgba(13, 148, 136, 0.25));
  }
  .welcome-title {
    font-size: 1.75rem;
    font-weight: 700;
    letter-spacing: -0.03em;
    margin-bottom: 10px;
    background: linear-gradient(135deg, var(--text-primary) 0%, var(--text-secondary) 100%);
    -webkit-background-clip: text;
    -webkit-text-fill-color: transparent;
    background-clip: text;
  }
  .welcome-sub {
    font-size: 14px;
    color: var(--text-muted);
    max-width: 480px;
    margin-bottom: 40px;
    line-height: 1.7;
  }
  .quick-start-grid {
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    gap: 16px;
    width: 100%;
  }
  .quick-card {
    position: relative;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 12px;
    padding: 36px 24px 28px;
    background: var(--bg-secondary);
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius-xl, 14px);
    text-decoration: none;
    color: inherit;
    transition: all 250ms cubic-bezier(0.16, 1, 0.3, 1);
    box-shadow: var(--shadow-card);
  }
  .quick-card:hover {
    border-color: var(--accent);
    background: linear-gradient(135deg, var(--surface-wash) 0%, var(--bg-secondary) 100%);
    transform: translateY(-4px);
    box-shadow:
      var(--shadow-card-hover),
      0 0 20px var(--surface-wash);
  }
  .qc-step {
    position: absolute;
    top: -13px;
    left: 50%;
    transform: translateX(-50%);
    width: 26px;
    height: 26px;
    border-radius: 50%;
    background: var(--bg-tertiary);
    border: 1px solid var(--border-hover);
    color: var(--text-secondary);
    font-size: 12px;
    font-weight: 700;
    display: flex;
    align-items: center;
    justify-content: center;
  }
  .qc-step.done {
    background: linear-gradient(135deg, #22c55e, #16a34a);
    border-color: transparent;
    color: white;
    box-shadow: 0 2px 10px rgba(34, 197, 94, 0.4);
  }
  .quick-card.done {
    border-color: rgba(34, 197, 94, 0.3);
    opacity: 0.7;
  }
  .quick-card.done .qc-title {
    text-decoration: line-through;
    color: var(--text-muted);
  }
  .qc-icon {
    color: var(--text-secondary);
    opacity: 0.9;
  }
  .quick-card:hover .qc-icon {
    opacity: 1;
  }
  .qc-title {
    font-size: 14px;
    font-weight: 600;
  }
  .qc-desc {
    font-size: 11.5px;
    color: var(--text-muted);
    line-height: 1.5;
  }

  /* Onboarding progress bar */
  .onboarding-progress {
    width: 100%;
    max-width: 420px;
    margin-bottom: 32px;
  }
  .progress-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 8px;
  }
  .progress-label {
    font-size: 12px;
    font-weight: 500;
    color: var(--text-secondary);
  }
  .progress-pct {
    font-size: 12px;
    font-weight: 700;
    color: var(--text-secondary);
    font-family: var(--font-mono);
  }
  .progress-track {
    height: 6px;
    border-radius: 3px;
    background: var(--bg-tertiary);
    overflow: hidden;
  }
  .progress-fill {
    height: 100%;
    border-radius: 3px;
    background: linear-gradient(90deg, var(--accent), #22c55e);
    transition: width 500ms cubic-bezier(0.16, 1, 0.3, 1);
  }
  .quick-alt {
    margin-top: 32px;
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 12px;
    padding: 10px 20px;
    background: var(--bg-secondary);
    border: 1px solid var(--border-subtle);
    border-radius: 999px;
  }
  .quick-alt-text {
    color: var(--text-dim);
  }
  .quick-alt-link {
    color: var(--accent);
    text-decoration: none;
    font-weight: 500;
    transition: color 150ms ease;
  }
  .quick-alt-link:hover {
    color: var(--accent-hover);
  }
  .quick-alt-sep {
    color: var(--text-ghost);
  }

  /* The rail stacks under the runs column before the columns get too
     narrow to read — same breakpoints the page used before. */
  @media (max-width: 1100px) and (min-width: 769px) {
    .main-grid {
      grid-template-columns: minmax(0, 1fr) 260px;
    }
  }
  @media (max-width: 768px) {
    /* Stacked, so there is no second column to line up with — let each
       panel size to its own content again. */
    .main-grid {
      grid-template-columns: minmax(0, 1fr);
      grid-template-rows: auto;
      max-height: none;
    }
    .rail-fill {
      flex: 0 0 auto;
    }
    .quick-start-grid {
      grid-template-columns: 1fr;
    }
  }
</style>
