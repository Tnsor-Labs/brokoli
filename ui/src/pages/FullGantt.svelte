<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import type { Pipeline, Run, NodeRun, RunStatus, NodeStats } from "../lib/types";
  import { authHeaders } from "../lib/auth";
  import StatusBadge from "../components/StatusBadge.svelte";
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
    for (const nr of (run?.node_runs || [])) {
      if (!m.has(nr.node_id)) m.set(nr.node_id, []);
      m.get(nr.node_id)!.push(nr);
    }
    for (const [, a] of m) a.sort((x, y) => (x.attempt ?? 0) - (y.attempt ?? 0));
    return m;
  })();

  $: primaryRuns = [...nodeGroups.entries()]
    .map(([, nrs]) => nrs[nrs.length - 1])
    .filter(nr => nr.started_at || nr.duration_ms > 0)
    .sort((a, b) => (a.started_at || "").localeCompare(b.started_at || ""));

  $: totalMs = primaryRuns.length
    ? Math.max(...primaryRuns.map(nr => {
        const s = nr.started_at ? new Date(nr.started_at).getTime() : 0;
        return s + nr.duration_ms;
      })) - (run?.started_at ? new Date(run.started_at).getTime() : 0)
    : 1000;

  $: totalRows = primaryRuns.reduce((s, r) => s + r.row_count, 0);

  // Detail panel
  $: sel = selectedNodeId ? primaryRuns.find(r => r.node_id === selectedNodeId) : null;
  $: selNode = selectedNodeId ? pipeline?.nodes.find(n => n.id === selectedNodeId) : null;
  $: selAttempts = selectedNodeId ? (nodeGroups.get(selectedNodeId) || []) : [];
  $: selLogs = selectedNodeId ? logs.filter(l => l.node_id === selectedNodeId) : [];

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
    return "bar-pending";
  }

  function renderTimeline() {
    if (!containerEl || !primaryRuns.length || !pipeline) return;

    const epoch = run?.started_at ? new Date(run.started_at).getTime() : Date.now();

    const { groups, items, arrows, options } = buildTimelineData(
      pipeline!, primaryRuns, nodeGroups, epoch, totalMs
    );

    timeline = new Timeline(containerEl, items, groups, options);

    if (arrows.length > 0) {
      try {
        new Arrow(timeline, arrows, {
          color: "#52525b",
          strokeWidth: 1.5,
          arrowEnd: true,
        });
      } catch (e) { console.warn("Arrow plugin:", e); }
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
    totalMs: number
  ) {
    const nodeMap = new Map(pipe.nodes.map(n => [n.id, n]));

    // Groups (left panel rows)
    const groups = new DataSet(
      runs.map((nr, i) => {
        const node = nodeMap.get(nr.node_id);
        const attempts = groups_map.get(nr.node_id) || [];
        const retryBadge = attempts.length > 1
          ? `<span class="g-badge retry">R${attempts.length}</span>` : "";
        const sc = nr.status === "failed" ? "#ef4444" : nr.status === "running" ? "#0d9488" : "#22c55e";

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
      })
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
          title: `${node?.name || nr.node_id}\nDuration: ${fmt(nr.duration_ms)}\nRows: ${nr.row_count.toLocaleString()}${nr.rows_per_sec ? '\nThroughput: ' + fmtRps(nr.rows_per_sec) + '/s' : ''}`,
        };
      })
    );

    // Arrows (dependencies)
    const runNodeIds = new Set(runs.map(r => r.node_id));
    const arrows = (pipe.edges || [])
      .filter(e => runNodeIds.has(e.from) && runNodeIds.has(e.to))
      .map((e, i) => ({ id: i, id_item_1: e.from, id_item_2: e.to }));

    // Options
    const tStart = new Date(epoch - totalMs * 0.05);
    const tEnd = new Date(epoch + totalMs * 1.15);

    const toMs = (date: any): number =>
      date instanceof Date ? date.getTime() : typeof date === 'number' ? date : new Date(date).getTime();

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
        majorLabels: (date: any) => { const ms = toMs(date) - epoch; return ms < 0 ? "" : fmt(ms); },
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
    if (!params.id || !params.runId) { error = "Missing params"; loading = false; return; }
    try {
      const [pR, rR, lR, sR] = await Promise.all([
        fetch(`/api/pipelines/${params.id}`, { headers: authHeaders() }),
        fetch(`/api/runs/${params.runId}`, { headers: authHeaders() }),
        fetch(`/api/runs/${params.runId}/logs`, { headers: authHeaders() }),
        fetch(`/api/pipelines/${params.id}/node-stats?runs=10`, { headers: authHeaders() }).catch(() => null),
      ]);
      if (pR.ok) pipeline = await pR.json();
      if (rR.ok) run = await rR.json();
      if (lR.ok) { const d = await lR.json(); logs = Array.isArray(d) ? d : d.logs || []; }
      if (sR?.ok) { const d = await sR.json(); nodeStats = d?.nodes || d || {}; }
      if (!pipeline) error = "Pipeline not found";
      else if (!run) error = "Run not found";
    } catch (e: any) { error = e.message || "Failed"; }
    loading = false;
    // Render after DOM update
    requestAnimationFrame(() => renderTimeline());
  });

  onDestroy(() => {
    if (timeline) { timeline.destroy(); timeline = null; }
  });
