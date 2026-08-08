<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import type { Pipeline, PipelineRollup } from "../../lib/types";
  import { api } from "../../lib/api";
  import { notify } from "../../lib/toast";
  import Panel from "./Panel.svelte";

  export let pipelines: Pipeline[] = [];
  export let rollups: PipelineRollup[] = [];
  // pipeline name → next scheduled run, from /api/scheduler/status.
  export let nextRuns: Record<string, string> = {};

  const dispatch = createEventDispatcher<{ changed: void }>();

  let busy: Record<string, boolean> = {};

  $: rollupById = new Map(rollups.map(r => [r.pipeline_id, r]));

  // Unhealthy first, then busy, then by name. The whole point of a fleet
  // table is that the one pipeline you need to look at is at the top even
  // when there are forty of them and the page is only tall enough for ten.
  $: ordered = [...pipelines].sort((a, b) => {
    const ra = rollupById.get(a.id);
    const rb = rollupById.get(b.id);
    const rank = (p: Pipeline, r?: PipelineRollup) => {
      if (r && r.failed > 0) return 0;
      if (r && r.running > 0) return 1;
      if (!p.enabled) return 3;
      return 2;
    };
    const d = rank(a, ra) - rank(b, rb);
    return d !== 0 ? d : a.name.localeCompare(b.name);
  });

  function healthPct(r?: PipelineRollup): number | null {
    if (!r) return null;
    const done = r.success + r.failed;
    if (done === 0) return null;
    return Math.round((r.success / done) * 100);
  }

  async function runNow(p: Pipeline) {
    if (busy[p.id]) return;
    busy = { ...busy, [p.id]: true };
    try {
      await api.runs.trigger(p.id);
      notify.success(`Started ${p.name}`);
      dispatch("changed");
    } catch (e: any) {
      notify.error(e?.message || `Failed to start ${p.name}`);
    } finally {
      busy = { ...busy, [p.id]: false };
    }
  }

  function formatNext(isoStr?: string): string {
    if (!isoStr) return "";
    const diffMs = new Date(isoStr).getTime() - Date.now();
    if (diffMs < 0) return "overdue";
    const mins = Math.floor(diffMs / 60000);
    if (mins < 60) return `in ${mins}m`;
    const hrs = Math.floor(mins / 60);
    if (hrs < 24) return `in ${hrs}h`;
    return `in ${Math.floor(hrs / 24)}d`;
  }

  function timeAgo(dateStr?: string): string {
    if (!dateStr) return "never";
    const s = Math.floor((Date.now() - new Date(dateStr).getTime()) / 1000);
    if (s < 60) return `${s}s ago`;
    if (s < 3600) return `${Math.floor(s / 60)}m ago`;
    if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
    return `${Math.floor(s / 86400)}d ago`;
  }
</script>

<Panel
  title="Pipelines"
  subtitle="{pipelines.length} total · last 24h"
  href="#/pipelines"
  linkLabel="Manage"
  maxHeight="330px"
  wide
