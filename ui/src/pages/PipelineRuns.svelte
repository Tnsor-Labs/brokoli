<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { icons } from "../lib/icons";
  import { api } from "../lib/api";
  import { authHeaders, dashboardKey } from "../lib/auth";
  import { notify } from "../lib/toast";
  import { getSodpClient } from "../lib/sodp";
  import StatusBadge from "../components/StatusBadge.svelte";
  import GanttChart from "../components/GanttChart.svelte";
  import LogStream from "../components/LogStream.svelte";
  import Breadcrumb from "../components/Breadcrumb.svelte";
  import Skeleton from "../components/Skeleton.svelte";
  import DataPreview from "../components/DataPreview.svelte";
  import PipelineCanvas from "../components/PipelineCanvas.svelte";
  import Pagination from "../components/Pagination.svelte";
  import type {
    Pipeline,
    Run,
    LogEntry,
    RunStatus,
    PhysicalInstance,
    PhysicalPlan,
  } from "../lib/types";

  export let params: { id?: string } = {};
  let runPage = 1;
  let runPageSize = 25;

  let pipeline: Pipeline | null = null;
  let plan: PhysicalPlan | null = null;
  let runs: Run[] = [];
  let selectedRun: Run | null = null;
  let logs: LogEntry[] = [];
  let instances: PhysicalInstance[] = [];
  let loading = true;
  let expandedRunId: string | null = null;
  let previewNodeId: string | null = null;
  let showBackfill = false;
  let historyExpanded = false;
  let backfillStart = "";
  let backfillEnd = "";
  let backfilling = false;
  let showParamsModal = false;
  let runParams: Record<string, string> = {};

  // Profile viewer
  let profileNodeId: string | null = null;
  let profileData: any = null;
  let loadingProfile = false;
  let profileRequest = 0;

  async function loadProfile(runId: string, nodeId: string) {
    const request = ++profileRequest;
    if (profileNodeId === nodeId) {
      profileNodeId = null;
      profileData = null;
      loadingProfile = false;
      return;
    }
    profileNodeId = nodeId;
    loadingProfile = true;
    let nextProfile: any = null;
    try {
      const res = await fetch(`/api/runs/${runId}/nodes/${nodeId}/profile`, {
        headers: authHeaders(),
      });
      if (res.ok) {
        nextProfile = await res.json();
      }
    } catch {}
    if (request !== profileRequest) return;
    profileData = nextProfile;
    loadingProfile = false;
  }

  // Subscriptions managed across the page lifecycle:
  //   • dashboardUnsub  — fires whenever any run state changes anywhere in
  //                       the org. Used as a tripwire to refetch the runs list
  //                       and the expanded run detail via REST.
  //   • logsUnsub       — fires whenever the expanded run's logs array changes.
  //                       Replaces the old onWSEvent log-streaming path.
  let dashboardUnsub: (() => void) | null = null;
  let logsUnsub: (() => void) | null = null;
  let runListReloadTimer: ReturnType<typeof setTimeout> | null = null;
  let relativeTimeTimer: ReturnType<typeof setInterval> | null = null;
  let now = Date.now();

  async function reloadRunList() {
    if (!params.id) return;
    try {
      runs = await api.runs.listByPipeline(params.id);
    } catch {
      /* ignore */
    }
    if (expandedRunId) {
      try {
        selectedRun = await api.runs.get(expandedRunId);
        instances = await api.runs.instances(expandedRunId);
      } catch {
        /* ignore */
      }
    }
  }

  // Watch the per-run logs key for the currently-expanded run. The bridge
  // appends every log event to runs.{id}.logs, capped at 200 entries. The
  // SODP server's diff machinery sends only the appended entry on the wire,
  // not the whole array, so this is efficient even at high log throughput.
  function subscribeLogs(runId: string) {
    logsUnsub?.();
    logsUnsub = null;
    if (!runId) return;
    const client = getSodpClient();
    logsUnsub = client.watch<any[]>(`runs.${runId}.logs`, (value) => {
      if (!value) {
        logs = [];
        return;
      }
      // Map the bridge's log entry shape to LogEntry. The bridge writes
      // {node_id, level, message, timestamp}; we need a run_id field too.
      logs = (value as any[]).map((entry) => ({
        run_id: runId,
        node_id: (entry?.node_id ?? "") as string,
        level: (entry?.level ?? "info") as any,
        message: (entry?.message ?? "") as string,
        timestamp: (entry?.timestamp ?? "") as string,
      }));
    });
  }

  onMount(async () => {
    if (!params.id) return;
    try {
      [pipeline, runs] = await Promise.all([
        api.pipelines.get(params.id),
        api.runs.listByPipeline(params.id),
      ]);
      try {
        plan = await api.pipelines.plan(params.id);
      } catch {
        // Older servers may not expose physical planning yet.
        plan = null;
      }
    } catch (e) {
      notify.error("Failed to load");
    } finally {
      loading = false;
    }

    // Tripwire on dashboard.default — when ANY run state changes in this
    // org, debounce-refetch our run list. The dashboard changes for any
    // run's start/complete/fail, which is exactly when this page's data
    // also needs refreshing.
    const client = getSodpClient();
    let firstCallback = true;
    dashboardUnsub = client.watch(dashboardKey(), () => {
      if (firstCallback) {
        firstCallback = false;
        return;
      }
      if (runListReloadTimer) clearTimeout(runListReloadTimer);
      runListReloadTimer = setTimeout(() => {
        reloadRunList();
        runListReloadTimer = null;
      }, 150);
    });
    relativeTimeTimer = setInterval(() => (now = Date.now()), 60_000);
  });

  onDestroy(() => {
    dashboardUnsub?.();
    logsUnsub?.();
    if (runListReloadTimer) clearTimeout(runListReloadTimer);
    if (relativeTimeTimer) clearInterval(relativeTimeTimer);
  });

  async function selectRun(run: Run) {
    if (expandedRunId === run.id) {
      expandedRunId = null;
      selectedRun = null;
      instances = [];
      logsUnsub?.();
      logsUnsub = null;
      logs = [];
      return;
    }
    expandedRunId = run.id;
    previewNodeId = null;
    profileNodeId = null;
    profileData = null;
    loadingProfile = false;
    profileRequest += 1;
    try {
      [selectedRun, logs, instances] = await Promise.all([
        api.runs.get(run.id),
        api.runs.getLogs(run.id),
        api.runs.instances(run.id),
      ]);
    } catch (e) {
      notify.error("Failed to load run");
    }
    // Subscribe to live log streaming for this run. The initial REST fetch
    // above gives us any historical logs from the database; the SODP watch
    // takes over from here for new entries the engine emits.
    subscribeLogs(run.id);
  }

  function addParam() {
    runParams = { ...runParams, "": "" };
  }

  function removeParam(key: string) {
    const copy = { ...runParams };
    delete copy[key];
    runParams = copy;
  }

  function updateParamKey(oldKey: string, newKey: string) {
    const entries = Object.entries(runParams);
    const updated: Record<string, string> = {};
    for (const [k, v] of entries) {
      updated[k === oldKey ? newKey : k] = v;
    }
    runParams = updated;
  }

  function openParamsModal() {
    runParams = {};
    showParamsModal = true;
  }

  async function triggerRun() {
    if (!pipeline) return;
    try {
      const params = Object.keys(runParams).length > 0 ? runParams : undefined;
      await api.runs.trigger(pipeline.id, params);
      runs = await api.runs.listByPipeline(pipeline.id);
      showParamsModal = false;
      runParams = {};
    } catch (e: any) {
      notify.error("Failed to trigger run: " + (e.message || e));
    }
  }

  async function runBackfill() {
    if (!params.id || !backfillStart || !backfillEnd) return;
    backfilling = true;
    try {
      const result = await api.runs.backfill(params.id, backfillStart, backfillEnd);
      notify.success(`Backfill started: ${result.count} runs queued`);
      showBackfill = false;
      runs = await api.runs.listByPipeline(params.id);
    } catch (e: any) {
      notify.error("Backfill failed: " + e.message);
    } finally {
      backfilling = false;
    }
  }

  async function cancelRun(runId: string) {
    try {
      await fetch(`/api/runs/${runId}/cancel`, { method: "POST", headers: authHeaders() });
      notify.success("Run cancelled");
      if (params.id) {
        runs = await api.runs.listByPipeline(params.id);
      }
    } catch {
      notify.error("Failed to cancel run");
    }
  }

  async function rerunPipeline() {
    if (!params.id) return;
    try {
      await api.runs.trigger(params.id);
      notify.success("Pipeline re-triggered");
      runs = await api.runs.listByPipeline(params.id);
    } catch {
      notify.error("Failed to re-run pipeline");
    }
  }

  async function resumeRun(runId: string) {
    try {
      await fetch(`/api/runs/${runId}/resume`, { method: "POST", headers: authHeaders() });
      if (params.id) {
        runs = await api.runs.listByPipeline(params.id);
      }
    } catch (e) {
      notify.error("Failed to resume run");
    }
  }

  function formatDuration(run: Run): string {
    if (!run.started_at || !run.finished_at) return "—";
    const ms = new Date(run.finished_at).getTime() - new Date(run.started_at).getTime();
    if (ms < 1000) return `${ms}ms`;
    return `${(ms / 1000).toFixed(1)}s`;
  }

  function formatTime(ts: string | null): string {
    if (!ts) return "—";
    return new Date(ts).toLocaleString();
  }

  function nodeStatuses(run: Run): Record<string, RunStatus> {
    const map: Record<string, RunStatus> = {};
    for (const nr of primaryNodeRuns(run)) {
      map[nr.node_id] = nr.status;
    }
    // Per-node live state comes from the dashboard tripwire above, which
    // refetches the run via REST whenever any state changes. The previous
    // liveNodeStatuses overlay is no longer needed — `selectedRun` is
    // already up to date by the time this function reads `run.node_runs`.
    return map;
  }

  function primaryNodeRuns(run: Run) {
    const latest = new Map<string, (typeof run.node_runs)[number]>();
    for (const nr of run.node_runs || []) {
      const current = latest.get(nr.node_id);
      if (!current || (nr.attempt ?? 0) >= (current.attempt ?? 0)) latest.set(nr.node_id, nr);
    }
    return [...latest.values()];
  }

  function totalRows(run: Run): number {
    return primaryNodeRuns(run).reduce((sum, nr) => sum + nr.row_count, 0);
  }

  function nodeCount(run: Run, status: RunStatus): number {
    return primaryNodeRuns(run).filter((nr) => nr.status === status).length;
  }

  function relativeTime(ts: string | null, currentTime: number): string {
    if (!ts) return "Not started";
    const seconds = Math.max(0, Math.floor((currentTime - new Date(ts).getTime()) / 1000));
    if (seconds < 60) return "just now";
    const minutes = Math.floor(seconds / 60);
    if (minutes < 60) return `${minutes}m ago`;
    const hours = Math.floor(minutes / 60);
    if (hours < 24) return `${hours}h ago`;
    const days = Math.floor(hours / 24);
    return days < 30 ? `${days}d ago` : new Date(ts).toLocaleDateString();
  }

  function historyTitle(run: Run): string {
    const parts = [run.id.slice(0, 8), run.status, formatTime(run.started_at), formatDuration(run)];
    if (run.node_runs?.length) parts.push(`${totalRows(run)} rows`);
    if (run.error) parts.push(run.error.slice(0, 50));
    return parts.join(" | ");
  }

  function formatFullTime(ts: string | null): string {
    if (!ts) return "--";
    const d = new Date(ts);
    return (
      d.toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric" }) +
      " " +
      d.toLocaleTimeString(undefined, {
        hour: "2-digit",
        minute: "2-digit",
        second: "2-digit",
        timeZoneName: "short",
      })
    );
  }

  interface DayRuns {
    date: string;
    label: string;
    runs: Run[];
    success: number;
    failed: number;
    total: number;
  }

  function groupRunsByDate(allRuns: Run[]): DayRuns[] {
    const map = new Map<string, Run[]>();
    for (const r of allRuns) {
      if (!r.started_at) continue;
      const date = r.started_at.slice(0, 10);
      if (!map.has(date)) map.set(date, []);
      map.get(date)!.push(r);
    }
    const result: DayRuns[] = [];
    for (const [date, dayRuns] of map) {
      result.push({
        date,
        label: new Date(date + "T00:00:00").toLocaleDateString(undefined, {
          month: "short",
          day: "numeric",
        }),
        runs: dayRuns.sort((a, b) => (a.started_at || "").localeCompare(b.started_at || "")),
        success: dayRuns.filter((r) => isSuccessStatus(r.status)).length,
        failed: dayRuns.filter((r) => r.status === "failed").length,
        total: dayRuns.length,
      });
    }
    return result.sort((a, b) => b.date.localeCompare(a.date)).slice(0, 14);
  }

  function isSuccessStatus(status: string): boolean {
    return status === "success" || status === "succeeded" || status === "completed";
  }

  $: runsByDate = groupRunsByDate(runs);
  $: latestRun = runs[0] || null;
  $: terminalRuns = runs.filter((run) => run.status === "success" || run.status === "failed");
  $: recentSuccessRate = terminalRuns.length
    ? Math.round(
        (terminalRuns.filter((run) => run.status === "success").length / terminalRuns.length) * 100,
      )
    : null;
