<script lang="ts">
  import { onMount } from "svelte";
  import { push } from "svelte-spa-router";
  import { api } from "../lib/api";
  import { notify } from "../lib/toast";
  import Breadcrumb from "../components/Breadcrumb.svelte";
  import Skeleton from "../components/Skeleton.svelte";
  import type { Pipeline, PipelineGrid } from "../lib/types";

  export let params: { id?: string } = {};

  let pipeline: Pipeline | null = null;
  let grid: PipelineGrid | null = null;
  let loading = true;
  let runCount = 30;

  async function load() {
    if (!params.id) return;
    loading = true;
    try {
      [pipeline, grid] = await Promise.all([
        api.pipelines.get(params.id),
        api.pipelines.grid(params.id, runCount),
      ]);
    } catch (e: any) {
      notify.error("Failed to load grid: " + (e.message || e));
    } finally {
      loading = false;
    }
  }
  onMount(load);

  function cellFor(runId: string, nodeId: string) {
    return grid?.cells?.[runId]?.[nodeId] || null;
  }

  function colTitle(run: PipelineGrid["runs"][number]): string {
    const parts = [run.started_at ? new Date(run.started_at).toLocaleString() : "not started"];
    if (run.trigger) parts.push("trigger: " + run.trigger);
    if (run.data_interval_start && run.data_interval_end)
      parts.push(`interval: ${run.data_interval_start} .. ${run.data_interval_end}`);
    parts.push("v" + run.pipeline_version);
    return parts.join("\n");
  }

  function cellTitle(runId: string, nodeId: string): string {
    const c = cellFor(runId, nodeId);
    if (!c) return "did not run";
    const parts = [c.status, `${c.duration_ms}ms`, `${c.row_count} rows`];
    if (c.attempt > 0) parts.push(`attempt ${c.attempt}`);
    if (c.error) parts.push(c.error);
    return parts.join("\n");
  }

  // A version boundary between columns is a diagnosis marker: a behavior
  // change lining up with a deploy is the answer half the time.
  function versionBoundary(i: number): boolean {
    if (!grid || i === 0) return false;
    return grid.runs[i].pipeline_version !== grid.runs[i - 1].pipeline_version;
  }

  function openRun(runId: string) {
    push(`/pipelines/${params.id}/runs?run=${runId}`);
  }

  function shortTime(iso: string | null | undefined): string {
    if (!iso) return "-";
    const d = new Date(iso);
    return (
      String(d.getMonth() + 1).padStart(2, "0") +
      "/" +
      String(d.getDate()).padStart(2, "0") +
      " " +
      String(d.getHours()).padStart(2, "0") +
      ":" +
      String(d.getMinutes()).padStart(2, "0")
    );
  }
</script>

