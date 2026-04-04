<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { icons } from "../lib/icons";
  import { api } from "../lib/api";
  import { authHeaders } from "../lib/auth";
  import { notify } from "../lib/toast";
  import { onWSEvent, liveNodeStatuses } from "../lib/stores";
  import StatusBadge from "../components/StatusBadge.svelte";
  import RunTimeline from "../components/RunTimeline.svelte";
  import LogStream from "../components/LogStream.svelte";
  import DataPreview from "../components/DataPreview.svelte";
  import PipelineCanvas from "../components/PipelineCanvas.svelte";
  import Pagination from "../components/Pagination.svelte";
  import type { Pipeline, Run, LogEntry, RunStatus } from "../lib/types";

  export let params: { id?: string } = {};
  let runPage = 1;
  let runPageSize = 25;

  let pipeline: Pipeline | null = null;
  let runs: Run[] = [];
  let selectedRun: Run | null = null;
  let logs: LogEntry[] = [];
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

  async function loadProfile(runId: string, nodeId: string) {
    if (profileNodeId === nodeId) { profileNodeId = null; profileData = null; return; }
    profileNodeId = nodeId;
    loadingProfile = true;
    try {
      const res = await fetch(`/api/runs/${runId}/nodes/${nodeId}/profile`, { headers: authHeaders() });
      if (res.ok) {
        profileData = await res.json();
      } else {
        profileData = null;
      }
    } catch {
      profileData = null;
    }
    loadingProfile = false;
  }

  let unsubWS: (() => void) | null = null;

  onMount(async () => {
    if (!params.id) return;
    try {
      pipeline = await api.pipelines.get(params.id);
      runs = await api.runs.listByPipeline(params.id);
    } catch (e) {
      notify.error("Failed to load");
    } finally {
      loading = false;
    }

    // Live updates: refresh run list when a run completes/fails for this pipeline
    unsubWS = onWSEvent(async (event) => {
      if (!params.id) return;
      if (event.pipeline_id !== params.id && !event.run_id) return;

      if (event.type === "run.completed" || event.type === "run.failed" || event.type === "run.started") {
        runs = await api.runs.listByPipeline(params.id);
        // Refresh expanded run detail
        if (expandedRunId && (event.type === "run.completed" || event.type === "run.failed")) {
          try {
            const [runData, logData] = await Promise.all([
              api.runs.get(expandedRunId),
              api.runs.getLogs(expandedRunId),
            ]);
            selectedRun = runData;
            logs = logData;
          } catch { /* ignore */ }
        }
      }

      // Live log streaming
      if (event.type === "log" && expandedRunId === event.run_id) {
        logs = [...logs, {
          run_id: event.run_id,
          node_id: event.node_id || "",
          level: (event.level as any) || "info",
          message: event.message || "",
          timestamp: event.timestamp,
        }];
      }
    });
  });

  onDestroy(() => {
    unsubWS?.();
  });

  async function selectRun(run: Run) {
    if (expandedRunId === run.id) {
      expandedRunId = null;
      selectedRun = null;
      return;
    }
    expandedRunId = run.id;
    try {
      selectedRun = await api.runs.get(run.id);
      logs = await api.runs.getLogs(run.id);
    } catch (e) {
      notify.error("Failed to load run");
    }
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
    for (const nr of run.node_runs || []) {
      map[nr.node_id] = nr.status;
    }
    // Merge live WS updates (overrides stored status with real-time)
    const live = $liveNodeStatuses[run.id];
    if (live) {
      for (const [nodeId, status] of Object.entries(live)) {
        map[nodeId] = status;
      }
    }
    return map;
  }

  function totalRows(run: Run): number {
    return (run.node_runs || []).reduce((sum, nr) => sum + nr.row_count, 0);
  }

  function formatFullTime(ts: string | null): string {
    if (!ts) return "--";
    const d = new Date(ts);
    return d.toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric" }) +
           " " + d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", second: "2-digit", timeZoneName: "short" });
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
        label: new Date(date + "T00:00:00").toLocaleDateString(undefined, { month: "short", day: "numeric" }),
        runs: dayRuns.sort((a, b) => (a.started_at || "").localeCompare(b.started_at || "")),
        success: dayRuns.filter(r => r.status === "success" || r.status === "succeeded" || r.status === "completed").length,
        failed: dayRuns.filter(r => r.status === "failed").length,
        total: dayRuns.length,
      });
    }
    return result.sort((a, b) => b.date.localeCompare(a.date)).slice(0, 14);
  }

  $: runsByDate = groupRunsByDate(runs);
