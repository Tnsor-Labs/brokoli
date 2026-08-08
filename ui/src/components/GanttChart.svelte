<script lang="ts">
  import type { Node, NodeRun, RunStatus } from "../lib/types";
  import StatusBadge from "./StatusBadge.svelte";

  export let nodes: Node[] = [];
  export let nodeRuns: NodeRun[] = [];
  export let runStartedAt: string | null = null;
  export let onSelectNode: ((nodeId: string) => void) | null = null;
  export let pipelineId: string = "";
  export let runId: string = "";

  $: nodeMap = new Map(nodes.map(n => [n.id, n]));

  // Group by node_id so retries/resumed-run/expansion re-attempts show as
  // one row per node (labeled with their attempt history) instead of one
  // indistinguishable row per attempt. Within a group, sort by `attempt`
  // ascending; `attempt` is undefined for older runs/run types that never
  // set it, which sorts as a single implicit first attempt — same as today
  // for the common (non-retry) case.
  $: groupedRuns = (() => {
    const byNode = new Map<string, NodeRun[]>();
    for (const nr of nodeRuns) {
      if (!nr.started_at && nr.duration_ms <= 0) continue;
      const attempts = byNode.get(nr.node_id) ?? [];
      attempts.push(nr);
      byNode.set(nr.node_id, attempts);
    }
    const groups = [...byNode.entries()].map(([nodeId, attempts]) => ({
      nodeId,
      attempts: [...attempts].sort((a, b) => (a.attempt ?? 0) - (b.attempt ?? 0)),
    }));
    groups.sort((a, b) => {
      const ta = a.attempts[0].started_at || "";
      const tb = b.attempts[0].started_at || "";
      return ta.localeCompare(tb);
    });
    return groups;
  })();

  // The primary (latest) attempt per node is what drives the main bar,
  // the footer summary, and bar-width scaling — a failed retry that later
  // succeeded shouldn't count twice toward "rows processed" or stretch the
  // duration axis past what's actually shown.
  $: primaryRuns = groupedRuns.map(g => g.attempts[g.attempts.length - 1]);

  $: maxDuration = Math.max(...primaryRuns.map(nr => nr.duration_ms), 1);
  $: totalRows = primaryRuns.reduce((sum, nr) => sum + nr.row_count, 0);
  $: totalDuration = runStartedAt && primaryRuns.length
    ? Math.max(...primaryRuns.map(nr => {
        const start = nr.started_at ? new Date(nr.started_at).getTime() : 0;
        return start - new Date(runStartedAt!).getTime() + nr.duration_ms;
      }))
    : maxDuration;

  function barWidth(duration: number): number {
    return Math.max(2, (duration / maxDuration) * 100);
  }

  function barColor(status: RunStatus): string {
    if (status === "success") return "var(--success)";
    if (status === "failed") return "var(--failed)";
    if (status === "running") return "var(--accent)";
    if (status === "cancelled") return "var(--warning)";
    return "var(--pending)";
  }

  function formatDuration(ms: number): string {
    if (ms < 1000) return `${Math.round(ms)}ms`;
    if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`;
    const m = Math.floor(ms / 60_000);
    const s = Math.floor((ms % 60_000) / 1000);
    return `${m}m ${s}s`;
  }

  function formatRows(count: number): string {
    if (count >= 1_000_000) return `${(count / 1_000_000).toFixed(1)}M rows`;
    if (count >= 1_000) return `${(count / 1_000).toFixed(1)}K rows`;
    return `${count.toLocaleString()} rows`;
  }

  let selectedId: string | null = null;
</script>

{#if groupedRuns.length === 0}
  <div class="empty">No node execution data available.</div>
{:else}
  <div class="timeline">
    <!-- Header -->
    <div class="timeline-header">
      <span class="header-label">EXECUTION TIMELINE</span>
      {#if pipelineId && runId}
        <a href="#/pipelines/{pipelineId}/runs/{runId}/gantt" class="full-link">
          Open Full Timeline
          <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
            <path d="M7 17l9.2-9.2M17 17V7H7"/>
          </svg>
        </a>
      {/if}
    </div>

    <!-- Rows -->
    {#each groupedRuns as g (g.nodeId)}
      {@const node = nodeMap.get(g.nodeId)}
      {@const nr = g.attempts[g.attempts.length - 1]}
      {@const width = barWidth(nr.duration_ms)}
      <div class="row-group">
        <!-- svelte-ignore a11y_no_static_element_interactions -->
        <div
          class="row"
          class:selected={selectedId === g.nodeId}
          class:failed={nr.status === "failed"}
          on:click={() => {
            selectedId = selectedId === g.nodeId ? null : g.nodeId;
            onSelectNode?.(g.nodeId);
          }}
          on:keydown={() => {}}
        >
          <div class="row-label">
            <span class="node-name">{node?.name || g.nodeId}</span>
            {#if g.attempts.length > 1}
              <span class="retry-badge" title="{g.attempts.length} attempts">×{g.attempts.length}</span>
            {/if}
            <StatusBadge status={nr.status} size="sm" />
          </div>
          <div class="row-bar-area">
            <div
              class="bar"
              class:running={nr.status === "running"}
              style="width: {width}%; background: {barColor(nr.status)}"
            ></div>
            <span class="bar-duration">{formatDuration(nr.duration_ms)}</span>
            {#if nr.row_count > 0}
              <span class="bar-rows">{formatRows(nr.row_count)}</span>
            {/if}
          </div>
        </div>
        {#if g.attempts.length > 1}
          <div class="attempt-stack">
            {#each g.attempts as att, i (att.id)}
              <div class="attempt-row" class:failed={att.status === "failed"}>
                <span class="attempt-label">Attempt {i + 1}</span>
                <StatusBadge status={att.status} size="sm" />
                <span class="attempt-duration">{formatDuration(att.duration_ms)}</span>
                {#if att.row_count > 0}
                  <span class="attempt-rows">{formatRows(att.row_count)}</span>
                {/if}
              </div>
            {/each}
          </div>
        {/if}
      </div>
    {/each}

    <!-- Footer -->
    <div class="timeline-footer">
      <span>Total: {formatDuration(totalDuration)}</span>
      <span class="sep">·</span>
      <span>{primaryRuns.filter(r => r.status === "success").length}/{primaryRuns.length} nodes</span>
      <span class="sep">·</span>
      <span>{formatRows(totalRows)}</span>
      {#if primaryRuns.some(r => r.status === "failed")}
        <span class="sep">·</span>
        <span class="footer-failed">{primaryRuns.filter(r => r.status === "failed").length} failed</span>
      {/if}
    </div>
  </div>
{/if}

<style>
  .timeline {
    border: 1px solid var(--border-subtle);
    border-radius: 8px;
    overflow: hidden;
    background: var(--bg-secondary);
  }

  .empty {
    padding: 32px;
    text-align: center;
    color: var(--text-muted);
    font-size: 13px;
  }

  /* Header */
  .timeline-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 10px 14px;
    border-bottom: 1px solid var(--border-subtle);
    background: var(--bg-tertiary);
  }
  .header-label {
    font-size: 10px;
    font-weight: 600;
    letter-spacing: 0.08em;
    color: var(--text-muted);
  }
  .full-link {
    font-size: 11px;
    font-weight: 500;
    color: var(--accent);
    text-decoration: none;
    display: flex;
    align-items: center;
    gap: 4px;
    transition: opacity 150ms;
  }
  .full-link:hover { opacity: 0.8; }

  /* Rows */
  .row-group:last-of-type .row { border-bottom: none; }
  .row-group:last-of-type .attempt-stack { border-bottom: none; }

  .row {
    display: flex;
    align-items: center;
    padding: 6px 14px;
    min-height: 36px;
    border-bottom: 1px solid var(--border-subtle);
    cursor: pointer;
    transition: background 100ms;
  }
  .row:hover { background: var(--bg-tertiary); }
  .row.selected { background: var(--accent-glow); }
  .row.failed { border-left: 2px solid var(--failed); }

  .retry-badge {
    font-size: 10px;
    font-weight: 700;
    font-family: var(--font-mono);
    color: var(--accent);
    background: var(--accent-glow);
    border-radius: 3px;
    padding: 1px 5px;
    flex-shrink: 0;
  }

  /* Attempt history — compact stack under a retried node's primary row */
  .attempt-stack {
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding: 2px 14px 6px 34px;
    border-bottom: 1px solid var(--border-subtle);
    background: var(--bg-tertiary);
  }
  .attempt-row {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 11px;
    color: var(--text-muted);
    padding: 2px 0;
  }
  .attempt-row.failed { color: var(--failed); }
  .attempt-label {
    font-family: var(--font-mono);
    font-weight: 500;
    min-width: 66px;
    flex-shrink: 0;
  }
  .attempt-duration {
    font-family: var(--font-mono);
    min-width: 48px;
  }
  .attempt-rows {
    font-family: var(--font-mono);
  }

  .row-label {
    width: 200px;
    min-width: 200px;
    display: flex;
    align-items: center;
    gap: 8px;
    flex-shrink: 0;
    overflow: hidden;
  }
  .node-name {
    font-size: 13px;
    font-weight: 500;
    color: var(--text-primary);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    max-width: 140px;
  }

  .row-bar-area {
    flex: 1;
    display: flex;
    align-items: center;
    gap: 10px;
    height: 24px;
  }

  .bar {
    height: 100%;
    border-radius: 4px;
    min-width: 4px;
    opacity: 0.85;
    transition: width 300ms ease, opacity 150ms;
  }
  .row:hover .bar { opacity: 1; }
  .bar.running {
    animation: pulse 1.5s ease-in-out infinite;
  }

  .bar-duration {
    font-family: var(--font-mono);
    font-size: 11px;
    font-weight: 500;
    color: var(--text-secondary);
    white-space: nowrap;
    min-width: 48px;
  }
  .bar-rows {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--text-muted);
    white-space: nowrap;
  }

  /* Footer */
  .timeline-footer {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 8px 14px;
    font-size: 11px;
    font-family: var(--font-mono);
    color: var(--text-muted);
    border-top: 1px solid var(--border-subtle);
    background: var(--bg-tertiary);
  }
  .sep { opacity: 0.3; }
  .footer-failed { color: var(--failed); }

  @keyframes pulse {
    0%, 100% { opacity: 0.85; }
    50% { opacity: 0.4; }
  }

  @media (max-width: 768px) {
    .row-label { width: 120px; min-width: 120px; }
    .node-name { max-width: 80px; }
  }
</style>
