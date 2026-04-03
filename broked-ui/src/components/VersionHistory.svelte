<script lang="ts">
  import { api } from "../lib/api";
  import { notify } from "../lib/toast";
  import type { Pipeline, PipelineVersion } from "../lib/types";
  import { createEventDispatcher } from "svelte";

  export let pipeline: Pipeline | null = null;

  const dispatch = createEventDispatcher();

  let versions: PipelineVersion[] = [];
  let loading = false;
  let rollingBack = false;

  export async function load() {
    if (!pipeline?.id) return;
    loading = true;
    try {
      versions = await api.pipelines.versions(pipeline.id);
    } catch {
      versions = [];
    }
    loading = false;
  }

  async function rollbackTo(version: number) {
    if (!pipeline?.id) return;
    rollingBack = true;
    try {
      const restored = await api.pipelines.rollback(pipeline.id, version);
      dispatch("restore", restored);
      notify.success(`Rolled back to v${version}`);
    } catch {
      notify.error("Rollback failed");
    }
    rollingBack = false;
  }

  function timeAgo(dateStr: string): string {
    const d = new Date(dateStr);
    const diffMs = Date.now() - d.getTime();
    const mins = Math.floor(diffMs / 60000);
    if (mins < 1) return "just now";
    if (mins < 60) return `${mins}m ago`;
    const hours = Math.floor(mins / 60);
    if (hours < 24) return `${hours}h ago`;
    return `${Math.floor(hours / 24)}d ago`;
  }
</script>

<div class="version-panel">
  <div class="version-header">
    <span class="version-title">Version History</span>
    <button class="btn-close" on:click={() => dispatch("close")}>Close</button>
  </div>
  {#if loading}
    <div class="version-loading">Loading...</div>
  {:else if versions.length === 0}
    <div class="version-empty">No versions yet. Save to create the first snapshot.</div>
  {:else}
    <div class="version-list">
      {#each versions as v, i}
        <div class="version-item" class:latest={i === 0}>
          <div class="version-meta">
            <span class="version-num">v{v.version}</span>
            <span class="version-time">{timeAgo(v.created_at)}</span>
          </div>
          {#if v.message}
            <div class="version-msg">{v.message}</div>
          {/if}
          {#if i > 0}
            <button class="version-restore" on:click={() => rollbackTo(v.version)} disabled={rollingBack}>
              {rollingBack ? "Restoring..." : "Restore"}
            </button>
          {:else}
            <span class="version-current">Current</span>
          {/if}
        </div>
      {/each}
    </div>
  {/if}
</div>

<style>
  .version-panel { display: flex; flex-direction: column; height: 100%; }
  .version-header { display: flex; justify-content: space-between; align-items: center; padding: 12px 14px; border-bottom: 1px solid var(--border-sidebar); }
  .version-title { font-size: 11px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.08em; color: var(--text-secondary); }
  .version-loading, .version-empty { padding: 20px 14px; font-size: 12px; color: var(--text-dim); text-align: center; }
  .version-list { flex: 1; overflow-y: auto; padding: 8px; }
  .version-item { padding: 10px; border-radius: 6px; margin-bottom: 4px; transition: background 150ms ease; border: 1px solid transparent; }
  .version-item:hover { background: var(--bg-tertiary); }
  .version-item.latest { border-color: var(--accent); background: var(--accent-glow); }
  .version-meta { display: flex; justify-content: space-between; align-items: center; margin-bottom: 4px; }
  .version-num { font-family: 'JetBrains Mono', monospace; font-size: 12px; font-weight: 600; color: var(--text-primary); }
  .version-time { font-size: 10px; color: var(--text-dim); }
  .version-msg { font-size: 11px; color: var(--text-muted); margin-bottom: 6px; }
  .version-restore { font-size: 10px; font-weight: 500; padding: 3px 10px; border-radius: 4px; background: var(--bg-secondary); border: 1px solid var(--border-subtle); color: var(--text-secondary); transition: all 150ms ease; }
  .version-restore:hover:not(:disabled) { border-color: var(--accent); color: var(--accent-text); background: var(--accent-glow); }
  .version-restore:disabled { opacity: 0.5; cursor: wait; }
  .version-current { font-size: 10px; font-weight: 600; color: var(--accent-text); text-transform: uppercase; letter-spacing: 0.08em; }
  .btn-close { font-size: 11px; color: var(--text-dim); padding: 2px 8px; border-radius: 4px; transition: all 150ms ease; }
  .btn-close:hover { color: var(--text-primary); background: var(--border-subtle); }
</style>