</script>

<div class="runs-page animate-in">
  <div class="toolbar">
    <div class="toolbar-left">
      <a href="#/pipelines" class="back-link"><svg width="14" height="14" viewBox="0 0 24 24" fill="none" style="display:inline;vertical-align:middle"><path d={icons.arrowLeft.d} stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" /></svg> Pipelines</a>
      <span class="separator">/</span>
      <span class="pipeline-name">{pipeline?.name || "Pipeline"}</span>
      <span class="separator">/</span>
      <span class="page-label">Runs</span>
    </div>
    <div class="toolbar-right">
      <a href="#/pipelines/{params.id}/edit" class="btn-sm" title="Edit Pipeline">
        <svg class="btn-icon" width="14" height="14" viewBox="0 0 24 24" fill="none"><path d={icons.layout.d} stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" /></svg>
        <span class="btn-label">Edit Pipeline</span>
      </a>
      <button class="btn-sm" on:click={() => showBackfill = !showBackfill} title="Backfill">
        <svg class="btn-icon" width="14" height="14" viewBox="0 0 24 24" fill="none"><path d={icons.history.d} stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" /></svg>
        <span class="btn-label">Backfill</span>
      </button>
      <button class="btn-sm" on:click={openParamsModal} title="Run with Params">
        <svg class="btn-icon" width="14" height="14" viewBox="0 0 24 24" fill="none"><path d={icons.settings.d} stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" /></svg>
        <span class="btn-label">Run with Params</span>
      </button>
      <button class="btn-sm btn-run" on:click={triggerRun} title="Run Now">
        <svg class="btn-icon" width="14" height="14" viewBox="0 0 24 24" fill="none"><path d={icons.play.d} stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" /></svg>
        <span class="btn-label">Run Now</span>
      </button>
    </div>
  </div>

  {#if showBackfill}
    <div class="backfill-panel">
      <span class="backfill-label">Backfill date range:</span>
      <input type="date" bind:value={backfillStart} class="date-input" />
      <span class="backfill-label">to</span>
      <input type="date" bind:value={backfillEnd} class="date-input" />
      <button class="btn-sm btn-run" on:click={runBackfill} disabled={backfilling || !backfillStart || !backfillEnd}>
        {backfilling ? "Running..." : "Start Backfill"}
      </button>
      <button class="btn-sm" on:click={() => showBackfill = false}>Cancel</button>
    </div>
  {/if}

  <!-- Run History Grid (collapsed by default, shows last 3 days) -->
  {#if runs.length > 0}
    <div class="history-section">
      <div class="history-header">
        <h3 class="detail-section-title" style="margin:0">Run History</h3>
        <button class="history-toggle" on:click={() => historyExpanded = !historyExpanded}>
          {historyExpanded ? "Collapse" : `Show all ${runsByDate.length} days`}
          <svg width="12" height="12" viewBox="0 0 24 24" fill="none" style="transform:rotate({historyExpanded ? 180 : 0}deg);transition:transform 150ms ease"><path d="M6 9l6 6 6-6" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/></svg>
        </button>
      </div>
      <div class="history-grid">
        {#each (historyExpanded ? runsByDate : runsByDate.slice(0, 3)) as day}
          <div class="history-row">
            <span class="history-date mono">{day.label}</span>
            <div class="history-cells">
              {#each day.runs as r}
                <button
                  class="history-cell"
                  class:cell-success={r.status === "success" || r.status === "succeeded" || r.status === "completed"}
                  class:cell-failed={r.status === "failed"}
                  class:cell-running={r.status === "running"}
                  class:cell-pending={r.status === "pending"}
                  title="{r.id.slice(0,8)} | {r.status} | {formatTime(r.started_at)} | {formatDuration(r)} | {totalRows(r)} rows{r.error ? ' | ' + r.error.slice(0,50) : ''}"
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
    <div class="empty-state">Loading...</div>
  {:else if runs.length === 0}
    <div class="empty-state">
      <p>No runs yet for this pipeline.</p>
      <button class="btn-primary" on:click={triggerRun}>Trigger First Run</button>
    </div>
  {:else}
    <div class="runs-list">
      {#each runs.slice((runPage - 1) * runPageSize, runPage * runPageSize) as run}
        <!-- svelte-ignore a11y_no_static_element_interactions -->
        <div class="run-card" class:expanded={expandedRunId === run.id} on:click={() => selectRun(run)} on:keydown={() => {}}>
          <div class="run-header">
            <StatusBadge status={run.status} />
            <span class="run-id mono">{run.id.slice(0, 8)}</span>
            <span class="run-time">{formatTime(run.started_at)}</span>
            <span class="run-duration mono">{formatDuration(run)}</span>
            <span class="run-rows mono">{totalRows(run)} rows</span>
            {#if run.status === "running"}
              <button class="btn-cancel" on:click|stopPropagation={() => cancelRun(run.id)}>Cancel</button>
            {/if}
            {#if run.status === "failed"}
              <span class="run-error-hint" title={run.error || "Check logs for details"}>Error</span>
              <button class="btn-resume" on:click|stopPropagation={() => resumeRun(run.id)}>Resume</button>
              <button class="btn-rerun" on:click|stopPropagation={() => rerunPipeline()}>Re-run</button>
            {/if}
            <a href="/api/runs/{run.id}/logs/export" class="btn-export" on:click|stopPropagation title="Download logs" target="_blank">Logs</a>
            <svg class="expand-icon" width="14" height="14" viewBox="0 0 24 24" fill="none" style="transform: rotate({expandedRunId === run.id ? 90 : 0}deg); transition: transform 150ms ease">
              <path d={icons.chevronRight.d} stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" />
            </svg>
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
                    {selectedRun.node_runs?.filter(n => n.status === "success").length || 0} ok,
                    {selectedRun.node_runs?.filter(n => n.status === "failed").length || 0} failed
                  </span>
                </div>
                {#if selectedRun.error}
                  <div class="summary-item summary-error">
                    <span class="summary-label">Error</span>
                    <span class="summary-value">{selectedRun.error.slice(0, 100)}</span>
                  </div>
                {/if}
              </div>

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
                  <RunTimeline
                    nodeRuns={selectedRun.node_runs}
                    nodes={pipeline?.nodes || []}
                  />
                </div>
              {/if}

              <!-- Data Preview -->
              {#if selectedRun.node_runs && selectedRun.node_runs.length > 0}
                <div class="detail-section">
                  <h3>Data Preview</h3>
                  <div class="preview-tabs">
                    {#each selectedRun.node_runs as nr}
                      {@const node = (pipeline?.nodes || []).find(n => n.id === nr.node_id)}
                      <button
                        class="preview-tab"
                        class:active={previewNodeId === nr.node_id}
                        on:click={() => previewNodeId = previewNodeId === nr.node_id ? null : nr.node_id}
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
                        nodeName={(pipeline?.nodes || []).find(n => n.id === previewNodeId)?.name || ""}
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
                    {#each selectedRun.node_runs as nr}
                      {@const node = (pipeline?.nodes || []).find(n => n.id === nr.node_id)}
                      <button
                        class="preview-tab"
                        class:active={profileNodeId === nr.node_id}
                        on:click={() => loadProfile(selectedRun.id, nr.node_id)}
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
                                <td class:high-null={col.null_pct > 20}>{col.null_pct.toFixed(1)}%</td>
                                <td>{col.unique_pct.toFixed(1)}%</td>
                                <td class="mono">{col.min_val || "—"}</td>
                                <td class="mono">{col.max_val || "—"}</td>
                                <td class="mono">{col.is_numeric ? col.mean_val?.toFixed(2) : "—"}</td>
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
        </div>
      {/each}
    </div>
    <Pagination total={runs.length} page={runPage} pageSize={runPageSize}
      on:page={(e) => runPage = e.detail} on:pagesize={(e) => { runPageSize = e.detail; runPage = 1; }} />
  {/if}

  {#if showParamsModal}
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <div class="modal-overlay" on:click={() => showParamsModal = false} on:keydown={() => {}}>
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
                value={value}
                on:input={(e) => { runParams[key] = e.currentTarget.value; runParams = runParams; }}
              />
              <button class="btn-remove-param" on:click={() => removeParam(key)}>x</button>
            </div>
          {/each}
        </div>
        <button class="btn-sm" on:click={addParam}>+ Add Parameter</button>
        <div class="modal-actions">
          <button class="btn-sm" on:click={() => showParamsModal = false}>Cancel</button>
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
  .back-link { font-size: 0.875rem; color: var(--text-muted); }
  .back-link:hover { color: var(--text-primary); }
  .separator { color: var(--border); }
  .pipeline-name { font-weight: 600; }
  .page-label { color: var(--text-muted); }
  .toolbar-right { display: flex; gap: var(--space-sm); }

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
  .btn-sm:hover { background: var(--border); color: var(--text-primary); }
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
  .run-card:hover { border-color: var(--border-hover); }
  .run-card.expanded { border-color: var(--accent); }

  .run-header {
    display: flex;
    align-items: center;
    gap: var(--space-md);
    padding: var(--space-md) var(--space-lg);
  }
  .run-id { font-size: 0.75rem; }
  .run-time { font-size: 0.8125rem; color: var(--text-secondary); }
  .run-duration { font-size: 0.75rem; color: var(--text-muted); }
  .run-rows { font-size: 0.75rem; color: var(--text-muted); margin-left: auto; }
  .btn-resume {
    padding: 3px 10px;
    border-radius: 4px;
    font-size: 11px;
    font-weight: 500;
    background: var(--warning-bg);
    border: 1px solid rgba(245, 158, 11, 0.3);
    color: #f59e0b;
    transition: all 150ms ease;
  }
  .btn-resume:hover { background: rgba(245, 158, 11, 0.15); }
  .btn-rerun {
    padding: 3px 10px; border-radius: 4px; font-size: 11px; font-weight: 500;
    background: var(--success-bg); color: #22c55e;
    border: 1px solid rgba(34, 197, 94, 0.2);
    transition: background 150ms ease;
  }
  .btn-rerun:hover { background: rgba(34, 197, 94, 0.15); }
  .btn-cancel {
    padding: 3px 10px; border-radius: 4px; font-size: 11px; font-weight: 500;
    background: var(--failed-bg); border: 1px solid rgba(239,68,68,0.3);
    color: var(--failed); transition: all 150ms ease;
  }
  .btn-cancel:hover { background: rgba(239,68,68,0.15); }
  .run-error-hint {
    font-size: 10px; font-weight: 600; color: var(--failed);
    background: var(--failed-bg); padding: 2px 6px; border-radius: 3px;
  }
  .btn-export {
    font-size: 10px; font-weight: 500; color: var(--text-muted);
    background: var(--bg-secondary); padding: 2px 8px; border-radius: 3px;
    border: 1px solid var(--border-subtle); text-decoration: none;
    transition: all 150ms ease;
  }
  .btn-export:hover { color: var(--text-primary); border-color: var(--border-hover); }
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

  .expand-icon { color: var(--text-muted); font-size: 0.75rem; }
  .mono { font-family: var(--font-mono); }

  .run-detail {
    border-top: 1px solid var(--border);
    padding: var(--space-lg);
    cursor: default;
  }

  .detail-section {
    margin-bottom: var(--space-lg);
  }
  .detail-section:last-child { margin-bottom: 0; }
  .detail-section h3 {
    font-size: 0.75rem;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.08em;
    margin-bottom: var(--space-sm);
  }

  .canvas-header {
    display: flex; justify-content: space-between; align-items: center;
    margin-bottom: 8px;
  }
  .canvas-hint {
    font-size: 10px; color: var(--text-dim); font-family: var(--font-mono);
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
  .preview-tab:hover { border-color: var(--text-ghost); color: var(--text-primary); }
  .preview-tab.active {
    border-color: var(--accent);
    background: var(--accent-glow);
    color: var(--accent-text);
  }
  .tab-rows {
    font-family: 'JetBrains Mono', monospace;
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
  .btn-remove-param:hover { background: rgba(239, 68, 68, 0.15); }
  .modal-actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--space-sm);
    margin-top: var(--space-md);
  }

  /* ── Profile ── */
  .profile-loading {
    padding: 12px; font-size: 12px; color: var(--text-muted); text-align: center;
  }
  .profile-summary {
    display: flex; gap: 16px; padding: 10px 0;
    border-bottom: 1px solid var(--border-subtle); margin-bottom: 8px;
  }
  .profile-stat {
    font-family: var(--font-mono); font-size: 12px; font-weight: 600;
    color: var(--text-primary);
  }
  .profile-table-wrap { overflow-x: auto; }
  .profile-table {
    width: 100%; border-collapse: collapse;
    font-size: 11px; font-family: var(--font-mono);
  }
  .profile-table th {
    text-align: left; padding: 5px 8px;
    font-size: 9px; font-weight: 600; text-transform: uppercase;
    letter-spacing: 0.06em; color: var(--text-ghost);
    border-bottom: 1px solid var(--border-subtle);
  }
  .profile-table td {
    padding: 4px 8px; color: var(--text-secondary);
    border-bottom: 1px solid var(--border-subtle);
  }
  .profile-table .col-name { font-weight: 600; color: var(--text-primary); }
  .profile-table .mono { font-family: var(--font-mono); }
  .type-badge {
    font-size: 9px; font-weight: 600; padding: 1px 5px;
    border-radius: 3px; background: var(--bg-tertiary); color: var(--text-muted);
  }
  .high-null { color: var(--warning); font-weight: 600; }

  /* ── Drift ── */
  .drift-section { margin-top: 12px; }
  .drift-section h4 {
    font-size: 10px; font-weight: 600; text-transform: uppercase;
    letter-spacing: 0.08em; color: var(--warning); margin-bottom: 6px;
  }
  .drift-alert {
    display: flex; align-items: center; gap: 8px;
    padding: 5px 8px; border-radius: 4px;
    font-size: 11px; margin-bottom: 3px;
    background: rgba(245, 158, 11, 0.08);
    border: 1px solid rgba(245, 158, 11, 0.15);
  }
  .drift-alert.critical {
    background: var(--failed-bg);
    border-color: rgba(239, 68, 68, 0.2);
  }
  .drift-type {
    font-family: var(--font-mono); font-size: 10px; font-weight: 600;
    padding: 1px 5px; border-radius: 3px;
    background: var(--bg-tertiary); color: var(--warning);
  }
  .drift-alert.critical .drift-type { color: var(--failed); }
  .drift-col { font-weight: 600; color: var(--text-primary); }
  .drift-detail { color: var(--text-muted); font-size: 10px; }

  /* Toolbar buttons: icon + label on desktop, icon-only on mobile */
  .btn-sm {
    display: inline-flex;
    align-items: center;
    gap: 5px;
  }
  .btn-icon { flex-shrink: 0; }

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
    .run-header {
      flex-wrap: wrap;
      gap: var(--space-sm);
    }
    .run-rows { margin-left: 0; }
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
  .history-toggle:hover { color: var(--text-primary); }
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
  .history-row:last-child { border-bottom: none; }
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
    transition: transform 100ms ease, box-shadow 100ms ease;
    background: var(--bg-tertiary);
  }
  .history-cell:hover {
    transform: scale(1.3);
    box-shadow: 0 0 4px rgba(255,255,255,0.1);
  }
  .cell-success { background: var(--success); }
  .cell-failed { background: var(--failed); }
  .cell-running { background: var(--running); animation: pulse-cell 1.5s ease-in-out infinite; }
  .cell-pending { background: var(--pending); }
  @keyframes pulse-cell {
    0%, 100% { opacity: 1; }
    50% { opacity: 0.5; }
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
</style>
