<script lang="ts">
  import type { NodeRun, Node } from "../lib/types";
  import StatusBadge from "./StatusBadge.svelte";

  export let nodeRuns: NodeRun[] = [];
  export let nodes: Node[] = [];

  $: nodeMap = new Map(nodes.map((n) => [n.id, n]));

  $: sortedRuns = [...nodeRuns].sort((a, b) => {
    const ta = a.started_at || "";
    const tb = b.started_at || "";
    return ta.localeCompare(tb);
  });

  $: maxDuration = Math.max(...nodeRuns.map((nr) => nr.duration_ms), 1);

  function barWidth(duration: number): string {
    return Math.max(2, (duration / maxDuration) * 100) + "%";
  }

  function barColor(status: string): string {
    const colors: Record<string, string> = {
      success: "var(--success)",
      failed: "var(--failed)",
      running: "var(--running)",
      pending: "var(--pending)",
    };
    return colors[status] || "var(--pending)";
  }
</script>

<div class="timeline">
  {#each sortedRuns as nr}
    {@const node = nodeMap.get(nr.node_id)}
    <div class="timeline-row">
      <div class="row-label">
        <span class="node-name">{node?.name || nr.node_id}</span>
        <StatusBadge status={nr.status} size="sm" />
      </div>
      <div class="row-bar">
        <div
          class="bar"
          class:running={nr.status === "running"}
          style="width: {barWidth(nr.duration_ms)}; background: {barColor(nr.status)}"
        ></div>
        <span class="bar-label">{nr.duration_ms}ms</span>
        {#if nr.row_count > 0}
          <span class="row-count">{nr.row_count} rows</span>
        {/if}
      </div>
    </div>
  {/each}
</div>

<style>
  .timeline {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }

  .timeline-row {
    display: grid;
    grid-template-columns: 200px 1fr;
    align-items: center;
    gap: var(--space-md);
  }

  .row-label {
    display: flex;
    align-items: center;
    gap: var(--space-sm);
    overflow: hidden;
  }
  .node-name {
    font-size: 0.8125rem;
    font-weight: 500;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .row-bar {
    display: flex;
    align-items: center;
    gap: var(--space-sm);
    height: 24px;
  }

  .bar {
    height: 100%;
    border-radius: var(--radius-sm);
    min-width: 4px;
    opacity: 0.8;
    transition: width 300ms ease;
  }
  .bar.running {
    animation: bar-pulse 1.5s ease-in-out infinite;
  }

  .bar-label {
    font-family: var(--font-mono);
    font-size: 0.6875rem;
    color: var(--text-muted);
    white-space: nowrap;
  }

  .row-count {
    font-family: var(--font-mono);
    font-size: 0.6875rem;
    color: var(--text-muted);
    white-space: nowrap;
  }

  @keyframes bar-pulse {
    0%, 100% { opacity: 0.8; }
    50% { opacity: 0.4; }
  }
</style>