</script>

<div class="page">
  <header class="tb">
    <div class="tb-l">
      <a href="#/pipelines/{params.id}/runs" class="tb-back">
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M15 18l-6-6 6-6"/></svg>
        Back to Runs
      </a>
      <span class="tb-sep"></span>
      <span class="tb-name">{pipeline?.name || "..."}</span>
      <span class="tb-id">{params.runId?.slice(0, 8)}</span>
      {#if run}<StatusBadge status={run.status} size="sm" />{/if}
    </div>
    <div class="tb-r">
      <div class="tb-kv"><span class="tb-v">{fmt(totalMs)}</span><span class="tb-k">TOTAL</span></div>
      <div class="tb-kv"><span class="tb-v">{primaryRuns.length}</span><span class="tb-k">NODES</span></div>
      <div class="tb-kv"><span class="tb-v">{fmtRows(totalRows)}</span><span class="tb-k">ROWS</span></div>
      {#if run?.trace_id}
        <div class="tb-kv"><span class="tb-v trace">{run.trace_id.slice(0, 12)}</span><span class="tb-k">TRACE</span></div>
      {/if}
    </div>
  </header>

  {#if loading}
    <div class="msg">Loading...</div>
  {:else if error}
    <div class="msg err">{error}</div>
  {:else}
    <div class="body" class:has-detail={!!sel}>
      <div class="timeline-container" bind:this={containerEl}></div>
    </div>

    {#if sel && selNode}
      <div class="detail">
        <div class="d-head">
          <div class="d-title">
            <span class="d-dot" style="background:{sel.status === 'failed' ? '#ef4444' : '#22c55e'}"></span>
            <span class="d-name">{selNode.name}</span>
            <StatusBadge status={sel.status} size="sm" />
            <span class="d-type">{selNode.type}</span>
          </div>
          <button class="d-close" on:click={() => { selectedNodeId = null; if (timeline) timeline.setSelection([]); }}>
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M18 6L6 18M6 6l12 12"/></svg>
          </button>
        </div>
        <div class="d-stats">
          <div class="d-kv"><span class="d-v">{fmt(sel.duration_ms)}</span><span class="d-k">DURATION</span></div>
          <div class="d-kv"><span class="d-v">{sel.row_count.toLocaleString()}</span><span class="d-k">ROWS</span></div>
          <div class="d-kv"><span class="d-v">{sel.started_at ? new Date(sel.started_at).toLocaleTimeString("en-US", { hour12: false }) : "—"}</span><span class="d-k">STARTED</span></div>
          {#if sel.rows_per_sec}<div class="d-kv"><span class="d-v">{fmtRows(sel.rows_per_sec)}/s</span><span class="d-k">THROUGHPUT</span></div>{/if}
          {#if sel.queue_ms}<div class="d-kv"><span class="d-v">{fmt(sel.queue_ms)}</span><span class="d-k">QUEUE</span></div>{/if}
          {#if sel.trace_id}<div class="d-kv"><span class="d-v trace">{sel.trace_id.slice(0, 12)}</span><span class="d-k">TRACE</span></div>{/if}
          {#if sel.error}<div class="d-kv err"><span class="d-v">{sel.error}</span><span class="d-k">ERROR</span></div>{/if}
        </div>
        {#if selAttempts.length > 1}
          <div class="d-attempts">
            {#each selAttempts as att, i}
              <div class="d-att" class:att-fail={att.status === "failed"}>
                <span class="att-n">A{i}</span><StatusBadge status={att.status} size="sm" /><span class="att-d">{fmt(att.duration_ms)}</span>
              </div>
            {/each}
          </div>
        {/if}
        <div class="d-logs">
          {#each selLogs as l}
            <div class="log" class:log-e={l.level === "error"} class:log-w={l.level === "warning"}>
              <span class="log-l">{l.level}</span><span class="log-m">{l.message}</span>
            </div>
          {/each}
          {#if selLogs.length === 0}<div class="log"><span class="log-m" style="opacity:0.4">No logs</span></div>{/if}
        </div>
      </div>
    {/if}
  {/if}
</div>

<style>
  .page { display:flex; flex-direction:column; height:100vh; background:var(--bg-primary); color:var(--text-primary); overflow:hidden; }

  .tb { display:flex; align-items:center; justify-content:space-between; padding:0 14px; height:44px; background:var(--bg-secondary); border-bottom:1px solid var(--border-subtle); flex-shrink:0; }
  .tb-l,.tb-r { display:flex; align-items:center; gap:10px; }
  .tb-back { display:flex; align-items:center; gap:4px; color:var(--text-muted); text-decoration:none; font-size:12px; font-weight:500; } .tb-back:hover { color:var(--accent); }
  .tb-sep { width:1px; height:18px; background:var(--border-subtle); }
  .tb-name { font-size:13px; font-weight:600; }
  .tb-id { font-size:10px; font-family:var(--font-mono); color:var(--text-ghost); }
  .tb-kv { display:flex; flex-direction:column; align-items:center; }
  .tb-v { font-size:12px; font-family:var(--font-mono); font-weight:600; }
  .tb-v.trace { font-size:9px; color:var(--text-muted); }
  .tb-k { font-size:8px; text-transform:uppercase; letter-spacing:.06em; color:var(--text-ghost); }

  .body { flex:1; overflow:hidden; }
  .body.has-detail { flex:0.65; }

  .timeline-container { width:100%; height:100%; }

  /* ─── vis-timeline premium dark theme ─── */

  /* Canvas */
  .timeline-container :global(.vis-timeline) { border:none; background:var(--bg-primary); font-family:var(--font-ui); }
  .timeline-container :global(.vis-panel.vis-top) { border-bottom:1px solid var(--border-subtle); background:var(--bg-secondary); }
  .timeline-container :global(.vis-panel.vis-bottom) { border-top:1px solid var(--border-subtle); }
  .timeline-container :global(.vis-panel.vis-left) { border-right:1px solid var(--border); background:var(--bg-secondary); }
  .timeline-container :global(.vis-panel.vis-center) { border-left:none; }

  /* Group labels (left panel) */
  .timeline-container :global(.vis-labelset .vis-label) {
    background:var(--bg-secondary); border-bottom:1px solid var(--border-subtle);
    color:var(--text-primary); padding:0;
  }
  .timeline-container :global(.vis-labelset .vis-label:hover) { background:var(--bg-tertiary); }
  .timeline-container :global(.vis-labelset .vis-label .vis-inner) { padding:0; margin:0; }

  /* Group label inner structure */
  /* Group label — single line: dot + name + duration */
  .timeline-container :global(.gl) { display:flex; align-items:center; gap:8px; padding:0 12px; height:100%; }
  .timeline-container :global(.gl-dot) { width:7px; height:7px; border-radius:50%; flex-shrink:0; }
  .timeline-container :global(.gl-name) { font-size:12px; font-weight:500; color:var(--text-primary); white-space:nowrap; overflow:hidden; text-overflow:ellipsis; flex:1; }
  .timeline-container :global(.gl-dur) { font-size:10px; font-family:var(--font-mono); color:var(--text-ghost); flex-shrink:0; }
  .timeline-container :global(.g-badge) { font-size:7px; font-weight:700; padding:1px 4px; border-radius:2px; text-transform:uppercase; flex-shrink:0; }
  .timeline-container :global(.g-badge.retry) { color:#3b82f6; background:rgba(59,130,246,0.1); }

  /* Row stripes */
  .timeline-container :global(.vis-foreground .vis-group) { border-bottom:1px solid var(--border-subtle); }
  .timeline-container :global(.vis-foreground .vis-group:nth-child(even)) { background:var(--bg-secondary); }

  /* Time axis */
  .timeline-container :global(.vis-time-axis) { background:var(--bg-secondary); }
  .timeline-container :global(.vis-time-axis .vis-text) { color:var(--text-muted); font-family:var(--font-mono); font-size:10px; font-weight:500; }
  .timeline-container :global(.vis-time-axis .vis-text.vis-major) { font-weight:600; color:var(--text-secondary); }
  .timeline-container :global(.vis-time-axis .vis-grid.vis-minor) { border-color:var(--border-subtle); opacity:0.2; }
  .timeline-container :global(.vis-time-axis .vis-grid.vis-major) { border-color:var(--border-subtle); opacity:0.4; }

  /* Bars */
  .timeline-container :global(.vis-item) {
    border-radius:6px; border:none; font-family:var(--font-mono); font-size:11px;
    color:white; box-shadow:0 1px 4px rgba(0,0,0,0.2); transition:box-shadow 150ms, transform 150ms;
  }
  .timeline-container :global(.vis-item:hover) { box-shadow:0 2px 8px rgba(0,0,0,0.3); transform:translateY(-1px); }
  .timeline-container :global(.vis-item.bar-success) { background:linear-gradient(135deg, #22c55e 0%, #16a34a 100%); }
  .timeline-container :global(.vis-item.bar-success .vis-item-overflow) { background:transparent; }
  .timeline-container :global(.vis-item.bar-failed) { background:linear-gradient(135deg, #ef4444 0%, #dc2626 100%); }
  .timeline-container :global(.vis-item.bar-failed .vis-item-overflow) { background:transparent; }
  .timeline-container :global(.vis-item.bar-running) { background:linear-gradient(135deg, #0d9488 0%, #0f766e 100%); }
  .timeline-container :global(.vis-item.bar-running .vis-item-overflow) { background:transparent; }
  .timeline-container :global(.vis-item.bar-pending) { background:linear-gradient(135deg, #71717a 0%, #52525b 100%); }
  .timeline-container :global(.vis-item.vis-selected) { box-shadow:0 0 0 2px var(--accent), 0 2px 12px rgba(13,148,136,0.3); z-index:10; }
  .timeline-container :global(.vis-item .vis-item-content) { padding:4px 10px; }
  .timeline-container :global(.vis-item .vis-item-visible-frame) { border-radius:6px; }

  /* Bar inner content */
  .timeline-container :global(.bar-inner) { display:flex; align-items:center; gap:8px; white-space:nowrap; overflow:hidden; }
  .timeline-container :global(.bar-label) { font-weight:600; font-size:10.5px; opacity:0.95; }
  .timeline-container :global(.bar-stats) { font-weight:500; font-size:9.5px; opacity:0.75; }

  /* Arrows */
  .timeline-container :global(.arrow-line) { stroke:#6b7280; stroke-width:1.5; opacity:0.6; }
  .timeline-container :global(.arrow-head) { fill:#6b7280; opacity:0.6; }

  /* Tooltip */
  .timeline-container :global(.vis-tooltip) {
    background:var(--bg-secondary) !important; color:var(--text-primary) !important;
    border:1px solid var(--border-subtle) !important; border-radius:6px !important;
    padding:8px 12px !important; font-family:var(--font-mono) !important; font-size:11px !important;
    box-shadow:0 4px 16px rgba(0,0,0,0.3) !important; white-space:pre-line !important;
  }

  /* Hide drag handles */
  .timeline-container :global(.vis-item .vis-drag-left),
  .timeline-container :global(.vis-item .vis-drag-right),
  .timeline-container :global(.vis-item .vis-drag-center) { display:none; }

  .msg { flex:1; display:flex; align-items:center; justify-content:center; color:var(--text-muted); }
  .msg.err { color:var(--failed); }

  .detail { flex-shrink:0; height:200px; border-top:2px solid var(--accent); background:var(--bg-secondary); display:flex; flex-direction:column; overflow:hidden; }
  .d-head { display:flex; align-items:center; justify-content:space-between; padding:8px 14px; border-bottom:1px solid var(--border-subtle); }
  .d-title { display:flex; align-items:center; gap:8px; }
  .d-dot { width:8px; height:8px; border-radius:50%; }
  .d-name { font-size:13px; font-weight:600; }
  .d-type { font-size:10px; font-family:var(--font-mono); color:var(--text-ghost); }
  .d-close { width:24px; height:24px; display:flex; align-items:center; justify-content:center; border-radius:4px; color:var(--text-muted); cursor:pointer; background:transparent; border:none; } .d-close:hover { color:var(--text-primary); background:var(--bg-tertiary); }
  .d-stats { display:flex; gap:20px; padding:6px 14px; border-bottom:1px solid var(--border-subtle); }
  .d-kv { display:flex; flex-direction:column; }
  .d-v { font-size:12px; font-family:var(--font-mono); font-weight:600; }
  .d-k { font-size:8px; text-transform:uppercase; letter-spacing:.05em; color:var(--text-ghost); }
  .d-kv.err .d-v { color:var(--failed); font-size:10px; }
  .d-attempts { display:flex; gap:6px; padding:4px 14px; border-bottom:1px solid var(--border-subtle); overflow-x:auto; }
  .d-att { display:flex; align-items:center; gap:4px; padding:2px 6px; border-radius:3px; background:var(--bg-tertiary); font-size:10px; }
  .d-att.att-fail { border-left:2px solid var(--failed); }
  .att-n { font-weight:700; font-family:var(--font-mono); font-size:9px; color:var(--text-muted); }
  .att-d { font-family:var(--font-mono); }
  .d-logs { flex:1; overflow-y:auto; padding:4px 14px; font-family:var(--font-mono); font-size:10px; line-height:1.6; }
  .log { display:flex; gap:6px; }
  .log-l { width:36px; flex-shrink:0; font-size:8px; text-transform:uppercase; color:var(--text-ghost); }
  .log-m { color:var(--text-secondary); word-break:break-word; }
  .log-e .log-l,.log-e .log-m { color:var(--failed); }
  .log-w .log-l { color:#f59e0b; }
</style>
