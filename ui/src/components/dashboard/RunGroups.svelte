<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import type { Pipeline, Run, PipelineRollup } from "../../lib/types";
  import StatusBadge from "../StatusBadge.svelte";
  import { api } from "../../lib/api";
  import { notify } from "../../lib/toast";

  export let runs: { pipeline: Pipeline; run: Run }[] = [];
  // DB-backed 24h counts. The sample above is bounded, so counting it would
  // understate a pipeline run more times than the sample holds.
  export let rollups: PipelineRollup[] = [];
  // How many groups to render. The dashboard is a triage surface, not a log
  // browser — the rest live behind "View all".
  export let maxGroups = 8;

  const dispatch = createEventDispatcher<{ changed: void }>();

  let expanded: Record<string, boolean> = {};
  let busy: Record<string, boolean> = {};

  $: rollupById = new Map(rollups.map(r => [r.pipeline_id, r]));

  // Collapse *consecutive* runs of the same pipeline into one group.
  // Consecutive rather than global so the list still reads chronologically:
  // a pipeline that ran, then another ran, then the first ran again stays
  // in time order instead of being reordered into per-pipeline buckets.
  $: groups = (() => {
    const out: {
      key: string;
      pipeline: Pipeline;
      items: { pipeline: Pipeline; run: Run }[];
      failed: number;
      running: number;
      rollup?: PipelineRollup;
    }[] = [];

    for (const entry of runs) {
      const last = out[out.length - 1];
      if (last && last.pipeline.id === entry.pipeline.id) {
        last.items.push(entry);
      } else {
        out.push({
          key: `${entry.pipeline.id}:${entry.run.id}`,
          pipeline: entry.pipeline,
          items: [entry],
          failed: 0,
          running: 0,
          rollup: rollupById.get(entry.pipeline.id),
        });
      }
    }
    for (const g of out) {
      g.failed = g.items.filter(i => i.run.status === "failed").length;
      g.running = g.items.filter(i => i.run.status === "running").length;
    }
    return out;
  })();

  $: visible = groups.slice(0, maxGroups);
  $: hiddenGroups = Math.max(0, groups.length - maxGroups);

  // A group is only collapsible if it actually collapses something. A group
  // containing a failure is never silently collapsed — see the markup: its
  // failed runs are always listed, because a green "28 succeeded" row that
  // quietly contains a failure is worse than no grouping at all.
  function isCollapsible(g: { items: unknown[] }): boolean {
    return g.items.length > 1;
  }

  async function rerun(pipeline: Pipeline, key: string) {
    if (busy[key]) return;
    busy = { ...busy, [key]: true };
    try {
      await api.runs.trigger(pipeline.id);
      notify.success(`Rerunning ${pipeline.name}`);
      dispatch("changed");
    } catch (e: any) {
      notify.error(e?.message || "Failed to start run");
    } finally {
      busy = { ...busy, [key]: false };
    }
  }

  async function cancel(run: Run, key: string) {
    if (busy[key]) return;
    busy = { ...busy, [key]: true };
    try {
      await api.runs.cancel(run.id);
      notify.success("Run cancelled");
      dispatch("changed");
    } catch (e: any) {
      notify.error(e?.message || "Failed to cancel run");
    } finally {
      busy = { ...busy, [key]: false };
    }
  }

  // Runs that failed the same way are one event, not N. A pipeline retrying
  // against a dead endpoint produces a wall of identical rows that costs N
  // lines to say one thing — and buries any *differently* failing run in the
  // middle of it. Clustering is by error signature, and the count and time
  // span stay on the row, so nothing is hidden: 18 failures still read as 18.
  function signature(run: Run): string {
    if (run.status !== "failed") return run.status;
    return `failed::${(run.error || "").split("\n")[0].trim()}`;
  }

  // Consecutive, so the list keeps reading chronologically instead of being
  // reordered into per-error buckets.
  function cluster(items: { pipeline: Pipeline; run: Run }[]) {
    const out: {
      key: string;
      status: string;
      error?: string;
      items: { pipeline: Pipeline; run: Run }[];
    }[] = [];
    for (const it of items) {
      const sig = signature(it.run);
      const last = out[out.length - 1];
      if (last && last.key === sig) {
        last.items.push(it);
      } else {
        out.push({ key: sig, status: it.run.status, error: it.run.error, items: [it] });
      }
    }
    return out;
  }

  // Newest first in, so the span reads oldest → newest.
  function spanLabel(items: { run: Run }[]): string {
    if (items.length === 1) return timeAgo(items[0].run.started_at);
    const newest = timeAgo(items[0].run.started_at);
    const oldest = timeAgo(items[items.length - 1].run.started_at);
    return newest === oldest ? newest : `${newest} – ${oldest}`;
  }

  function timeAgo(dateStr?: string | null): string {
    if (!dateStr) return "";
    const s = Math.floor((Date.now() - new Date(dateStr).getTime()) / 1000);
    if (s < 60) return `${s}s ago`;
    if (s < 3600) return `${Math.floor(s / 60)}m ago`;
    if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
    return `${Math.floor(s / 86400)}d ago`;
  }

  function duration(run: Run): string {
    if (!run.started_at || !run.finished_at) return "";
    const ms = new Date(run.finished_at).getTime() - new Date(run.started_at).getTime();
    if (ms < 1000) return `${ms}ms`;
    if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`;
    return `${Math.floor(ms / 60_000)}m ${Math.floor((ms % 60_000) / 1000)}s`;
  }
</script>

{#if visible.length === 0}
  <div class="empty">No runs yet.</div>
{:else}
  <div class="groups">
    {#each visible as g (g.key)}
      <div class="group" class:has-failure={g.failed > 0}>
        <div class="row">
          <div class="row-main">
            <a class="name" href="#/pipelines/{g.pipeline.id}/runs">{g.pipeline.name}</a>

            {#if isCollapsible(g)}
              <button
                class="chip"
                on:click={() => (expanded = { ...expanded, [g.key]: !expanded[g.key] })}
                aria-expanded={!!expanded[g.key]}
              >
                ×{g.items.length}
                <span class="caret">{expanded[g.key] ? "▾" : "▸"}</span>
              </button>
            {/if}

            {#if g.rollup}
              <!-- The honest number: DB-backed 24h totals, independent of
                   how many runs the bounded sample happens to hold. -->
              <span class="counts">
                <span class="ok">{g.rollup.success} ok</span>
                {#if g.rollup.failed > 0}
                  <span class="bad">{g.rollup.failed} failed</span>
                {/if}
                <span class="dim">/ 24h</span>
              </span>
            {/if}
          </div>

          <div class="row-side">
            <StatusBadge status={g.items[0].run.status} size="sm" />
            <span class="meta">{duration(g.items[0].run)}</span>
            <span class="meta">{timeAgo(g.items[0].run.started_at)}</span>
            <div class="actions">
              <a class="act" href="#/pipelines/{g.pipeline.id}/runs" title="View logs">Logs</a>
              {#if g.items[0].run.status === "running"}
                <button
                  class="act"
                  disabled={busy[g.key]}
                  on:click={() => cancel(g.items[0].run, g.key)}
                  title="Cancel run"
                >Cancel</button>
              {:else}
                <button
                  class="act"
                  disabled={busy[g.key]}
                  on:click={() => rerun(g.pipeline, g.key)}
                  title="Run again"
                >Rerun</button>
              {/if}
            </div>
          </div>
        </div>

        <!-- Failures are listed whether or not the group is expanded.
             Collapsing must never be the reason someone misses one. -->
        {#if g.failed > 0 && !expanded[g.key]}
          <div class="sub-rows">
            {#each cluster(g.items.filter(i => i.run.status === "failed")) as c (c.key + c.items[0].run.id)}
              <a class="sub-row failed" href="#/pipelines/{c.items[0].pipeline.id}/runs">
                <span class="sub-dot"></span>
                <span class="sub-label">failed</span>
                {#if c.items.length > 1}
                  <span class="sub-count">×{c.items.length}</span>
                {/if}
                <span class="sub-meta">{spanLabel(c.items)}</span>
                {#if c.error}
                  <span class="sub-err" title={c.error}>{c.error.split("\n")[0]}</span>
                {/if}
              </a>
            {/each}
          </div>
        {/if}

        {#if expanded[g.key]}
          <div class="sub-rows">
            {#each cluster(g.items) as c (c.key + c.items[0].run.id)}
              <a
                class="sub-row"
                class:failed={c.status === "failed"}
                href="#/pipelines/{c.items[0].pipeline.id}/runs"
              >
                <span class="sub-dot"></span>
                <span class="sub-label">{c.status}</span>
                {#if c.items.length > 1}
                  <span class="sub-count">×{c.items.length}</span>
                {/if}
                <span class="sub-meta">{duration(c.items[0].run)}</span>
                <span class="sub-meta">{spanLabel(c.items)}</span>
                {#if c.error}
                  <span class="sub-err" title={c.error}>{c.error.split("\n")[0]}</span>
                {/if}
              </a>
            {/each}
          </div>
        {/if}
      </div>
    {/each}
  </div>

  {#if hiddenGroups > 0}
    <a class="more" href="#/pipelines">
      {hiddenGroups} more {hiddenGroups === 1 ? "pipeline" : "pipelines"} with recent runs →
    </a>
  {/if}
{/if}

<style>
  .groups { min-width: 0; }

  .group { border-bottom: 1px solid var(--border-subtle); }
  .group:last-child { border-bottom: none; }
  .group.has-failure { border-left: 2px solid var(--failed); }

  .row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-md);
    padding: 8px 14px;
    min-height: 38px;
  }
  .row:hover { background: var(--bg-tertiary); }

  .row-main {
    display: flex;
    align-items: center;
    gap: var(--space-sm);
    min-width: 0;
    flex: 1;
  }

  .name {
    font-size: 0.8125rem;
    font-weight: 500;
    color: var(--text-primary);
    text-decoration: none;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .name:hover { color: var(--accent); }

  .chip {
    font-family: var(--font-mono);
    font-size: 0.6875rem;
    font-weight: 700;
    color: var(--accent);
    background: var(--accent-glow);
    border: none;
    border-radius: var(--radius-sm);
    padding: 1px 5px;
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    gap: 3px;
  }
  .chip:hover { background: var(--accent-glow-strong); }
  .caret { font-size: 0.5625rem; }

  .counts {
    font-family: var(--font-mono);
    font-size: 0.6875rem;
    display: flex;
    gap: 6px;
    white-space: nowrap;
  }
  .ok  { color: var(--success); }
  .bad { color: var(--failed); }
  .dim { color: var(--text-dim); }

  .row-side {
    display: flex;
    align-items: center;
    gap: var(--space-sm);
    flex-shrink: 0;
  }

  .meta {
    font-family: var(--font-mono);
    font-size: 0.6875rem;
    color: var(--text-muted);
    white-space: nowrap;
  }

  /* Actions stay reserved-but-dim until hover, so the row doesn't reflow
     when they appear. */
  .actions {
    display: flex;
    gap: 4px;
    opacity: 0;
    transition: opacity var(--transition-fast);
  }
  .row:hover .actions,
  .actions:focus-within { opacity: 1; }

  .act {
    font-size: 0.6875rem;
    padding: 2px 7px;
    border-radius: var(--radius-sm);
    border: 1px solid var(--border);
    background: var(--bg-secondary);
    color: var(--text-secondary);
    cursor: pointer;
    text-decoration: none;
    white-space: nowrap;
  }
  .act:hover:not(:disabled) {
    color: var(--text-primary);
    border-color: var(--border-hover);
  }
  .act:disabled { opacity: 0.5; cursor: default; }

  .sub-rows {
    background: var(--bg-primary);
    border-top: 1px solid var(--border-subtle);
  }

  .sub-row {
    display: flex;
    align-items: center;
    gap: var(--space-sm);
    padding: 4px 14px 4px 28px;
    font-size: 0.6875rem;
    color: var(--text-muted);
    text-decoration: none;
  }
  .sub-row:hover { background: var(--bg-tertiary); }

  .sub-dot {
    width: 5px;
    height: 5px;
    border-radius: 50%;
    background: var(--text-dim);
    flex-shrink: 0;
  }
  .sub-row.failed .sub-dot { background: var(--failed); }
  .sub-row.failed .sub-label { color: var(--failed); }

  .sub-label { font-family: var(--font-mono); min-width: 60px; }

  .sub-count {
    font-family: var(--font-mono);
    font-size: 0.625rem;
    font-weight: 700;
    color: var(--text-secondary);
    background: var(--bg-tertiary);
    border-radius: var(--radius-sm);
    padding: 0 4px;
    flex-shrink: 0;
  }
  .sub-row.failed .sub-count { color: var(--failed); background: var(--failed-bg); }
  .sub-meta  { font-family: var(--font-mono); color: var(--text-dim); }

  .sub-err {
    font-family: var(--font-mono);
    color: var(--failed);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    min-width: 0;
  }

  .more {
    display: block;
    padding: 7px 14px;
    border-top: 1px solid var(--border-subtle);
    font-size: 0.75rem;
    color: var(--text-muted);
    text-decoration: none;
  }
  .more:hover { color: var(--accent); }

  .empty {
    padding: var(--space-lg);
    text-align: center;
    font-size: 0.8125rem;
    color: var(--text-muted);
  }

  @media (max-width: 768px) {
    /* .row-side is flex-shrink: 0 so the actions never get squeezed on a
       desktop row. On a phone that same rule sets a min-content width the
       row cannot go below — status badge + duration + timestamp + two
       buttons — which is what forced the card wider than the screen.
       Let the row wrap onto a second line instead, and drop the duration,
       which is the least useful of the three metadata items. */
    .row { flex-wrap: wrap; row-gap: 6px; }
    .row-main { flex: 1 1 100%; }
    .row-side { flex-shrink: 1; flex-wrap: wrap; width: 100%; justify-content: flex-end; }
    .row-side .meta:first-of-type { display: none; }
    .actions { opacity: 1; }
    .counts { display: none; }
    .sub-err { display: none; }
  }
</style>