<div class="page">
  <Breadcrumb
    items={[
      { label: "Pipelines", href: "#/pipelines" },
      { label: pipeline?.name || "...", href: `#/pipelines/${params.id}/runs` },
      { label: "Grid" },
    ]}
  />

  <div class="grid-toolbar">
    <h2>Grid</h2>
    <span class="hint"
      >runs x nodes, newest right; a red row is a broken node, a red column a bad day</span
    >
    <label class="run-count">
      show
      <select
        value={runCount}
        on:change={(e) => {
          runCount = Number(e.currentTarget.value);
          load();
        }}
      >
        <option value={15}>15</option>
        <option value={30}>30</option>
        <option value={60}>60</option>
        <option value={100}>100</option>
      </select>
      runs
    </label>
    <button class="btn-sm" on:click={load}>Refresh</button>
  </div>

  {#if loading}
    <div class="skeleton-stack">
      {#each Array(6) as _}
        <Skeleton height="22px" />
      {/each}
    </div>
  {:else if !grid || grid.runs.length === 0}
    <p class="empty">No runs yet. The grid appears once this pipeline has history.</p>
  {:else}
    <div class="grid-scroll">
      <table class="grid-table">
        <thead>
          <tr>
            <th class="node-col">node</th>
            {#each grid.runs as run, i}
              <th class="run-col" class:version-boundary={versionBoundary(i)} title={colTitle(run)}>
                <button class="col-head status-{run.status}" on:click={() => openRun(run.id)}>
                  <span class="col-time">{shortTime(run.started_at)}</span>
                  {#if run.trigger === "backfill"}<span class="trigger-mark">B</span>{/if}
                  {#if versionBoundary(i)}<span class="version-mark">v{run.pipeline_version}</span
                    >{/if}
                </button>
              </th>
            {/each}
          </tr>
        </thead>
        <tbody>
          {#each grid.nodes as node}
            <tr>
              <td class="node-col" title={node.type}>{node.name}</td>
              {#each grid.runs as run, i}
                <td class="cell-col" class:version-boundary={versionBoundary(i)}>
                  {#if cellFor(run.id, node.id)}
                    <button
                      class="cell status-{cellFor(run.id, node.id)?.status}"
                      title={cellTitle(run.id, node.id)}
                      on:click={() => openRun(run.id)}
                      aria-label={`${node.name} in run ${shortTime(run.started_at)}: ${cellFor(run.id, node.id)?.status}`}
                    ></button>
                  {:else}
                    <span class="cell absent" title="did not run"></span>
                  {/if}
                </td>
              {/each}
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</div>

<style>
  .page {
    padding: 16px 20px;
  }
  .grid-toolbar {
    display: flex;
    align-items: center;
    gap: 12px;
    margin: 10px 0 14px;
  }
  .grid-toolbar h2 {
    margin: 0;
    font-size: 18px;
  }
  .hint {
    color: var(--text-secondary, #888);
    font-size: 12px;
  }
  .run-count {
    margin-left: auto;
    font-size: 12px;
    color: var(--text-secondary, #888);
    display: inline-flex;
    align-items: center;
    gap: 6px;
  }
  .skeleton-stack {
    display: flex;
    flex-direction: column;
    gap: 8px;
    max-width: 640px;
  }
  .empty {
    color: var(--text-secondary, #888);
  }
  .grid-scroll {
    overflow-x: auto;
    border: 1px solid var(--border-color, #333);
    border-radius: 6px;
  }
  .grid-table {
    border-collapse: collapse;
    font-size: 12px;
  }
  .node-col {
    position: sticky;
    left: 0;
    background: var(--bg-secondary, #1c1c1c);
    text-align: left;
    padding: 6px 12px;
    white-space: nowrap;
    max-width: 220px;
    overflow: hidden;
    text-overflow: ellipsis;
    z-index: 1;
    border-right: 1px solid var(--border-color, #333);
  }
  th.node-col {
    color: var(--text-secondary, #888);
    font-weight: 500;
  }
  .run-col,
  .cell-col {
    padding: 3px;
    text-align: center;
  }
  .version-boundary {
    border-left: 2px dashed var(--accent-color, #7a5cff);
  }
  .col-head {
    background: none;
    border: none;
    color: var(--text-secondary, #888);
    font-size: 10px;
    cursor: pointer;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 2px;
    padding: 4px 6px;
  }
  .col-head.status-failed .col-time {
    color: var(--status-failed, #e5534b);
  }
  .trigger-mark {
    font-size: 9px;
    border: 1px solid currentColor;
    border-radius: 3px;
    padding: 0 3px;
    color: var(--accent-color, #7a5cff);
  }
  .version-mark {
    font-size: 9px;
    color: var(--accent-color, #7a5cff);
  }
  .cell {
    display: inline-block;
    width: 16px;
    height: 16px;
    border-radius: 3px;
    border: none;
    cursor: pointer;
    padding: 0;
  }
  .cell.status-success {
    background: var(--status-success, #3fb950);
  }
  .cell.status-failed {
    background: var(--status-failed, #e5534b);
  }
  .cell.status-running {
    background: var(--status-running, #d29922);
    animation: pulse 1.2s ease-in-out infinite;
  }
  .cell.status-cancelled {
    background: var(--text-secondary, #888);
  }
  .cell.status-skipped {
    background: repeating-linear-gradient(
      45deg,
      var(--bg-secondary, #2a2a2a),
      var(--bg-secondary, #2a2a2a) 3px,
      var(--text-secondary, #666) 3px,
      var(--text-secondary, #666) 6px
    );
  }
  .cell.absent {
    background: transparent;
    border: 1px dashed var(--border-color, #333);
    cursor: default;
  }
  .cell:focus-visible {
    outline: 2px solid var(--accent-color, #7a5cff);
    outline-offset: 1px;
  }
  @keyframes pulse {
    50% {
      opacity: 0.5;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .cell.status-running {
      animation: none;
    }
  }
</style>
