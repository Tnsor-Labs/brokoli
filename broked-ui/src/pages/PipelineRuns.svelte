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
  import type { Pipeline, Run, LogEntry, RunStatus } from "../lib/types";

  export let params: { id?: string } = {};

  let pipeline: Pipeline | null = null;
  let runs: Run[] = [];
  let selectedRun: Run | null = null;
  let logs: LogEntry[] = [];
  let loading = true;
  let expandedRunId: string | null = null;
  let previewNodeId: string | null = null;
  let showBackfill = false;
  let backfillStart = "";
  let backfillEnd = "";
  let backfilling = false;

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
            selectedRun = await api.runs.get(expandedRunId);
            logs = await api.runs.getLogs(expandedRunId);
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

  async function triggerRun() {
    if (!pipeline) return;
    try {
      await api.runs.trigger(pipeline.id);
      runs = await api.runs.listByPipeline(pipeline.id);
    } catch (e) {
      notify.error("Failed to trigger run");
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
      <a href="#/pipelines/{params.id}/edit" class="btn-sm">Edit Pipeline</a>
      <button class="btn-sm" on:click={() => showBackfill = !showBackfill}>Backfill</button>
      <button class="btn-sm btn-run" on:click={triggerRun}>Run Now</button>
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

  {#if loading}
    <div class="empty-state">Loading...</div>
  {:else if runs.length === 0}
    <div class="empty-state">
      <p>No runs yet for this pipeline.</p>
      <button class="btn-primary" on:click={triggerRun}>Trigger First Run</button>
    </div>
  {:else}
    <div class="runs-list">
      {#each runs as run}
        <!-- svelte-ignore a11y_no_static_element_interactions -->
        <div class="run-card" class:expanded={expandedRunId === run.id} on:click={() => selectRun(run)} on:keydown={() => {}}>
          <div class="run-header">
            <StatusBadge status={run.status} />
            <span class="run-id mono">{run.id.slice(0, 8)}</span>
            <span class="run-time">{formatTime(run.started_at)}</span>
            <span class="run-duration mono">{formatDuration(run)}</span>
            <span class="run-rows mono">{totalRows(run)} rows</span>
            {#if run.status === "failed"}
              <span class="run-error-hint" title={run.error || "Check logs for details"}>Error</span>
              <button class="btn-resume" on:click|stopPropagation={() => resumeRun(run.id)}>Resume</button>
            {/if}
            <a href="/api/runs/{run.id}/logs/export" class="btn-export" on:click|stopPropagation title="Download logs" target="_blank">Logs</a>
            <svg class="expand-icon" width="14" height="14" viewBox="0 0 24 24" fill="none" style="transform: rotate({expandedRunId === run.id ? 90 : 0}deg); transition: transform 150ms ease">
              <path d={icons.chevronRight.d} stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" />
            </svg>
          </div>

          {#if expandedRunId === run.id && selectedRun}
            <!-- svelte-ignore a11y_no_static_element_interactions -->
            <div class="run-detail" on:click|stopPropagation on:keydown={() => {}}>
              <!-- DAG with live status -->
              {#if pipeline && pipeline.nodes.length > 0}
                <div class="detail-section">
                  <h3>Pipeline Status</h3>
                  <div class="canvas-mini">
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

  .canvas-mini {
    height: 200px;
    border-radius: var(--radius-md);
    overflow: hidden;
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
</style>
