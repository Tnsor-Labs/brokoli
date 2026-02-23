<script lang="ts">
  import type { LogEntry } from "../lib/types";
  import { afterUpdate } from "svelte";

  export let logs: LogEntry[] = [];
  export let filterNodeId: string | null = null;
  export let autoScroll: boolean = true;

  let container: HTMLDivElement;

  $: filtered = filterNodeId
    ? logs.filter((l) => l.node_id === filterNodeId)
    : logs;

  afterUpdate(() => {
    if (autoScroll && container) {
      container.scrollTop = container.scrollHeight;
    }
  });

  function levelColor(level: string): string {
    const colors: Record<string, string> = {
      debug: "var(--text-muted)",
      info: "var(--text-secondary)",
      warning: "var(--warning)",
      error: "var(--failed)",
    };
    return colors[level] || "var(--text-secondary)";
  }

  function formatTime(ts: string): string {
    try {
      return new Date(ts).toLocaleTimeString();
    } catch {
      return ts;
    }
  }
</script>

<div class="log-stream" bind:this={container}>
  {#if filtered.length === 0}
    <div class="empty">No logs yet</div>
  {:else}
    {#each filtered as log}
      <div class="log-line">
        <span class="log-time">{formatTime(log.timestamp)}</span>
        <span class="log-level" style="color: {levelColor(log.level)}">{log.level.toUpperCase().padEnd(7)}</span>
        {#if log.node_id && !filterNodeId}
          <span class="log-node">[{log.node_id}]</span>
        {/if}
        <span class="log-msg">{log.message}</span>
      </div>
    {/each}
  {/if}
</div>

<style>
  .log-stream {
    font-family: var(--font-mono);
    font-size: 0.75rem;
    line-height: 1.6;
    background: var(--bg-primary);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    padding: var(--space-sm);
    overflow-y: auto;
    max-height: 300px;
  }

  .empty {
    color: var(--text-muted);
    padding: var(--space-md);
    text-align: center;
  }

  .log-line {
    display: flex;
    gap: var(--space-sm);
    padding: 1px 0;
    white-space: nowrap;
  }
  .log-line:hover {
    background: var(--bg-secondary);
  }

  .log-time {
    color: var(--text-muted);
    flex-shrink: 0;
  }
  .log-level {
    flex-shrink: 0;
    font-weight: 600;
  }
  .log-node {
    color: var(--accent);
    flex-shrink: 0;
  }
  .log-msg {
    color: var(--text-primary);
    overflow: hidden;
    text-overflow: ellipsis;
  }
</style>