>
  <div class="table">
    <!-- Each header cell carries the same class as the column it labels, so
         a rule that hides a column hides its header with it. Without that
         the header keeps 7 cells while the rows drop to 3, and the surplus
         wraps onto extra grid rows. -->
    <div class="row row-head">
      <span>Pipeline</span>
      <span class="sched">Schedule</span>
      <span class="last">Last run</span>
      <span class="counts num">24h</span>
      <span class="health-cell">Health</span>
      <span class="next">Next</span>
      <span class="actions"></span>
    </div>

    {#each ordered as p (p.id)}
      {@const r = rollupById.get(p.id)}
      {@const pct = healthPct(r)}
      <div class="row" class:paused={!p.enabled}>
        <a class="name" href="#/pipelines/{p.id}/runs" title={p.name}>{p.name}</a>

        <span class="sched">
          {#if !p.enabled}
            <span class="tag">Paused</span>
          {:else if p.schedule}
            <code>{p.schedule}</code>
          {:else}
            <span class="dim">Manual</span>
          {/if}
        </span>

        <span class="last">
          {#if r?.last_status}
            <span class="dot dot-{r.last_status}"></span>
            <span class="last-when">{timeAgo(r.last_started_at)}</span>
          {:else}
            <span class="dim">never</span>
          {/if}
        </span>

        <span class="counts num">
          {#if r && r.total > 0}
            <span class="ok">{r.success}</span>
            {#if r.failed > 0}<span class="bad">{r.failed}</span>{/if}
            {#if r.running > 0}<span class="run">{r.running}</span>{/if}
          {:else}
            <span class="dim">—</span>
          {/if}
        </span>

        <span class="health-cell">
          {#if pct === null}
            <span class="dim">—</span>
          {:else}
            <span class="bar" title="{pct}% of completed runs succeeded in the last 24h">
              <span
                class="bar-fill"
                class:warn={pct < 95 && pct >= 80}
                class:bad={pct < 80}
                style="width: {pct}%"
              ></span>
            </span>
            <span class="pct" class:warn={pct < 95 && pct >= 80} class:bad={pct < 80}>{pct}%</span>
          {/if}
        </span>

        <span class="next">
          {#if p.enabled && nextRuns[p.name]}
            {formatNext(nextRuns[p.name])}
          {:else}
            <span class="dim">—</span>
          {/if}
        </span>

        <span class="actions">
          <button class="btn" disabled={busy[p.id]} on:click={() => runNow(p)}>
            {busy[p.id] ? "…" : "Run"}
          </button>
          <a class="btn" href="#/pipelines/{p.id}">Open</a>
        </span>
      </div>
    {/each}

    {#if pipelines.length === 0}
      <div class="empty">No pipelines yet.</div>
    {/if}
  </div>
</Panel>

<style>
  /* Border/background/rounding live on Panel's body. */
  .table { min-width: 0; }

  .row {
    display: grid;
    grid-template-columns: minmax(140px, 2fr) 1.2fr 1.1fr 90px 120px 80px auto;
    align-items: center;
    gap: var(--space-md);
    padding: 8px 14px;
    border-bottom: 1px solid var(--border-subtle);
    font-size: 0.75rem;
  }
  .row:last-child { border-bottom: none; }
  .row:not(.row-head):hover { background: var(--bg-tertiary); }
  .row.paused { opacity: 0.6; }

  /* Column labels, not a second header band — the panel already provides
     one directly above, and two stacked tinted bars read as a mistake. */
  .row-head {
    position: sticky;
    top: 0;
    z-index: 1;
    font-size: 0.625rem;
    font-weight: 700;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: var(--text-dim);
    background: var(--bg-secondary);
    padding: 6px 14px;
  }
  .row-head:hover { background: var(--bg-secondary); }
  /* Header cells are plain labels: undo the flex/opacity treatment the data
     cells of the same class get. */
  .row-head .last,
  .row-head .counts,
  .row-head .health-cell { display: block; }
  .row-head .actions { opacity: 1; }

  .num { text-align: left; }

  .name {
    font-weight: 600;
    color: var(--text-primary);
    text-decoration: none;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .name:hover { color: var(--accent); }

  .sched code {
    font-family: var(--font-mono);
    font-size: 0.6875rem;
    color: var(--text-secondary);
  }

  .tag {
    font-size: 0.625rem;
    padding: 1px 5px;
    border-radius: var(--radius-sm);
    background: var(--pending-bg);
    color: var(--pending);
  }

  .dim { color: var(--text-dim); }

  .last { display: flex; align-items: center; gap: 6px; }
  .last-when { color: var(--text-muted); font-family: var(--font-mono); font-size: 0.6875rem; }

  .dot { width: 6px; height: 6px; border-radius: 50%; flex-shrink: 0; }
  .dot-success { background: var(--success); }
  .dot-failed  { background: var(--failed); }
  .dot-running { background: var(--running); }
  .dot-pending, .dot-cancelled { background: var(--pending); }

  .counts { display: flex; gap: 6px; font-family: var(--font-mono); font-size: 0.6875rem; }
  .counts .ok  { color: var(--success); }
  .counts .bad { color: var(--failed); font-weight: 700; }
  .counts .run { color: var(--running); }

  .health-cell { display: flex; align-items: center; gap: 6px; }
  .bar {
    flex: 1;
    height: 4px;
    min-width: 40px;
    border-radius: 2px;
    background: var(--bg-tertiary);
    overflow: hidden;
  }
  .bar-fill { display: block; height: 100%; background: var(--success); }
  .bar-fill.warn { background: var(--warning); }
  .bar-fill.bad  { background: var(--failed); }
  .pct {
    font-family: var(--font-mono);
    font-size: 0.625rem;
    color: var(--text-muted);
    min-width: 30px;
    text-align: right;
  }
  .pct.warn { color: var(--warning); }
  .pct.bad  { color: var(--failed); }

  .next {
    font-family: var(--font-mono);
    font-size: 0.6875rem;
    color: var(--text-muted);
  }

  /* Actions stay hidden until hover so the table reads as data, not as a
     wall of buttons — same treatment as the runs list. */
  .actions { display: flex; gap: 4px; opacity: 0; transition: opacity var(--transition-fast); }
  .row:hover .actions { opacity: 1; }
  .actions:focus-within { opacity: 1; }

  .btn {
    font-size: 0.6875rem;
    padding: 2px 8px;
    border-radius: var(--radius-sm);
    border: 1px solid var(--border);
    background: var(--bg-tertiary);
    color: var(--text-secondary);
    cursor: pointer;
    text-decoration: none;
    white-space: nowrap;
  }
  .btn:hover:not(:disabled) {
    background: var(--bg-card-hover);
    color: var(--text-primary);
    border-color: var(--border-hover);
  }
  .btn:disabled { opacity: 0.6; cursor: default; }

  .empty { padding: var(--space-md); text-align: center; color: var(--text-dim); font-size: 0.75rem; }

  /* Below tablet the middle columns are the first to go — name, last run
     and actions are what you still need on a phone. */
  @media (max-width: 1100px) {
    .row { grid-template-columns: minmax(120px, 2fr) 1.1fr 90px 110px auto; }
    .sched, .next,
    .row-head .sched, .row-head .next { display: none; }
  }
  @media (max-width: 768px) {
    .row { grid-template-columns: minmax(0, 1fr) auto auto; gap: var(--space-sm); }
    .health-cell, .counts,
    .row-head .health-cell, .row-head .counts { display: none; }
    .actions { opacity: 1; }
  }
</style>
