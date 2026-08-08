<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import type { Pipeline, Run } from "../../lib/types";
  import { api } from "../../lib/api";
  import { notify } from "../../lib/toast";

  // Failing pipelines, one entry per pipeline (its most recent failed run).
  export let items: { pipeline: Pipeline; run: Run }[] = [];

  const dispatch = createEventDispatcher<{ changed: void }>();

  // Per-run in-flight state, so one row's rerun doesn't disable the others.
  let busy: Record<string, boolean> = {};

  async function rerun(item: { pipeline: Pipeline; run: Run }) {
    const key = item.run.id;
    if (busy[key]) return;
    busy = { ...busy, [key]: true };
    try {
      await api.runs.trigger(item.pipeline.id);
      notify.success(`Rerunning ${item.pipeline.name}`);
      dispatch("changed");
    } catch (e: any) {
      notify.error(e?.message || `Failed to rerun ${item.pipeline.name}`);
    } finally {
      busy = { ...busy, [key]: false };
    }
  }

  // Errors can be long multi-line stack traces. Show the first line — the
  // part that identifies the problem — and let the title attribute carry
  // the rest rather than letting one failure push the others off screen.
  function firstLine(err?: string): string {
    if (!err) return "";
    const line = err.split("\n")[0].trim();
    return line.length > 160 ? line.slice(0, 160) + "…" : line;
  }

  function timeAgo(dateStr?: string | null): string {
    if (!dateStr) return "";
    const s = Math.floor((Date.now() - new Date(dateStr).getTime()) / 1000);
    if (s < 60) return `${s}s ago`;
    if (s < 3600) return `${Math.floor(s / 60)}m ago`;
    if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
    return `${Math.floor(s / 86400)}d ago`;
  }
</script>

{#if items.length === 0}
  <!-- Collapses to a single line when there's nothing wrong. The old
       layout reserved a full-height panel to say "all healthy", which is
       the one state that needs the least space. -->
  <div class="band band-ok">
    <span class="dot dot-ok"></span>
    <span class="band-title">All pipelines healthy</span>
  </div>
{:else}
  <div class="band band-alert">
    <div class="band-head">
      <span class="dot dot-fail"></span>
      <h2 class="band-title">
        Needs attention
        <span class="band-count">{items.length}</span>
      </h2>
    </div>

    <!-- Capped and scrollable: this band is the top of the page, and with
         a dozen pipelines down it would otherwise push everything else off
         screen — the failures would hide the recovery tools. -->
    <div class="issues">
      {#each items as item (item.run.id)}
        <div class="issue">
          <div class="issue-main">
            <div class="issue-line">
              <a class="issue-name" href="#/pipelines/{item.pipeline.id}/runs">
                {item.pipeline.name}
              </a>
              <span class="issue-meta">failed · {timeAgo(item.run.started_at)}</span>
            </div>
            {#if item.run.error}
              <p class="issue-error" title={item.run.error}>{firstLine(item.run.error)}</p>
            {/if}
          </div>

          <!-- The point of this panel: respond without navigating away. -->
          <div class="issue-actions">
            <a class="btn" href="#/pipelines/{item.pipeline.id}/runs">Logs</a>
            <button
              class="btn btn-primary"
              disabled={busy[item.run.id]}
              on:click={() => rerun(item)}
            >
              {busy[item.run.id] ? "Starting…" : "Rerun"}
            </button>
          </div>
        </div>
      {/each}
    </div>
  </div>
{/if}

<style>
  .band {
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius-lg);
    background: var(--bg-secondary);
    box-shadow: var(--shadow-card);
    overflow: hidden;
  }

  .band-ok {
    display: flex;
    align-items: center;
    gap: var(--space-sm);
    padding: 10px 14px;
  }

  /* Severity is carried by the dot, the count badge and the per-row accent.
     Ringing the whole card in red on top of those is redundant, and it
     makes a single recoverable failure look like an outage. */
  .band-alert { border-color: var(--border); }

  .band-head {
    display: flex;
    align-items: center;
    gap: var(--space-sm);
    padding: 7px 12px;
    background: var(--bg-tertiary);
    border-bottom: 1px solid var(--border-subtle);
  }

  .band-title {
    font-size: 0.8125rem;
    font-weight: 600;
    letter-spacing: 0.04em;
    text-transform: uppercase;
    color: var(--text-secondary);
    margin: 0;
    display: flex;
    align-items: center;
    gap: var(--space-sm);
  }

  .band-count {
    font-family: var(--font-mono);
    font-size: 0.6875rem;
    font-weight: 700;
    color: var(--failed);
    background: var(--failed-bg);
    border-radius: var(--radius-sm);
    padding: 1px 6px;
  }

  .dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    flex-shrink: 0;
  }
  .dot-ok { background: var(--success); }
  .dot-fail { background: var(--failed); }

  .issues {
    display: flex;
    flex-direction: column;
    max-height: 210px;
    overflow-y: auto;
  }

  .issue {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-md);
    padding: 10px 14px;
    border-bottom: 1px solid var(--border-subtle);
  }
  .issue:last-child { border-bottom: none; }
  .issue:hover { background: var(--bg-tertiary); }

  .issue-main { min-width: 0; flex: 1; }

  .issue-line {
    display: flex;
    align-items: baseline;
    gap: var(--space-sm);
    flex-wrap: wrap;
  }

  .issue-name {
    font-size: 0.875rem;
    font-weight: 600;
    color: var(--text-primary);
    text-decoration: none;
  }
  .issue-name:hover { color: var(--accent); }

  .issue-meta {
    font-size: 0.75rem;
    color: var(--text-muted);
  }

  .issue-error {
    margin: 3px 0 0;
    font-family: var(--font-mono);
    font-size: 0.6875rem;
    color: var(--failed);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .issue-actions {
    display: flex;
    gap: 6px;
    flex-shrink: 0;
  }

  .btn {
    font-size: 0.75rem;
    font-weight: 500;
    padding: 4px 10px;
    border-radius: var(--radius-sm);
    border: 1px solid var(--border);
    background: var(--bg-tertiary);
    color: var(--text-secondary);
    cursor: pointer;
    text-decoration: none;
    white-space: nowrap;
    transition: background var(--transition-fast), color var(--transition-fast);
  }
  .btn:hover:not(:disabled) {
    background: var(--bg-card-hover);
    color: var(--text-primary);
    border-color: var(--border-hover);
  }
  .btn:disabled { opacity: 0.6; cursor: default; }

  .btn-primary {
    border-color: var(--accent);
    color: var(--accent);
  }
  .btn-primary:hover:not(:disabled) {
    background: var(--accent-glow);
    color: var(--accent-hover);
  }

  @media (max-width: 768px) {
    .issue { flex-direction: column; align-items: stretch; gap: var(--space-sm); }
    .issue-actions { justify-content: flex-end; }
  }
</style>