</script>

<div class="runs-page animate-in">
  <header class="runs-header">
    <Breadcrumb
      items={[
        { label: "Pipelines", href: "#/pipelines" },
        { label: pipeline?.name || "Pipeline", href: `#/pipelines/${params.id}/edit` },
        { label: "Runs" },
      ]}
    />
    <div class="header-main">
      <div class="header-copy">
        <span class="eyebrow">Execution control</span>
        <h1>{pipeline?.name || "Pipeline runs"}</h1>
        <p>Inspect run health, node timing, output data, and logs.</p>
      </div>
      <div class="toolbar-right">
        <a href="#/pipelines/{params.id}/edit" class="btn-sm" title="Edit Pipeline">
          <svg class="btn-icon" width="14" height="14" viewBox="0 0 24 24" fill="none"
            ><path
              d={icons.layout.d}
              stroke="currentColor"
              stroke-width="2"
              stroke-linecap="round"
              stroke-linejoin="round"
            /></svg
          >
          <span class="btn-label">Edit Pipeline</span>
        </a>
        <button class="btn-sm" on:click={() => (showBackfill = !showBackfill)} title="Backfill">
          <svg class="btn-icon" width="14" height="14" viewBox="0 0 24 24" fill="none"
            ><path
              d={icons.history.d}
              stroke="currentColor"
              stroke-width="2"
              stroke-linecap="round"
              stroke-linejoin="round"
            /></svg
          >
          <span class="btn-label">Backfill</span>
        </button>
        <button class="btn-sm" on:click={openParamsModal} title="Run with Params">
          <svg class="btn-icon" width="14" height="14" viewBox="0 0 24 24" fill="none"
            ><path
              d={icons.settings.d}
              stroke="currentColor"
              stroke-width="2"
              stroke-linecap="round"
              stroke-linejoin="round"
            /></svg
          >
          <span class="btn-label">Run with Params</span>
        </button>
        <button class="btn-sm btn-run" on:click={triggerRun} title="Run Now">
          <svg class="btn-icon" width="14" height="14" viewBox="0 0 24 24" fill="none"
            ><path
              d={icons.play.d}
              stroke="currentColor"
              stroke-width="2"
              stroke-linecap="round"
              stroke-linejoin="round"
            /></svg
          >
          <span class="btn-label">Run Now</span>
        </button>
      </div>
    </div>
  </header>

  {#if showBackfill}
    <div class="backfill-panel">
      <span class="backfill-label">Backfill date range:</span>
      <input type="date" bind:value={backfillStart} class="date-input" />
      <span class="backfill-label">to</span>
      <input type="date" bind:value={backfillEnd} class="date-input" />
      <button
        class="btn-sm btn-run"
        on:click={runBackfill}
        disabled={backfilling || !backfillStart || !backfillEnd}
      >
        {backfilling ? "Running..." : "Start Backfill"}
      </button>
      <button class="btn-sm" on:click={() => (showBackfill = false)}>Cancel</button>
    </div>
  {/if}

  {#if latestRun}
    <section class="run-metrics" aria-label="Recent run metrics">
      <div class="metric latest">
        <span>Latest state</span><strong><StatusBadge status={latestRun.status} size="sm" /></strong
        >
      </div>
      <div class="metric">
        <span>Recent runs</span><strong>{runs.length}</strong><small>loaded executions</small>
      </div>
      <div class="metric">
        <span>Success rate</span><strong
          >{recentSuccessRate === null ? "--" : `${recentSuccessRate}%`}</strong
        ><small>completed runs</small>
      </div>
      <div class="metric">
        <span>Latest duration</span><strong class="mono">{formatDuration(latestRun)}</strong><small
          >{relativeTime(latestRun.started_at, now)}</small
        >
      </div>
    </section>
  {/if}

  {#if plan}
    <section class="plan-section">
      <div class="plan-header">
        <div>
          <span class="eyebrow">Nodus planner</span>
          <h2>Execution plan</h2>
          <p>What Brokoli will place before runtime data resolves.</p>
        </div>
        <div class="plan-summary">
          <strong>{plan.static_instance_count}+</strong>
          <span>known instances</span>
          {#if plan.dynamic_nodes > 0}
            <strong>{plan.dynamic_nodes}</strong>
            <span>runtime fan-outs</span>
          {/if}
        </div>
      </div>
      <div class="plan-stages">
        {#each plan.stages as stage}
          <div class="plan-stage">
            <span class="stage-index">Stage {stage.index + 1}</span>
            <div class="stage-units">
              {#each stage.work_units as unit}
                <div class="plan-unit">
                  <div class="plan-unit-top">
                    <strong>{unit.logical_node_id}</strong>
                    <span>{unit.kind}</span>
                  </div>
                  <p>{unit.explain}</p>
                  <small>Retry scope: {unit.retry_scope}</small>
                </div>
              {/each}
            </div>
          </div>
        {/each}
      </div>
    </section>
  {/if}

  <!-- Run History Grid (collapsed by default, shows last 3 days) -->
  {#if runs.length > 0}
    <div class="history-section">
      <div class="history-header">
        <div>
          <h2>Run history</h2>
          <p>Each marker is an execution, grouped by start date.</p>
        </div>
        <div class="history-tools">
          <span class="history-legend"
            ><i class="success"></i>Success <i class="failed"></i>Failed
            <i class="running"></i>Running</span
          >
          <button class="history-toggle" on:click={() => (historyExpanded = !historyExpanded)}>
            {historyExpanded ? "Collapse" : `Show all ${runsByDate.length} days`}
            <svg
              width="12"
              height="12"
              viewBox="0 0 24 24"
              fill="none"
              style="transform:rotate({historyExpanded
                ? 180
                : 0}deg);transition:transform 150ms ease"
              ><path
                d="M6 9l6 6 6-6"
                stroke="currentColor"
                stroke-width="2"
                stroke-linecap="round"
                stroke-linejoin="round"
              /></svg
            >
          </button>
        </div>
      </div>
      <div class="history-grid">
        {#each historyExpanded ? runsByDate : runsByDate.slice(0, 3) as day}
          <div class="history-row">
            <span class="history-date mono">{day.label}</span>
            <div class="history-cells">
              {#each day.runs as r}
                <button
                  class="history-cell"
                  class:cell-success={isSuccessStatus(r.status)}
                  class:cell-failed={r.status === "failed"}
                  class:cell-running={r.status === "running"}
                  class:cell-pending={r.status === "pending"}
                  title={historyTitle(r)}
                  on:click={() => selectRun(r)}
                ></button>
              {/each}
            </div>
            <span class="history-count mono">{day.total}</span>
          </div>
        {/each}
      </div>
    </div>
  {/if}

  {#if loading}
    <div class="skeleton-rows">
      {#each Array(4) as _}
        <Skeleton height="64px" width="100%" />
      {/each}
    </div>
  {:else if runs.length === 0}
    <div class="empty-state">
      <p style="margin-bottom: 12px">No runs yet for this pipeline.</p>
      <button class="btn-primary" on:click={triggerRun}>Run Now</button>
    </div>
  {:else}
    <div class="list-heading">
      <div>
        <span class="eyebrow">Execution log</span>
        <h2>Recent runs</h2>
      </div>
      <span>{runs.length} loaded</span>
    </div>
    <div class="runs-list">
      {#each runs.slice((runPage - 1) * runPageSize, runPage * runPageSize) as run}
        <article class="run-card status-{run.status}" class:expanded={expandedRunId === run.id}>
          <div class="run-header">
            <button
              class="run-select"
              aria-expanded={expandedRunId === run.id}
              on:click={() => selectRun(run)}
            >
              <StatusBadge status={run.status} />
              <span class="run-identity"
                ><strong class="run-id mono">{run.id.slice(0, 8)}</strong><small
                  >{formatTime(run.started_at)} · {relativeTime(run.started_at, now)}</small
                ></span
              >
              <span class="run-stat"
                ><small>Duration</small><strong class="mono">{formatDuration(run)}</strong></span
              >
              {#if run.node_runs?.length}
                <span class="run-stat"
                  ><small>Rows</small><strong class="mono">{totalRows(run).toLocaleString()}</strong
                  ></span
                >
              {/if}
            </button>
            <div class="run-actions">
              {#if run.status === "running"}
                <button class="btn-cancel" on:click|stopPropagation={() => cancelRun(run.id)}
                  >Cancel</button
                >
              {/if}
              {#if run.status === "failed"}
                <span class="run-error-hint" title={run.error || "Check logs for details"}
                  >Error</span
                >
                <button class="btn-resume" on:click|stopPropagation={() => resumeRun(run.id)}
                  >Resume</button
                >
                <button class="btn-rerun" on:click|stopPropagation={() => rerunPipeline()}
                  >Re-run</button
                >
              {/if}
              <a
                href="/api/runs/{run.id}/logs/export"
                class="btn-export"
                on:click|stopPropagation
                title="Download logs"
                target="_blank">Logs</a
              >
              <button
                class="expand-button"
                aria-label={expandedRunId === run.id
                  ? "Collapse run details"
                  : "Expand run details"}
                on:click={() => selectRun(run)}
                ><svg
                  class="expand-icon"
                  width="14"
                  height="14"
                  viewBox="0 0 24 24"
                  fill="none"
                  style="transform: rotate({expandedRunId === run.id
                    ? 90
                    : 0}deg); transition: transform 150ms ease"
                >
                  <path
                    d={icons.chevronRight.d}
                    stroke="currentColor"
                    stroke-width="2"
                    stroke-linecap="round"
                    stroke-linejoin="round"
                  />
                </svg></button
              >
            </div>
          </div>

          {#if expandedRunId === run.id && selectedRun}
            <!-- svelte-ignore a11y_no_static_element_interactions -->
            <div class="run-detail" on:click|stopPropagation on:keydown={() => {}}>
              <!-- Run Summary Bar -->
              <div class="run-summary-bar">
                <div class="summary-item">
                  <span class="summary-label">Started</span>
                  <span class="summary-value">{formatFullTime(selectedRun.started_at)}</span>
                </div>
                <div class="summary-item">
                  <span class="summary-label">Finished</span>
                  <span class="summary-value">{formatFullTime(selectedRun.finished_at)}</span>
                </div>
                <div class="summary-item">
                  <span class="summary-label">Duration</span>
                  <span class="summary-value mono">{formatDuration(selectedRun)}</span>
                </div>
                <div class="summary-item">
                  <span class="summary-label">Rows</span>
                  <span class="summary-value mono">{totalRows(selectedRun)}</span>
                </div>
                <div class="summary-item">
                  <span class="summary-label">Nodes</span>
                  <span class="summary-value mono">
                    {nodeCount(selectedRun, "success")} ok,
                    {nodeCount(selectedRun, "failed")} failed,
                    {nodeCount(selectedRun, "skipped")} skipped
                  </span>
                </div>
                {#if selectedRun.error}
                  <div class="summary-item summary-error">
                    <span class="summary-label">Error</span>
                    <span class="summary-value">{selectedRun.error.slice(0, 100)}</span>
                  </div>
                {/if}
              </div>

              {#if instances.length > 0}
                <div class="detail-section">
                  <div class="physical-header">
                    <div>
                      <h3>Physical execution</h3>
                      <p>Nodus instances produced from this logical run.</p>
                    </div>
                    <span class="physical-count">{instances.length} instance{instances.length === 1 ? "" : "s"}</span>
                  </div>
                  <div class="physical-table-wrap">
                    <table class="physical-table">
                      <thead>
                        <tr>
                          <th>Node</th>
                          <th>Instance</th>
                          <th>Status</th>
                          <th>Attempt</th>
                          <th>Rows</th>
                          <th>Duration</th>
                        </tr>
                      </thead>
                      <tbody>
                        {#each instances as instance (instance.logical_node_id + instance.instance_key + instance.attempt)}
                          <tr>
                            <td class="mono">{instance.logical_node_id}</td>
                            <td class="mono">{instance.instance_key}</td>
                            <td><StatusBadge status={instance.status} /></td>
                            <td class="mono">{instance.attempt}</td>
                            <td class="mono">{instance.row_count.toLocaleString()}</td>
                            <td class="mono">{instance.duration_ms}ms</td>
                          </tr>
                        {/each}
                      </tbody>
                    </table>
                  </div>
                </div>
              {/if}

              <!-- DAG with live status -->
              {#if pipeline && pipeline.nodes.length > 0}
                <div class="detail-section">
                  <div class="canvas-header">
                    <h3>Pipeline Status</h3>
                    <span class="canvas-hint">Scroll to zoom, Alt+drag to pan</span>
                  </div>
                  <div class="canvas-status">
                    <PipelineCanvas
                      nodes={pipeline.nodes}
                      edges={pipeline.edges}
                      nodeStatuses={nodeStatuses(selectedRun)}
                      readonly={true}
                    />
                  </div>
                </div>
              {/if}

              <!-- Gantt Timeline -->
              {#if selectedRun.node_runs && selectedRun.node_runs.length > 0}
                <div class="detail-section">
                  <h3>Execution Timeline</h3>
                  <GanttChart
                    nodeRuns={selectedRun.node_runs}
                    nodes={pipeline?.nodes || []}
                    runStartedAt={selectedRun.started_at}
                    pipelineId={pipeline?.id || params.id || ""}
                    runId={selectedRun.id}
                    onSelectNode={(nodeId) => {
                      previewNodeId = previewNodeId === nodeId ? null : nodeId;
                    }}
                  />
                </div>
              {/if}

              <!-- Data Preview -->
              {#if selectedRun.node_runs && selectedRun.node_runs.length > 0}
                <div class="detail-section">
                  <h3>Data Preview</h3>
                  <div class="preview-tabs">
                    {#each primaryNodeRuns(selectedRun) as nr}
                      {@const node = (pipeline?.nodes || []).find((n) => n.id === nr.node_id)}
                      <button
                        class="preview-tab"
                        class:active={previewNodeId === nr.node_id}
                        on:click={() =>
                          (previewNodeId = previewNodeId === nr.node_id ? null : nr.node_id)}
                      >
                        {node?.name || nr.node_id}
                        <span class="tab-rows">{nr.row_count}</span>
                      </button>
                    {/each}
                  </div>
                  {#if previewNodeId}
                    {#key previewNodeId}
                      <DataPreview
                        runId={selectedRun.id}
                        nodeId={previewNodeId}
                        nodeName={(pipeline?.nodes || []).find((node) => node.id === previewNodeId)
                          ?.name || ""}
                      />
                    {/key}
                  {/if}
                </div>
              {/if}

              <!-- Node Profiling -->
              {#if selectedRun.node_runs && selectedRun.node_runs.length > 0}
                <div class="detail-section">
                  <h3>Data Profile</h3>
                  <div class="preview-tabs">
                    {#each primaryNodeRuns(selectedRun) as nr}
                      {@const node = (pipeline?.nodes || []).find((n) => n.id === nr.node_id)}
                      <button
                        class="preview-tab"
                        class:active={profileNodeId === nr.node_id}
                        on:click={() => loadProfile(run.id, nr.node_id)}
                      >
                        {node?.name || nr.node_id}
                      </button>
                    {/each}
                  </div>
                  {#if loadingProfile}
                    <div class="profile-loading">Loading profile...</div>
                  {:else if profileData?.profile}
                    {@const p = profileData.profile}
                    <div class="profile-summary">
                      <span class="profile-stat">{p.row_count} rows</span>
                      <span class="profile-stat">{p.column_count} columns</span>
                      <span class="profile-stat">{p.profiling_ms}ms</span>
                    </div>
                    {#if p.columns && p.columns.length > 0}
                      <div class="profile-table-wrap">
                        <table class="profile-table">
                          <thead>
                            <tr>
                              <th>Column</th>
                              <th>Type</th>
                              <th>Null %</th>
                              <th>Unique %</th>
                              <th>Min</th>
                              <th>Max</th>
                              <th>Mean</th>
                            </tr>
                          </thead>
                          <tbody>
                            {#each p.columns as col}
                              <tr>
                                <td class="col-name">{col.name}</td>
                                <td><span class="type-badge">{col.type}</span></td>
                                <td class:high-null={col.null_pct > 20}
                                  >{col.null_pct.toFixed(1)}%</td
                                >
                                <td>{col.unique_pct.toFixed(1)}%</td>
                                <td class="mono">{col.min_val || "—"}</td>
                                <td class="mono">{col.max_val || "—"}</td>
                                <td class="mono"
                                  >{col.is_numeric ? col.mean_val?.toFixed(2) : "—"}</td
                                >
                              </tr>
                            {/each}
                          </tbody>
                        </table>
                      </div>
                    {/if}
                    <!-- Drift alerts -->
                    {#if profileData.drift && profileData.drift.length > 0}
                      <div class="drift-section">
                        <h4>Schema Drift Alerts</h4>
                        {#each profileData.drift as alert}
                          <div class="drift-alert" class:critical={alert.severity === "critical"}>
                            <span class="drift-type">{alert.type}</span>
                            <span class="drift-col">{alert.column}</span>
                            <span class="drift-detail">{alert.previous} → {alert.current}</span>
                          </div>
                        {/each}
                      </div>
                    {/if}
                  {:else if profileNodeId}
                    <div class="profile-loading">No profile data for this node.</div>
                  {/if}
                </div>
              {/if}

              <!-- Logs -->
              <div class="detail-section">
                <h3>Logs</h3>
                <LogStream {logs} />
              </div>
            </div>
          {/if}
        </article>
      {/each}
    </div>
    <Pagination
      total={runs.length}
      page={runPage}
      pageSize={runPageSize}
      on:page={(e) => (runPage = e.detail)}
      on:pagesize={(e) => {
        runPageSize = e.detail;
        runPage = 1;
      }}
    />
  {/if}

  {#if showParamsModal}
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <div class="modal-overlay" on:click={() => (showParamsModal = false)} on:keydown={() => {}}>
      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <div class="modal-content" on:click|stopPropagation on:keydown={() => {}}>
        <h3>Run with Parameters</h3>
        <div class="params-list">
          {#each Object.entries(runParams) as [key, value], i}
            <div class="param-row">
              <input
                class="param-input"
                placeholder="Key"
                value={key}
                on:input={(e) => updateParamKey(key, e.currentTarget.value)}
              />
              <input
                class="param-input"
                placeholder="Value"
                {value}
                on:input={(e) => {
                  runParams[key] = e.currentTarget.value;
                  runParams = runParams;
                }}
              />
              <button class="btn-remove-param" on:click={() => removeParam(key)}>x</button>
            </div>
          {/each}
        </div>
        <button class="btn-sm" on:click={addParam}>+ Add Parameter</button>
        <div class="modal-actions">
          <button class="btn-sm" on:click={() => (showParamsModal = false)}>Cancel</button>
          <button class="btn-sm btn-run" on:click={triggerRun}>Run</button>
        </div>
      </div>
    </div>
  {/if}
</div>

<style>
  .toolbar {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: var(--space-xl);
  }
  .toolbar-left {
    display: flex;
    align-items: center;
    gap: var(--space-sm);
  }
  .back-link {
    font-size: 0.875rem;
    color: var(--text-muted);
  }
  .back-link:hover {
    color: var(--text-primary);
  }
  .separator {
    color: var(--border);
  }
  .pipeline-name {
    font-weight: 600;
  }
  .page-label {
    color: var(--text-muted);
  }
  .toolbar-right {
    display: flex;
    gap: var(--space-sm);
  }

  .btn-sm {
    padding: 4px 12px;
    border-radius: var(--radius-md);
    font-size: 0.8125rem;
    font-weight: 500;
    background: var(--bg-tertiary);
    border: 1px solid var(--border);
    color: var(--text-secondary);
    text-decoration: none;
    transition: all var(--transition-fast);
  }
  .btn-sm:hover {
    background: var(--border);
    color: var(--text-primary);
  }
  .btn-sm.btn-run {
    background: var(--success-bg);
    border-color: var(--success);
    color: var(--success);
  }
  .btn-primary {
    background: var(--accent);
    color: white;
    padding: var(--space-sm) var(--space-md);
    border-radius: var(--radius-md);
    font-weight: 500;
    margin-top: var(--space-md);
  }

  .empty-state {
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    padding: var(--space-xl);
    text-align: center;
    color: var(--text-secondary);
  }

  .runs-list {
    display: flex;
    flex-direction: column;
    gap: var(--space-sm);
  }

  .run-card {
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    cursor: pointer;
    transition: border-color var(--transition-fast);
    overflow: hidden;
  }
  .run-card:hover {
    border-color: var(--border-hover);
  }
  .run-card.expanded {
    border-color: var(--accent);
  }

  .run-header {
    display: flex;
    align-items: center;
    gap: var(--space-md);
    padding: var(--space-md) var(--space-lg);
  }
  .run-id {
    font-size: 0.75rem;
  }
  .run-time {
    font-size: 0.8125rem;
    color: var(--text-secondary);
  }
  .run-duration {
    font-size: 0.75rem;
    color: var(--text-muted);
  }
  .run-rows {
    font-size: 0.75rem;
    color: var(--text-muted);
    margin-left: auto;
  }
  .btn-resume {
    padding: 3px 10px;
    border-radius: 4px;
    font-size: 11px;
    font-weight: 500;
    background: var(--warning-bg);
    border: 1px solid rgba(245, 158, 11, 0.3);
    color: var(--warning);
    transition: all 150ms ease;
  }
  .btn-resume:hover {
    background: rgba(245, 158, 11, 0.15);
  }
  .btn-rerun {
    padding: 3px 10px;
    border-radius: 4px;
    font-size: 11px;
    font-weight: 500;
    background: var(--success-bg);
    color: var(--success);
    border: 1px solid rgba(34, 197, 94, 0.2);
    transition: background 150ms ease;
  }
  .btn-rerun:hover {
    background: rgba(34, 197, 94, 0.15);
  }
  .btn-cancel {
    padding: 3px 10px;
    border-radius: 4px;
    font-size: 11px;
    font-weight: 500;
    background: var(--failed-bg);
    border: 1px solid rgba(239, 68, 68, 0.3);
    color: var(--failed);
    transition: all 150ms ease;
  }
  .btn-cancel:hover {
    background: rgba(239, 68, 68, 0.15);
  }
  .run-error-hint {
    font-size: 10px;
    font-weight: 600;
    color: var(--failed);
    background: var(--failed-bg);
    padding: 2px 6px;
    border-radius: 3px;
  }
  .btn-export {
    font-size: 10px;
    font-weight: 500;
    color: var(--text-muted);
    background: var(--bg-secondary);
    padding: 2px 8px;
    border-radius: 3px;
    border: 1px solid var(--border-subtle);
    text-decoration: none;
    transition: all 150ms ease;
  }
  .btn-export:hover {
    color: var(--text-primary);
    border-color: var(--border-hover);
  }
  .backfill-panel {
    display: flex;
    align-items: center;
    gap: var(--space-sm);
    padding: var(--space-md);
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    margin-bottom: var(--space-md);
  }
  .backfill-label {
    font-size: 0.8125rem;
    color: var(--text-secondary);
  }
  .date-input {
    font-family: var(--font-mono);
    font-size: 0.8125rem;
    padding: 4px 8px;
  }

  .physical-header {
    display: flex;
    align-items: end;
    justify-content: space-between;
    gap: var(--space-md);
    margin-bottom: var(--space-sm);
  }
  .physical-header p {
    margin: 3px 0 0;
    color: var(--text-muted);
    font-size: 0.75rem;
  }
  .physical-count {
    color: var(--text-muted);
    font-size: 0.75rem;
    white-space: nowrap;
  }
  .physical-table-wrap {
    overflow-x: auto;
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius-md);
  }
  .physical-table {
    width: 100%;
    min-width: 620px;
    border-collapse: collapse;
    font-size: 0.75rem;
  }
  .physical-table th,
  .physical-table td {
    padding: 8px 10px;
    text-align: left;
    border-bottom: 1px solid var(--border-subtle);
    white-space: nowrap;
  }
  .physical-table th {
    color: var(--text-muted);
    font-size: 0.6875rem;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  .physical-table tr:last-child td {
    border-bottom: 0;
  }

  .expand-icon {
    color: var(--text-muted);
    font-size: 0.75rem;
  }
  .mono {
    font-family: var(--font-mono);
  }

  .run-detail {
    border-top: 1px solid var(--border);
    padding: var(--space-lg);
    cursor: default;
  }

  .detail-section {
    margin-bottom: var(--space-lg);
  }
  .detail-section:last-child {
    margin-bottom: 0;
  }
  .detail-section h3 {
    font-size: 0.75rem;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.08em;
    margin-bottom: var(--space-sm);
  }

  .canvas-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 8px;
  }
  .canvas-hint {
    font-size: 10px;
    color: var(--text-dim);
    font-family: var(--font-mono);
  }
  .canvas-status {
    height: 220px;
    border-radius: var(--radius-lg);
    overflow: hidden;
    border: 1px solid var(--border-subtle);
  }

  .preview-tabs {
    display: flex;
    gap: 4px;
    margin-bottom: 8px;
    flex-wrap: wrap;
  }
  .preview-tab {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 5px 10px;
    border-radius: 6px;
    font-size: 12px;
    font-weight: 500;
    background: var(--bg-secondary);
    border: 1px solid var(--border-subtle);
    color: var(--text-secondary);
    transition: all 150ms ease;
  }
  .preview-tab:hover {
    border-color: var(--text-ghost);
    color: var(--text-primary);
  }
  .preview-tab.active {
    border-color: var(--accent);
    background: var(--accent-glow);
    color: var(--accent-text);
  }
  .tab-rows {
    font-family: "JetBrains Mono", monospace;
    font-size: 10px;
    color: var(--text-dim);
    background: var(--bg-code);
    padding: 1px 5px;
    border-radius: 3px;
  }
  .preview-tab.active .tab-rows {
    color: var(--accent);
    background: rgba(99, 102, 241, 0.1);
  }

  .modal-overlay {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.5);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 1000;
  }
  .modal-content {
    background: var(--bg-primary);
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    padding: var(--space-lg);
    min-width: 400px;
    max-width: 540px;
  }
  .modal-content h3 {
    font-size: 0.875rem;
    font-weight: 600;
    margin-bottom: var(--space-md);
  }
  .params-list {
    display: flex;
    flex-direction: column;
    gap: var(--space-sm);
    margin-bottom: var(--space-md);
  }
  .param-row {
    display: flex;
    gap: var(--space-sm);
    align-items: center;
  }
  .param-input {
    flex: 1;
    padding: 6px 10px;
    border-radius: var(--radius-md);
    border: 1px solid var(--border);
    background: var(--bg-secondary);
    color: var(--text-primary);
    font-size: 0.8125rem;
    font-family: var(--font-mono);
  }
  .param-input:focus {
    outline: none;
    border-color: var(--accent);
  }
  .btn-remove-param {
    padding: 4px 8px;
    border-radius: var(--radius-md);
    font-size: 0.75rem;
    font-weight: 600;
    background: var(--failed-bg);
    border: 1px solid rgba(239, 68, 68, 0.3);
    color: var(--failed);
    cursor: pointer;
  }
  .btn-remove-param:hover {
    background: rgba(239, 68, 68, 0.15);
  }
  .modal-actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--space-sm);
    margin-top: var(--space-md);
  }

  /* ── Profile ── */
  .profile-loading {
    padding: 12px;
    font-size: 12px;
    color: var(--text-muted);
    text-align: center;
  }
  .profile-summary {
    display: flex;
    gap: 16px;
    padding: 10px 0;
    border-bottom: 1px solid var(--border-subtle);
    margin-bottom: 8px;
  }
  .profile-stat {
    font-family: var(--font-mono);
    font-size: 12px;
    font-weight: 600;
    color: var(--text-primary);
  }
  .profile-table-wrap {
    overflow-x: auto;
  }
  .profile-table {
    width: 100%;
    border-collapse: collapse;
    font-size: 11px;
    font-family: var(--font-mono);
  }
  .profile-table th {
    text-align: left;
    padding: 5px 8px;
    font-size: 9px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--text-ghost);
    border-bottom: 1px solid var(--border-subtle);
  }
  .profile-table td {
    padding: 4px 8px;
    color: var(--text-secondary);
    border-bottom: 1px solid var(--border-subtle);
  }
  .profile-table .col-name {
    font-weight: 600;
    color: var(--text-primary);
  }
  .profile-table .mono {
    font-family: var(--font-mono);
  }
  .type-badge {
    font-size: 9px;
    font-weight: 600;
    padding: 1px 5px;
    border-radius: 3px;
    background: var(--bg-tertiary);
    color: var(--text-muted);
  }
  .high-null {
    color: var(--warning);
    font-weight: 600;
  }

  /* ── Drift ── */
  .drift-section {
    margin-top: 12px;
  }
  .drift-section h4 {
    font-size: 10px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--warning);
    margin-bottom: 6px;
  }
  .drift-alert {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 5px 8px;
    border-radius: 4px;
    font-size: 11px;
    margin-bottom: 3px;
    background: rgba(245, 158, 11, 0.08);
    border: 1px solid rgba(245, 158, 11, 0.15);
  }
  .drift-alert.critical {
    background: var(--failed-bg);
    border-color: rgba(239, 68, 68, 0.2);
  }
  .drift-type {
    font-family: var(--font-mono);
    font-size: 10px;
    font-weight: 600;
    padding: 1px 5px;
    border-radius: 3px;
    background: var(--bg-tertiary);
    color: var(--warning);
  }
  .drift-alert.critical .drift-type {
    color: var(--failed);
  }
  .drift-col {
    font-weight: 600;
    color: var(--text-primary);
  }
  .drift-detail {
    color: var(--text-muted);
    font-size: 10px;
  }

  /* Toolbar buttons: icon + label on desktop, icon-only on mobile */
  .btn-sm {
    display: inline-flex;
    align-items: center;
    gap: 5px;
  }
  .btn-icon {
    flex-shrink: 0;
  }

  @media (max-width: 768px) {
    .toolbar {
      flex-wrap: wrap;
      gap: var(--space-sm);
    }
    .toolbar-left {
      flex: 1;
      min-width: 0;
    }
    .pipeline-name {
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
      max-width: 140px;
      display: inline-block;
    }
    .toolbar-right {
      gap: 4px;
    }
    .btn-label {
      display: none;
    }
    .btn-sm {
      padding: 6px 8px;
    }
    .backfill-panel {
      flex-wrap: wrap;
    }
    .plan-header {
      align-items: flex-start;
      flex-direction: column;
      gap: var(--space-sm);
    }
    .run-header {
      flex-wrap: wrap;
      gap: var(--space-sm);
    }
    .run-rows {
      margin-left: 0;
    }
  }

  .plan-section {
    margin-bottom: var(--space-lg);
    padding: var(--space-lg);
    background: linear-gradient(135deg, var(--bg-secondary), rgba(13, 148, 136, 0.05));
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
  }
  .plan-header {
    display: flex;
    align-items: end;
    justify-content: space-between;
    gap: var(--space-lg);
    margin-bottom: var(--space-md);
  }
  .plan-header h2 {
    margin: 3px 0 0;
  }
  .plan-header p {
    margin: 4px 0 0;
    color: var(--text-muted);
    font-size: 0.75rem;
  }
  .plan-summary {
    display: flex;
    align-items: baseline;
    gap: 6px;
    color: var(--text-muted);
    font-size: 0.6875rem;
    white-space: nowrap;
  }
  .plan-summary strong {
    margin-left: 8px;
    color: var(--accent);
    font-family: var(--font-mono);
    font-size: 1rem;
  }
  .plan-summary strong:first-child {
    margin-left: 0;
  }
  .plan-stages {
    display: grid;
    gap: var(--space-sm);
  }
  .plan-stage {
    display: grid;
    grid-template-columns: 72px 1fr;
    gap: var(--space-sm);
    align-items: start;
  }
  .stage-index {
    padding-top: 9px;
    color: var(--text-muted);
    font-family: var(--font-mono);
    font-size: 0.6875rem;
    text-transform: uppercase;
  }
  .stage-units {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(190px, 1fr));
    gap: var(--space-sm);
  }
  .plan-unit {
    padding: 10px;
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius-md);
    background: var(--bg-primary);
  }
  .plan-unit-top {
    display: flex;
    justify-content: space-between;
    gap: var(--space-sm);
  }
  .plan-unit-top strong {
    overflow: hidden;
    text-overflow: ellipsis;
    font-family: var(--font-mono);
    font-size: 0.75rem;
  }
  .plan-unit-top span,
  .plan-unit small {
    color: var(--text-muted);
    font-size: 0.6875rem;
  }
  .plan-unit p {
    min-height: 2.2em;
    margin: 6px 0;
    color: var(--text-secondary);
    font-size: 0.75rem;
    line-height: 1.4;
  }

  /* ── Run History Grid ── */
  .history-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: var(--space-sm);
  }
  .history-toggle {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    font-size: 0.6875rem;
    color: var(--text-muted);
    background: none;
    border: none;
    cursor: pointer;
    padding: 4px 8px;
    border-radius: var(--radius-sm);
    transition: color 150ms ease;
  }
  .history-toggle:hover {
    color: var(--text-primary);
  }
  .history-section {
    margin-bottom: var(--space-lg);
  }
  .detail-section-title {
    font-size: 0.75rem;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.08em;
    margin-bottom: var(--space-sm);
  }
  .history-grid {
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    padding: var(--space-md);
  }
  .history-row {
    display: flex;
    align-items: center;
    gap: var(--space-sm);
    padding: 4px 0;
    border-bottom: 1px solid var(--border-subtle);
  }
  .history-row:last-child {
    border-bottom: none;
  }
  .history-date {
    width: 55px;
    flex-shrink: 0;
    font-size: 0.6875rem;
    color: var(--text-muted);
  }
  .history-cells {
    display: flex;
    gap: 3px;
    flex: 1;
    flex-wrap: wrap;
  }
  .history-cell {
    width: 14px;
    height: 14px;
    border-radius: 2px;
    border: none;
    cursor: pointer;
    transition:
      transform 100ms ease,
      box-shadow 100ms ease;
    background: var(--bg-tertiary);
  }
  .history-cell:hover {
    transform: scale(1.3);
    box-shadow: 0 0 4px rgba(255, 255, 255, 0.1);
  }
  .cell-success {
    background: var(--success);
  }
  .cell-failed {
    background: var(--failed);
  }
  .cell-running {
    background: var(--running);
    animation: pulse-cell 1.5s ease-in-out infinite;
  }
  .cell-pending {
    background: var(--pending);
  }
  @keyframes pulse-cell {
    0%,
    100% {
      opacity: 1;
    }
    50% {
      opacity: 0.5;
    }
  }
  .history-count {
    width: 30px;
    text-align: right;
    font-size: 0.6875rem;
    color: var(--text-dim);
    flex-shrink: 0;
  }

  /* ── Run Summary Bar ── */
  .run-summary-bar {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-md);
    padding: var(--space-md) 0;
    margin-bottom: var(--space-md);
    border-bottom: 1px solid var(--border);
  }
  .summary-item {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .summary-label {
    font-size: 0.625rem;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--text-muted);
    font-weight: 600;
  }
  .summary-value {
    font-size: 0.8125rem;
    color: var(--text-primary);
  }
  .summary-error .summary-value {
    color: var(--failed);
    font-size: 0.75rem;
  }

  /* Refined execution workspace */
  .runs-header {
    margin-bottom: 18px;
  }
  .header-main {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
    gap: 24px;
    margin-top: 18px;
  }
  .header-copy {
    min-width: 0;
  }
  .eyebrow {
    display: block;
    margin-bottom: 5px;
    color: var(--accent);
    font: 650 9px var(--font-mono);
    letter-spacing: 0.14em;
    text-transform: uppercase;
  }
  .header-copy h1 {
    overflow: hidden;
    color: var(--text-primary);
    font-size: 24px;
    font-weight: 650;
    letter-spacing: -0.035em;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .header-copy p {
    margin-top: 4px;
    color: var(--text-muted);
    font-size: 12px;
  }
  .header-main .toolbar-right {
    flex: none;
    align-items: center;
  }
  .header-main .btn-sm {
    min-height: 34px;
    padding: 0 12px;
    border-color: var(--border-subtle);
    background: var(--bg-secondary);
  }
  .header-main .btn-sm:hover {
    border-color: var(--border-hover);
    background: var(--bg-tertiary);
  }
  .header-main .btn-run {
    border-color: color-mix(in srgb, var(--success), transparent 35%);
    background: var(--success-bg);
  }

  .run-metrics {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: 10px;
    margin-bottom: 18px;
  }
  .metric {
    position: relative;
    display: grid;
    min-height: 78px;
    grid-template-columns: 1fr;
    align-content: center;
    gap: 3px;
    padding: 13px 15px;
    overflow: hidden;
    border: 1px solid var(--border-subtle);
    border-radius: 8px;
    background:
      linear-gradient(140deg, color-mix(in srgb, var(--bg-tertiary), transparent 52%), transparent),
      var(--bg-secondary);
    box-shadow: var(--shadow-card);
  }
  .metric::before {
    content: "";
    position: absolute;
    inset: 0 auto 0 0;
    width: 2px;
    background: var(--border);
  }
  .metric.latest::before {
    background: var(--accent);
  }
  .metric > span {
    color: var(--text-muted);
    font-size: 9px;
    font-weight: 600;
    letter-spacing: 0.07em;
    text-transform: uppercase;
  }
  .metric > strong {
    min-height: 23px;
    color: var(--text-primary);
    font-size: 18px;
    font-weight: 640;
  }
  .metric.latest > strong {
    display: flex;
    align-items: center;
  }
  .metric > small {
    color: var(--text-dim);
    font-size: 9px;
  }

  .history-section {
    margin-bottom: 22px;
  }
  .history-header {
    margin-bottom: 9px;
  }
  .history-header h2,
  .list-heading h2 {
    color: var(--text-primary);
    font-size: 13px;
    font-weight: 620;
    letter-spacing: -0.01em;
  }
  .history-header p {
    margin-top: 2px;
    color: var(--text-dim);
    font-size: 10px;
  }
  .history-tools,
  .history-legend {
    display: flex;
    align-items: center;
    gap: 9px;
  }
  .history-legend {
    color: var(--text-dim);
    font-size: 9px;
  }
  .history-legend i {
    width: 7px;
    height: 7px;
    margin-right: -5px;
    border-radius: 2px;
    background: var(--pending);
  }
  .history-legend i.success {
    background: var(--success);
  }
  .history-legend i.failed {
    background: var(--failed);
  }
  .history-legend i.running {
    background: var(--running);
  }
  .history-grid {
    padding: 11px 14px;
    border-color: var(--border-subtle);
    border-radius: 8px;
    background: var(--bg-secondary);
    box-shadow: var(--shadow-card);
  }
  .history-row {
    min-height: 31px;
    padding: 6px 2px;
  }
  .history-date {
    width: 62px;
    color: var(--text-secondary);
  }
  .history-cell {
    width: 20px;
    height: 9px;
    border-radius: 3px;
    opacity: 0.88;
  }
  .history-cell:hover {
    opacity: 1;
    transform: translateY(-1px) scaleY(1.35);
  }

  .list-heading {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
    margin: 0 1px 9px;
  }
  .list-heading .eyebrow {
    margin-bottom: 2px;
  }
  .list-heading > span {
    color: var(--text-dim);
    font: 9px var(--font-mono);
  }
  .runs-list {
    gap: 8px;
  }
  .run-card {
    position: relative;
    border-color: var(--border-subtle);
    border-radius: 8px;
    cursor: default;
    box-shadow: var(--shadow-card);
  }
  .run-card::before {
    content: "";
    position: absolute;
    z-index: 1;
    top: 10px;
    bottom: 10px;
    left: 0;
    width: 2px;
    border-radius: 0 2px 2px 0;
    background: var(--pending);
    opacity: 0.7;
  }
  .run-card.status-success::before {
    background: var(--success);
  }
  .run-card.status-failed::before {
    background: var(--failed);
  }
  .run-card.status-running::before {
    background: var(--running);
  }
  .run-card:hover {
    border-color: var(--border-hover);
  }
  .run-card.expanded {
    border-color: color-mix(in srgb, var(--accent), transparent 30%);
    box-shadow:
      0 0 0 1px color-mix(in srgb, var(--accent), transparent 88%),
      var(--shadow-md);
  }
  .run-header {
    gap: 0;
    padding: 0;
  }
  .run-select {
    display: flex;
    min-width: 0;
    flex: 1;
    align-items: center;
    gap: 14px;
    padding: 13px 16px 13px 18px;
    color: inherit;
    text-align: left;
  }
  .run-select:hover {
    background: color-mix(in srgb, var(--bg-tertiary), transparent 58%);
  }
  .run-identity {
    display: flex;
    min-width: 180px;
    flex: 1;
    flex-direction: column;
    gap: 2px;
  }
  .run-id {
    color: var(--text-primary);
    font-size: 11px;
    font-weight: 650;
    letter-spacing: 0.025em;
  }
  .run-identity small {
    overflow: hidden;
    color: var(--text-muted);
    font-size: 9.5px;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .run-stat {
    display: flex;
    min-width: 78px;
    flex-direction: column;
    gap: 2px;
  }
  .run-stat small {
    color: var(--text-dim);
    font-size: 8.5px;
    letter-spacing: 0.06em;
    text-transform: uppercase;
  }
  .run-stat strong {
    color: var(--text-secondary);
    font-size: 10px;
    font-weight: 500;
  }
  .run-actions {
    display: flex;
    flex: none;
    align-items: center;
    gap: 6px;
    padding-right: 12px;
  }
  .btn-export,
  .btn-cancel,
  .btn-resume,
  .btn-rerun {
    min-height: 25px;
    display: inline-flex;
    align-items: center;
  }
  .expand-button {
    display: grid;
    width: 28px;
    height: 28px;
    place-items: center;
    border-radius: 5px;
    color: var(--text-muted);
  }
  .expand-button:hover {
    background: var(--bg-tertiary);
    color: var(--text-primary);
  }

  .run-detail {
    padding: 18px;
    border-color: var(--border-subtle);
    background: color-mix(in srgb, var(--bg-primary), transparent 30%);
  }
  .run-summary-bar {
    display: grid;
    grid-template-columns: 1.2fr 1.2fr 0.55fr 0.45fr 1fr;
    gap: 0;
    margin-bottom: 14px;
    padding: 0;
    overflow: hidden;
    border: 1px solid var(--border-subtle);
    border-radius: 7px;
    background: var(--bg-secondary);
  }
  .summary-item {
    min-height: 62px;
    justify-content: center;
    padding: 11px 13px;
    border-right: 1px solid var(--border-subtle);
  }
  .summary-item:last-child {
    border-right: 0;
  }
  .summary-label {
    font-size: 8.5px;
  }
  .summary-value {
    font-size: 10.5px;
  }
  .summary-error {
    grid-column: 1 / -1;
    min-height: auto;
    border-top: 1px solid var(--border-subtle);
    border-right: 0;
  }
  .detail-section {
    margin-bottom: 12px;
    padding: 14px;
    border: 1px solid var(--border-subtle);
    border-radius: 8px;
    background: var(--bg-secondary);
    box-shadow: var(--shadow-card);
  }
  .detail-section h3 {
    margin-bottom: 10px;
    color: var(--text-secondary);
    font-size: 9px;
    font-weight: 650;
    letter-spacing: 0.1em;
  }
  .canvas-header {
    margin-bottom: 10px;
  }
  .canvas-header h3 {
    margin: 0;
  }
  .canvas-status {
    height: 270px;
    border-radius: 6px;
    background: var(--bg-canvas);
  }
  .preview-tab {
    min-height: 28px;
    padding: 4px 9px;
    border-radius: 5px;
    font-size: 10.5px;
  }
  .modal-content {
    width: min(540px, calc(100vw - 32px));
    min-width: 0;
  }

  @media (max-width: 980px) {
    .header-main {
      align-items: flex-start;
      flex-direction: column;
    }
    .run-summary-bar {
      grid-template-columns: repeat(3, 1fr);
    }
    .summary-item {
      border-bottom: 1px solid var(--border-subtle);
    }
  }
  @media (max-width: 768px) {
    .runs-header {
      margin-bottom: 14px;
    }
    .header-main {
      gap: 14px;
      margin-top: 14px;
    }
    .header-copy h1 {
      font-size: 20px;
    }
    .header-main .toolbar-right {
      width: 100%;
    }
    .run-metrics {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }
    .history-legend {
      display: none;
    }
    .run-header {
      align-items: stretch;
    }
    .run-select {
      display: grid;
      grid-template-columns: auto minmax(0, 1fr) auto;
      gap: 10px;
    }
    .run-identity {
      min-width: 0;
    }
    .run-stat {
      min-width: 64px;
    }
    .run-actions {
      padding-right: 8px;
    }
    .btn-export {
      font-size: 0;
      width: 28px;
      justify-content: center;
    }
    .btn-export::after {
      content: "↓";
      font-size: 12px;
    }
    .run-summary-bar {
      grid-template-columns: repeat(2, 1fr);
    }
    .canvas-status {
      height: 230px;
    }
  }
  @media (max-width: 520px) {
    .header-main .toolbar-right {
      display: grid;
      grid-template-columns: repeat(4, 1fr);
    }
    .header-main .btn-sm {
      justify-content: center;
      padding: 0 8px;
    }
    .metric {
      min-height: 70px;
    }
    .history-tools {
      gap: 2px;
    }
    .history-grid {
      padding-inline: 10px;
    }
    .history-cell {
      width: 16px;
    }
    .run-header {
      flex-wrap: wrap;
    }
    .run-select {
      width: 100%;
      flex-basis: 100%;
    }
    .run-actions {
      width: 100%;
      justify-content: flex-end;
      padding: 0 8px 8px;
    }
    .run-detail {
      padding: 10px;
    }
    .run-summary-bar {
      grid-template-columns: 1fr;
    }
    .summary-item {
      min-height: 54px;
      border-right: 0;
    }
    .detail-section {
      padding: 10px;
    }
    .canvas-hint {
      display: none;
    }
  }
</style>
