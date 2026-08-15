<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import type { Pipeline, Run, NodeRun, RunStatus, NodeStats } from "../lib/types";
  import { authHeaders } from "../lib/auth";
  import StatusBadge from "../components/StatusBadge.svelte";
  import Skeleton from "../components/Skeleton.svelte";
  import { Timeline, DataSet } from "vis-timeline/standalone";
  import "vis-timeline/styles/vis-timeline-graph2d.css";
  import Arrow from "timeline-arrows";

  export let params: { id?: string; runId?: string } = {};

  let pipeline: Pipeline | null = null;
  let run: Run | null = null;
  let logs: { node_id: string; level: string; message: string }[] = [];
  let nodeStats: NodeStats = {};
  let loading = true;
  let error = "";
  let selectedNodeId: string | null = null;

  let containerEl: HTMLDivElement;
  let timeline: any = null;

  // Group node_runs
  $: nodeGroups = (() => {
    const m = new Map<string, NodeRun[]>();
    for (const nr of run?.node_runs || []) {
      if (!m.has(nr.node_id)) m.set(nr.node_id, []);
      m.get(nr.node_id)!.push(nr);
    }
    for (const [, a] of m) a.sort((x, y) => (x.attempt ?? 0) - (y.attempt ?? 0));
    return m;
  })();

  $: primaryRuns = [...nodeGroups.entries()]
    .map(([, nrs]) => nrs[nrs.length - 1])
    .filter((nr) => nr.started_at || nr.duration_ms > 0 || nr.status === "skipped")
    .sort((a, b) => (a.started_at || "").localeCompare(b.started_at || ""));

  $: totalMs = primaryRuns.length
    ? Math.max(
        ...primaryRuns.map((nr) => {
          const s = nr.started_at ? new Date(nr.started_at).getTime() : 0;
          return s + nr.duration_ms;
        }),
      ) - (run?.started_at ? new Date(run.started_at).getTime() : 0)
    : 1000;

  $: totalRows = primaryRuns.reduce((s, r) => s + r.row_count, 0);

  // Detail panel
  $: sel = selectedNodeId ? primaryRuns.find((r) => r.node_id === selectedNodeId) : null;
  $: selNode = selectedNodeId ? pipeline?.nodes.find((n) => n.id === selectedNodeId) : null;
  $: selAttempts = selectedNodeId ? nodeGroups.get(selectedNodeId) || [] : [];
  $: selLogs = selectedNodeId ? logs.filter((l) => l.node_id === selectedNodeId) : [];

  function fmt(ms: number): string {
    if (ms < 1000) return `${Math.round(ms)}ms`;
    if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`;
    return `${Math.floor(ms / 60000)}m${Math.floor((ms % 60000) / 1000)}s`;
  }
  function fmtRows(n: number): string {
    if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`;
    if (n >= 1e3) return `${(n / 1e3).toFixed(1)}K`;
    return String(Math.round(n));
  }
  function fmtRps(n: number): string {
    if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`;
    if (n >= 1e3) return `${(n / 1e3).toFixed(1)}K`;
    return String(Math.round(n));
  }

  function statusClass(s: RunStatus): string {
    if (s === "success") return "bar-success";
    if (s === "failed") return "bar-failed";
    if (s === "running") return "bar-running";
    if (s === "skipped") return "bar-skipped";
    return "bar-pending";
  }

  function renderTimeline() {
    if (!containerEl || !primaryRuns.length || !pipeline) return;

    const epoch = run?.started_at ? new Date(run.started_at).getTime() : Date.now();

    const { groups, items, arrows, options } = buildTimelineData(
      pipeline!,
      primaryRuns,
      nodeGroups,
      epoch,
      totalMs,
    );

    timeline = new Timeline(containerEl, items, groups, options);

    if (arrows.length > 0) {
      try {
        new Arrow(timeline, arrows, {
          color: "var(--text-muted)",
          strokeWidth: 1.5,
          arrowEnd: true,
        });
      } catch (e) {
        console.warn("Arrow plugin:", e);
      }
    }

    timeline.on("select", (props: any) => {
      const ids = props.items || [];
      selectedNodeId = ids.length > 0 ? ids[0] : null;
    });
  }

  // ── Modular timeline data builder ──
  function buildTimelineData(
    pipe: Pipeline,
    runs: NodeRun[],
    groups_map: Map<string, NodeRun[]>,
    epoch: number,
    totalMs: number,
  ) {
    const nodeMap = new Map(pipe.nodes.map((n) => [n.id, n]));

    // Groups (left panel rows)
    const groups = new DataSet(
      runs.map((nr, i) => {
        const node = nodeMap.get(nr.node_id);
        const attempts = groups_map.get(nr.node_id) || [];
        const retryBadge =
          attempts.length > 1 ? `<span class="g-badge retry">R${attempts.length}</span>` : "";
        const sc =
          nr.status === "failed"
            ? "var(--failed)"
            : nr.status === "running"
              ? "var(--running)"
              : nr.status === "skipped"
                ? "var(--pending)"
                : "var(--success)";

        return {
          id: nr.node_id,
          content: `<div class="gl">
            <span class="gl-dot" style="background:${sc}"></span>
            <span class="gl-name">${node?.name || nr.node_id}</span>
            ${retryBadge}
            <span class="gl-dur">${fmt(nr.duration_ms)}</span>
          </div>`,
          order: i,
        };
      }),
    );

    // Items (bars)
    const items = new DataSet(
      runs.map((nr) => {
        const start = nr.started_at ? new Date(nr.started_at).getTime() : epoch;
        const end = start + Math.max(nr.duration_ms, 1);
        const node = nodeMap.get(nr.node_id);
        const rowLabel = nr.row_count > 0 ? ` · ${fmtRows(nr.row_count)}` : "";

        return {
          id: nr.node_id,
          group: nr.node_id,
          content: `<div class="bar-inner">
            <span class="bar-label">${node?.name || nr.node_id}</span>
            <span class="bar-stats">${fmt(nr.duration_ms)}${rowLabel}</span>
          </div>`,
          start: new Date(start),
          end: new Date(end),
          className: statusClass(nr.status),
          title: `${node?.name || nr.node_id}\nDuration: ${fmt(nr.duration_ms)}\nRows: ${nr.row_count.toLocaleString()}${nr.rows_per_sec ? "\nThroughput: " + fmtRps(nr.rows_per_sec) + "/s" : ""}`,
        };
      }),
    );

    // Arrows (dependencies)
    const runNodeIds = new Set(runs.map((r) => r.node_id));
    const arrows = (pipe.edges || [])
      .filter((e) => runNodeIds.has(e.from) && runNodeIds.has(e.to))
      .map((e, i) => ({ id: i, id_item_1: e.from, id_item_2: e.to }));

    // Options
    const tStart = new Date(epoch - totalMs * 0.05);
    const tEnd = new Date(epoch + totalMs * 1.15);

    const toMs = (date: any): number =>
      date instanceof Date
        ? date.getTime()
        : typeof date === "number"
          ? date
          : new Date(date).getTime();

    const options: any = {
      start: tStart,
      end: tEnd,
      min: new Date(epoch - totalMs * 0.5),
      max: new Date(epoch + totalMs * 2),
      orientation: "top",
      stack: false,
      showCurrentTime: false,
      zoomMin: Math.max(5, totalMs * 0.05),
      zoomMax: totalMs * 10,
      margin: { item: { horizontal: 0, vertical: 6 } },
      selectable: true,
      multiselect: false,
      editable: false,
      height: "100%",
      groupHeightMode: "fixed",
      format: {
        minorLabels: (date: any) => fmt(Math.max(0, toMs(date) - epoch)),
        majorLabels: (date: any) => {
          const ms = toMs(date) - epoch;
          return ms < 0 ? "" : fmt(ms);
        },
      },
      groupOrder: "order",
      tooltip: { followMouse: true, overflowMethod: "cap" },
    };

    // Adaptive time scale
    if (totalMs < 500) {
      options.timeAxis = { scale: "millisecond", step: Math.max(1, Math.round(totalMs / 10)) };
    } else if (totalMs < 5000) {
      options.timeAxis = { scale: "millisecond", step: Math.max(10, Math.round(totalMs / 10)) };
    } else if (totalMs < 60000) {
      options.timeAxis = { scale: "millisecond", step: Math.max(50, Math.round(totalMs / 12)) };
    } else if (totalMs < 3600000) {
      options.timeAxis = { scale: "second", step: Math.max(1, Math.round(totalMs / 60000)) };
    } else {
      options.timeAxis = { scale: "minute", step: 1 };
    }

    return { groups, items, arrows, options };
  }

  onMount(async () => {
    if (!params.id || !params.runId) {
      error = "Missing params";
      loading = false;
      return;
    }
    try {
      const [pR, rR, lR, sR] = await Promise.all([
        fetch(`/api/pipelines/${params.id}`, { headers: authHeaders() }),
        fetch(`/api/runs/${params.runId}`, { headers: authHeaders() }),
        fetch(`/api/runs/${params.runId}/logs`, { headers: authHeaders() }),
        fetch(`/api/pipelines/${params.id}/node-stats?runs=10`, { headers: authHeaders() }).catch(
          () => null,
        ),
      ]);
      if (pR.ok) pipeline = await pR.json();
      if (rR.ok) run = await rR.json();
      if (lR.ok) {
        const d = await lR.json();
        logs = Array.isArray(d) ? d : d.logs || [];
      }
      if (sR?.ok) {
        const d = await sR.json();
        nodeStats = d?.nodes || d || {};
      }
      if (!pipeline) error = "Pipeline not found";
      else if (!run) error = "Run not found";
    } catch (e: any) {
      error = e.message || "Failed";
    }
    loading = false;
    // Render after DOM update
    requestAnimationFrame(() => renderTimeline());
  });

  onDestroy(() => {
    if (timeline) {
      timeline.destroy();
      timeline = null;
    }
  });
</script>

<div class="page animate-in">
  <header class="page-header">
    <div class="header-copy">
      <a href="#/pipelines/{params.id}/runs" class="back-link">
        <svg
          width="14"
          height="14"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          stroke-width="2"><path d="M15 18l-6-6 6-6" /></svg
        >
        Back to Runs
      </a>
      <p class="eyebrow">Execution timeline</p>
      <div class="title-row">
        <h1>{pipeline?.name || "Run timeline"}</h1>
        {#if run}<StatusBadge status={run.status} size="sm" />{/if}
      </div>
      <p class="page-subtitle">
        Run <code>{params.runId?.slice(0, 8) || "..."}</code> · Inspect node timing, throughput, and retries.
      </p>
    </div>
    {#if run}
      <div class="run-metrics" aria-label="Run summary">
        <div class="metric">
          <span class="metric-value">{fmt(totalMs)}</span><span class="metric-label">Duration</span>
        </div>
        <div class="metric">
          <span class="metric-value">{primaryRuns.length}</span><span class="metric-label"
            >Nodes</span
          >
        </div>
        <div class="metric">
          <span class="metric-value">{fmtRows(totalRows)}</span><span class="metric-label"
            >Rows</span
          >
        </div>
        {#if run?.trace_id}
          <div class="metric">
            <span class="metric-value trace">{run.trace_id.slice(0, 12)}</span><span
              class="metric-label">Trace</span
            >
          </div>
        {/if}
      </div>
    {/if}
  </header>

  {#if loading}
    <div class="state-card loading-state" aria-label="Loading execution timeline">
      <div class="state-heading">
        <span class="state-icon">↦</span>
        <div>
          <strong>Loading execution timeline</strong><small
            >Preparing node timings and run telemetry...</small
          >
        </div>
      </div>
      <Skeleton height="420px" />
    </div>
  {:else if error}
    <div class="state-card empty-state error-state">
      <span class="empty-icon">!</span>
      <h2>Timeline unavailable</h2>
      <p>{error}</p>
      <a href="#/pipelines/{params.id}/runs">Return to runs</a>
    </div>
  {:else if primaryRuns.length === 0}
    <div class="state-card empty-state">
      <span class="empty-icon">↦</span>
      <h2>No node timing data</h2>
      <p>This run has not produced execution timing data yet.</p>
      <a href="#/pipelines/{params.id}/runs">Return to runs</a>
    </div>
  {:else}
    <section class="workspace" aria-label="Execution timeline workspace">
      <div class="workspace-toolbar">
        <div>
          <span class="workspace-title">Node execution</span><span class="workspace-hint"
            >Select a bar to inspect details</span
          >
        </div>
        <div class="status-legend" aria-label="Execution statuses">
          <span><i class="success"></i>Succeeded</span>
          <span><i class="running"></i>Running</span>
          <span><i class="failed"></i>Failed</span>
          <span><i class="skipped"></i>Skipped</span>
        </div>
      </div>
      <div class="body" class:has-detail={!!sel}>
        <div class="timeline-container" bind:this={containerEl}></div>
      </div>

      {#if sel && selNode}
        <div class="detail">
          <div class="d-head">
            <div class="d-title">
              <span class="d-dot status-{sel.status}"></span>
              <div class="d-identity">
                <span class="d-label">Selected node</span><span class="d-name">{selNode.name}</span>
              </div>
              <StatusBadge status={sel.status} size="sm" />
              <span class="d-type">{selNode.type}</span>
            </div>
            <button
              class="d-close"
              aria-label="Close node details"
              on:click={() => {
                selectedNodeId = null;
                if (timeline) timeline.setSelection([]);
              }}
            >
              <svg
                width="14"
                height="14"
                viewBox="0 0 24 24"
                fill="none"
                stroke="currentColor"
                stroke-width="2"><path d="M18 6L6 18M6 6l12 12" /></svg
              >
            </button>
          </div>
          <div class="d-stats">
            <div class="d-kv">
              <span class="d-v">{fmt(sel.duration_ms)}</span><span class="d-k">Duration</span>
            </div>
            <div class="d-kv">
              <span class="d-v">{sel.row_count.toLocaleString()}</span><span class="d-k">Rows</span>
            </div>
            <div class="d-kv">
              <span class="d-v"
                >{sel.started_at
                  ? new Date(sel.started_at).toLocaleTimeString("en-US", { hour12: false })
                  : "—"}</span
              ><span class="d-k">Started</span>
            </div>
            {#if sel.rows_per_sec}<div class="d-kv">
                <span class="d-v">{fmtRows(sel.rows_per_sec)}/s</span><span class="d-k"
                  >Throughput</span
                >
              </div>{/if}
            {#if sel.queue_ms}<div class="d-kv">
                <span class="d-v">{fmt(sel.queue_ms)}</span><span class="d-k">Queue</span>
              </div>{/if}
            {#if sel.trace_id}<div class="d-kv">
                <span class="d-v trace">{sel.trace_id.slice(0, 12)}</span><span class="d-k"
                  >Trace</span
                >
              </div>{/if}
            {#if sel.error}<div class="d-kv err">
                <span class="d-v">{sel.error}</span><span class="d-k">Error</span>
              </div>{/if}
          </div>
          {#if selAttempts.length > 1}
            <div class="d-attempts">
              {#each selAttempts as att, i}
                <div class="d-att" class:att-fail={att.status === "failed"}>
                  <span class="att-n">A{i}</span><StatusBadge status={att.status} size="sm" /><span
                    class="att-d">{fmt(att.duration_ms)}</span
                  >
                </div>
              {/each}
            </div>
          {/if}
          <div class="d-logs">
            {#each selLogs as l}
              <div
                class="log"
                class:log-e={l.level === "error"}
                class:log-w={l.level === "warning"}
              >
                <span class="log-l">{l.level}</span><span class="log-m">{l.message}</span>
              </div>
            {/each}
            {#if selLogs.length === 0}<div class="log">
                <span class="log-m no-logs">No logs for this node.</span>
              </div>{/if}
          </div>
        </div>
      {/if}
    </section>
  {/if}
</div>

<style>
  .page {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-height: 0;
    color: var(--text-primary);
  }

  .page-header {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
    gap: 24px;
    margin-bottom: 18px;
    flex-shrink: 0;
  }
  .header-copy {
    min-width: 0;
  }
  .back-link {
    display: flex;
    align-items: center;
    gap: 4px;
    width: fit-content;
    margin-bottom: 14px;
    color: var(--text-muted);
    text-decoration: none;
    font-size: 11px;
    font-weight: 500;
  }
  .back-link:hover {
    color: var(--accent);
  }
  .eyebrow {
    margin-bottom: 5px;
    color: var(--accent);
    font: 650 9px var(--font-mono);
    letter-spacing: 0.14em;
    text-transform: uppercase;
  }
  .title-row {
    display: flex;
    min-width: 0;
    align-items: center;
    gap: 10px;
  }
  .title-row h1 {
    overflow: hidden;
    font-size: 24px;
    font-weight: 650;
    letter-spacing: -0.035em;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .page-subtitle {
    margin-top: 4px;
    color: var(--text-muted);
    font-size: 12px;
  }
  .page-subtitle code {
    color: var(--text-secondary);
    font-size: 10px;
  }
  .run-metrics {
    display: flex;
    flex: none;
    overflow: hidden;
    border: 1px solid var(--border-subtle);
    border-radius: 8px;
    background: var(--bg-secondary);
    box-shadow: var(--shadow-card);
  }
  .metric {
    display: flex;
    min-width: 76px;
    flex-direction: column;
    justify-content: center;
    padding: 10px 13px;
    border-right: 1px solid var(--border-subtle);
  }
  .metric:last-child {
    border-right: 0;
  }
  .metric-value {
    font-size: 11px;
    font-family: var(--font-mono);
    font-weight: 600;
  }
  .metric-value.trace {
    font-size: 9px;
    color: var(--text-muted);
  }
  .metric-label {
    font-size: 8px;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--text-dim);
  }

  .workspace {
    display: flex;
    min-height: 0;
    flex: 1;
    flex-direction: column;
    overflow: hidden;
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius-lg);
    background: var(--bg-secondary);
    box-shadow: var(--shadow-card);
  }
  .workspace-toolbar {
    display: flex;
    min-height: 42px;
    flex: none;
    align-items: center;
    justify-content: space-between;
    gap: 16px;
    padding: 0 14px;
    border-bottom: 1px solid var(--border-subtle);
  }
  .workspace-toolbar > div {
    display: flex;
    align-items: center;
    gap: 12px;
  }
  .workspace-title {
    color: var(--text-secondary);
    font-size: 11px;
    font-weight: 620;
  }
  .workspace-hint {
    color: var(--text-dim);
    font: 9px var(--font-mono);
  }
  .status-legend {
    color: var(--text-dim);
    font-size: 9px;
  }
  .status-legend span {
    display: flex;
    align-items: center;
    gap: 5px;
  }
  .status-legend i {
    width: 7px;
    height: 7px;
    border-radius: 2px;
    background: var(--pending);
  }
  .status-legend i.success {
    background: var(--success);
  }
  .status-legend i.running {
    background: var(--running);
  }
  .status-legend i.failed {
    background: var(--failed);
  }
  .status-legend i.skipped {
    background: var(--pending);
    opacity: 0.65;
  }

  .body {
    flex: 1;
    min-height: 0;
    overflow: hidden;
  }
  .body.has-detail {
    flex: 0.65;
  }

  .timeline-container {
    width: 100%;
    height: 100%;
  }

  /* ─── vis-timeline premium dark theme ─── */

  /* Canvas */
  .timeline-container :global(.vis-timeline) {
    border: none;
    background: var(--bg-primary);
    font-family: var(--font-ui);
  }
  .timeline-container :global(.vis-panel.vis-top) {
    border-bottom: 1px solid var(--border-subtle);
    background: var(--bg-secondary);
  }
  .timeline-container :global(.vis-panel.vis-bottom) {
    border-top: 1px solid var(--border-subtle);
  }
  .timeline-container :global(.vis-panel.vis-left) {
    border-right: 1px solid var(--border);
    background: var(--bg-secondary);
  }
  .timeline-container :global(.vis-panel.vis-center) {
    border-left: none;
  }

  /* Group labels (left panel) */
  .timeline-container :global(.vis-labelset .vis-label) {
    background: var(--bg-secondary);
    border-bottom: 1px solid var(--border-subtle);
    color: var(--text-primary);
    padding: 0;
  }
  .timeline-container :global(.vis-labelset .vis-label:hover) {
    background: var(--bg-tertiary);
  }
  .timeline-container :global(.vis-labelset .vis-label .vis-inner) {
    padding: 0;
    margin: 0;
  }

  /* Group label inner structure */
  /* Group label — single line: dot + name + duration */
  .timeline-container :global(.gl) {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 0 12px;
    height: 100%;
  }
  .timeline-container :global(.gl-dot) {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    flex-shrink: 0;
  }
  .timeline-container :global(.gl-name) {
    font-size: 12px;
    font-weight: 500;
    color: var(--text-primary);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    flex: 1;
  }
  .timeline-container :global(.gl-dur) {
    font-size: 10px;
    font-family: var(--font-mono);
    color: var(--text-ghost);
    flex-shrink: 0;
  }
  .timeline-container :global(.g-badge) {
    font-size: 7px;
    font-weight: 700;
    padding: 1px 4px;
    border-radius: 2px;
    text-transform: uppercase;
    flex-shrink: 0;
  }
  .timeline-container :global(.g-badge.retry) {
    color: var(--running);
    background: var(--running-bg);
  }

  /* Row stripes */
  .timeline-container :global(.vis-foreground .vis-group) {
    border-bottom: 1px solid var(--border-subtle);
  }
  .timeline-container :global(.vis-foreground .vis-group:nth-child(even)) {
    background: var(--bg-secondary);
  }

  /* Time axis */
  .timeline-container :global(.vis-time-axis) {
    background: var(--bg-secondary);
  }
  .timeline-container :global(.vis-time-axis .vis-text) {
    color: var(--text-muted);
    font-family: var(--font-mono);
    font-size: 10px;
    font-weight: 500;
  }
  .timeline-container :global(.vis-time-axis .vis-text.vis-major) {
    font-weight: 600;
    color: var(--text-secondary);
  }
  .timeline-container :global(.vis-time-axis .vis-grid.vis-minor) {
    border-color: var(--border-subtle);
    opacity: 0.2;
  }
  .timeline-container :global(.vis-time-axis .vis-grid.vis-major) {
    border-color: var(--border-subtle);
    opacity: 0.4;
  }

  /* Bars */
  .timeline-container :global(.vis-item) {
    border-radius: 6px;
    border: none;
    font-family: var(--font-mono);
    font-size: 11px;
    color: white;
    box-shadow: 0 1px 4px rgba(0, 0, 0, 0.2);
    transition:
      box-shadow 150ms,
      transform 150ms;
  }
  .timeline-container :global(.vis-item:hover) {
    box-shadow: 0 2px 8px rgba(0, 0, 0, 0.3);
    transform: translateY(-1px);
  }
  .timeline-container :global(.vis-item.bar-success) {
    background: linear-gradient(
      135deg,
      var(--success),
      color-mix(in srgb, var(--success), black 18%)
    );
  }
  .timeline-container :global(.vis-item.bar-success .vis-item-overflow) {
    background: transparent;
  }
  .timeline-container :global(.vis-item.bar-failed) {
    background: linear-gradient(
      135deg,
      var(--failed),
      color-mix(in srgb, var(--failed), black 18%)
    );
  }
  .timeline-container :global(.vis-item.bar-failed .vis-item-overflow) {
    background: transparent;
  }
  .timeline-container :global(.vis-item.bar-running) {
    background: linear-gradient(
      135deg,
      var(--running),
      color-mix(in srgb, var(--running), black 18%)
    );
  }
  .timeline-container :global(.vis-item.bar-running .vis-item-overflow) {
    background: transparent;
  }
  .timeline-container :global(.vis-item.bar-pending) {
    background: linear-gradient(
      135deg,
      var(--pending),
      color-mix(in srgb, var(--pending), black 18%)
    );
  }
  .timeline-container :global(.vis-item.bar-skipped) {
    background: linear-gradient(
      135deg,
      var(--pending),
      color-mix(in srgb, var(--pending), black 18%)
    );
    opacity: 0.65;
  }
  .timeline-container :global(.vis-item.vis-selected) {
    box-shadow:
      0 0 0 2px var(--accent),
      0 2px 12px rgba(13, 148, 136, 0.3);
    z-index: 10;
  }
  .timeline-container :global(.vis-item .vis-item-content) {
    padding: 4px 10px;
  }
  .timeline-container :global(.vis-item .vis-item-visible-frame) {
    border-radius: 6px;
  }

  /* Bar inner content */
  .timeline-container :global(.bar-inner) {
    display: flex;
    align-items: center;
    gap: 8px;
    white-space: nowrap;
    overflow: hidden;
  }
  .timeline-container :global(.bar-label) {
    font-weight: 600;
    font-size: 10.5px;
    opacity: 0.95;
  }
  .timeline-container :global(.bar-stats) {
    font-weight: 500;
    font-size: 9.5px;
    opacity: 0.75;
  }

  /* Arrows */
  .timeline-container :global(.arrow-line) {
    stroke: var(--text-muted);
    stroke-width: 1.5;
    opacity: 0.6;
  }
  .timeline-container :global(.arrow-head) {
    fill: var(--text-muted);
    opacity: 0.6;
  }

  /* Tooltip */
  .timeline-container :global(.vis-tooltip) {
    background: var(--bg-secondary) !important;
    color: var(--text-primary) !important;
    border: 1px solid var(--border-subtle) !important;
    border-radius: 6px !important;
    padding: 8px 12px !important;
    font-family: var(--font-mono) !important;
    font-size: 11px !important;
    box-shadow: 0 4px 16px rgba(0, 0, 0, 0.3) !important;
    white-space: pre-line !important;
  }

  /* Hide drag handles */
  .timeline-container :global(.vis-item .vis-drag-left),
  .timeline-container :global(.vis-item .vis-drag-right),
  .timeline-container :global(.vis-item .vis-drag-center) {
    display: none;
  }

  .state-card {
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius-lg);
    background: var(--bg-secondary);
    box-shadow: var(--shadow-card);
  }
  .loading-state {
    display: flex;
    flex-direction: column;
    gap: var(--space-md);
    padding: var(--space-md);
  }
  .state-heading {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 2px;
  }
  .state-heading div {
    display: flex;
    flex-direction: column;
  }
  .state-heading strong {
    font-size: 12px;
    font-weight: 600;
  }
  .state-heading small {
    color: var(--text-muted);
    font-size: 10px;
  }
  .state-icon,
  .empty-icon {
    color: var(--accent);
    font: 18px var(--font-mono);
  }
  .empty-state {
    padding: 72px var(--space-xl);
    text-align: center;
    color: var(--text-muted);
  }
  .empty-state h2 {
    margin-top: 8px;
    color: var(--text-primary);
    font-size: 16px;
    font-weight: 620;
  }
  .empty-state p {
    margin-top: 4px;
    font-size: 12px;
  }
  .empty-state a {
    display: inline-block;
    margin-top: var(--space-md);
    font-size: 12px;
    font-weight: 600;
  }
  .error-state .empty-icon {
    color: var(--failed);
  }

  .detail {
    flex-shrink: 0;
    height: 200px;
    border-top: 1px solid var(--border);
    background: var(--bg-secondary);
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }
  .d-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 8px 14px;
    border-bottom: 1px solid var(--border-subtle);
  }
  .d-title {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .d-identity {
    display: flex;
    min-width: 0;
    flex-direction: column;
  }
  .d-label {
    color: var(--text-dim);
    font-size: 8px;
    font-weight: 650;
    letter-spacing: 0.08em;
    text-transform: uppercase;
  }
  .d-dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
  }
  .d-dot.status-success {
    background: var(--success);
  }
  .d-dot.status-failed {
    background: var(--failed);
  }
  .d-dot.status-running {
    background: var(--running);
  }
  .d-dot.status-skipped,
  .d-dot.status-pending {
    background: var(--pending);
  }
  .d-name {
    font-size: 13px;
    font-weight: 600;
  }
  .d-type {
    font-size: 10px;
    font-family: var(--font-mono);
    color: var(--text-ghost);
  }
  .d-close {
    width: 24px;
    height: 24px;
    display: flex;
    align-items: center;
    justify-content: center;
    border-radius: 4px;
    color: var(--text-muted);
    cursor: pointer;
    background: transparent;
    border: none;
  }
  .d-close:hover {
    color: var(--text-primary);
    background: var(--bg-tertiary);
  }
  .d-stats {
    display: flex;
    gap: 20px;
    padding: 6px 14px;
    border-bottom: 1px solid var(--border-subtle);
  }
  .d-kv {
    display: flex;
    flex-direction: column;
  }
  .d-v {
    font-size: 12px;
    font-family: var(--font-mono);
    font-weight: 600;
  }
  .d-k {
    font-size: 8px;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--text-ghost);
  }
  .d-kv.err .d-v {
    color: var(--failed);
    font-size: 10px;
  }
  .d-attempts {
    display: flex;
    gap: 6px;
    padding: 4px 14px;
    border-bottom: 1px solid var(--border-subtle);
    overflow-x: auto;
  }
  .d-att {
    display: flex;
    align-items: center;
    gap: 4px;
    padding: 2px 6px;
    border-radius: 3px;
    background: var(--bg-tertiary);
    font-size: 10px;
  }
  .d-att.att-fail {
    border-left: 2px solid var(--failed);
  }
  .att-n {
    font-weight: 700;
    font-family: var(--font-mono);
    font-size: 9px;
    color: var(--text-muted);
  }
  .att-d {
    font-family: var(--font-mono);
  }
  .d-logs {
    flex: 1;
    overflow-y: auto;
    padding: 4px 14px;
    font-family: var(--font-mono);
    font-size: 10px;
    line-height: 1.6;
  }
  .log {
    display: flex;
    gap: 6px;
  }
  .log-l {
    width: 36px;
    flex-shrink: 0;
    font-size: 8px;
    text-transform: uppercase;
    color: var(--text-ghost);
  }
  .log-m {
    color: var(--text-secondary);
    word-break: break-word;
  }
  .no-logs {
    opacity: 0.55;
  }
  .log-e .log-l,
  .log-e .log-m {
    color: var(--failed);
  }
  .log-w .log-l {
    color: var(--warning);
  }

  @media (max-width: 980px) {
    .page-header {
      align-items: stretch;
      flex-direction: column;
      gap: 14px;
    }
    .run-metrics {
      width: fit-content;
      max-width: 100%;
    }
  }

  @media (max-width: 768px) {
    .page {
      height: auto;
      min-height: calc(100dvh - 102px);
    }
    .title-row h1 {
      font-size: 20px;
    }
    .run-metrics {
      width: 100%;
      overflow-x: auto;
    }
    .metric {
      min-width: 25%;
      flex: 1;
    }
    .workspace {
      min-height: 610px;
      flex: none;
    }
    .workspace-toolbar {
      align-items: flex-start;
      flex-direction: column;
      gap: 7px;
      padding: 9px 12px;
    }
    .workspace-toolbar > div {
      flex-wrap: wrap;
      gap: 7px;
    }
    .workspace-hint {
      display: none;
    }
    .status-legend {
      flex-wrap: wrap;
    }
    .body.has-detail {
      flex: 0.58;
    }
    .detail {
      height: 250px;
    }
    .d-title {
      min-width: 0;
      flex-wrap: wrap;
    }
    .d-identity {
      max-width: 170px;
    }
    .d-name {
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
    .d-stats {
      overflow-x: auto;
    }
    .d-kv {
      flex: none;
    }
  }

  @media (max-width: 480px) {
    .metric {
      min-width: 82px;
    }
    .status-legend span {
      font-size: 0;
    }
    .status-legend i {
      width: 9px;
      height: 9px;
    }
    .detail {
      height: 280px;
    }
    .d-type {
      display: none;
    }
  }
</style>
