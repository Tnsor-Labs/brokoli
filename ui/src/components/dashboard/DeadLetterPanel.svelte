<script lang="ts">
  import { onMount } from "svelte";
  import type { DLQEntry } from "../../lib/types";
  import { api } from "../../lib/api";
  import { notify } from "../../lib/toast";
  import Panel from "./Panel.svelte";

  export let fill = false;

  let entries: DLQEntry[] = [];
  let busy: Record<string, boolean> = {};
  let loaded = false;

  export async function reload() {
    try {
      // The panel scrolls and expands, so it no longer needs a tiny cap
      // to stay a sane size — fetch a useful page of the queue.
      entries = await api.dlq.list({ limit: 200 });
    } catch {
      // Silent: the panel hides itself when empty, and a failed fetch
      // shouldn't put an error card on a screen that's about other failures.
    } finally {
      loaded = true;
    }
  }

  onMount(reload);

  // One broken endpoint produces one dead letter per record, all identical.
  // Listing them individually turns a single fact into a wall — and pushes
  // a genuinely different failure out of sight below it. Grouped by
  // pipeline + node + error, newest group first, with the count on the row.
  interface DLQGroup {
    key: string;
    entries: DLQEntry[];
    pipeline_id: string;
    pipeline_name?: string;
    node_name: string;
    error: string;
    latest: string;
  }

  $: groups = (() => {
    const by = new Map<string, DLQGroup>();
    for (const e of entries) {
      const key = `${e.pipeline_id}|${e.node_name}|${firstLine(e.error)}`;
      const g = by.get(key);
      if (g) {
        g.entries.push(e);
        if (e.created_at > g.latest) g.latest = e.created_at;
      } else {
        by.set(key, {
          key,
          entries: [e],
          pipeline_id: e.pipeline_id,
          pipeline_name: e.pipeline_name,
          node_name: e.node_name,
          error: e.error,
          latest: e.created_at,
        });
      }
    }
    return [...by.values()].sort((a, b) => b.latest.localeCompare(a.latest));
  })();

  // Resolving a group resolves every entry in it — the grouping is a display
  // decision, so the action has to apply to what was actually grouped.
  // Failures are reported but don't abort the rest: a partially resolved
  // group is better than stopping on the first bad id.
  async function resolveGroup(g: DLQGroup) {
    if (busy[g.key]) return;
    busy = { ...busy, [g.key]: true };
    const failures: string[] = [];
    for (const e of g.entries) {
      try {
        await api.dlq.resolve(e.pipeline_id, e.id);
      } catch (err: any) {
        failures.push(err?.message || e.id);
      }
    }
    const resolvedIds = new Set(g.entries.map(e => e.id));
    if (failures.length === 0) {
      entries = entries.filter(x => !resolvedIds.has(x.id));
      notify.success(
        g.entries.length === 1 ? "Marked resolved" : `Marked ${g.entries.length} resolved`,
      );
    } else {
      // Don't guess which ones landed — refetch and show what's really left.
      notify.error(`${failures.length} of ${g.entries.length} could not be resolved`);
      await reload();
    }
    busy = { ...busy, [g.key]: false };
  }

  function firstLine(err?: string): string {
    if (!err) return "";
    const l = err.split("\n")[0].trim();
    return l.length > 90 ? l.slice(0, 90) + "…" : l;
  }
</script>

<!-- Hidden entirely when the queue is empty. An always-present "0 dead
     letters" card is the kind of thing that trains people to stop reading
     the rail. -->
{#if loaded && entries.length > 0}
  <Panel title="Dead letters" count={entries.length} tone="warn" maxHeight="240px" {fill}>
    {#each groups as g (g.key)}
      <div class="item">
        <div class="item-top">
          <a class="pipe" href="#/pipelines/{g.pipeline_id}/runs">
            {g.pipeline_name || "Pipeline"}
          </a>
          {#if g.entries.length > 1}
            <span class="times">×{g.entries.length}</span>
          {/if}
          {#if g.node_name}<span class="node">{g.node_name}</span>{/if}
        </div>
        <p class="err" title={g.error}>{firstLine(g.error)}</p>
        <button class="link" disabled={busy[g.key]} on:click={() => resolveGroup(g)}>
          {#if busy[g.key]}
            …
          {:else if g.entries.length > 1}
            Mark {g.entries.length} resolved
          {:else}
            Mark resolved
          {/if}
        </button>
      </div>
    {/each}
  </Panel>
{/if}

<style>
  .item {
    padding: 8px 12px;
    border-bottom: 1px solid var(--border-subtle);
    min-width: 0;
  }
  .item:last-child { border-bottom: none; }
  .item:hover { background: var(--bg-tertiary); }

  .item-top { display: flex; align-items: baseline; gap: 6px; min-width: 0; }

  .pipe {
    font-size: 0.75rem;
    font-weight: 600;
    color: var(--text-primary);
    text-decoration: none;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .pipe:hover { color: var(--accent); }

  .times {
    font-family: var(--font-mono);
    font-size: 0.625rem;
    font-weight: 700;
    color: var(--warning);
    background: var(--warning-bg);
    border-radius: var(--radius-sm);
    padding: 0 4px;
    flex-shrink: 0;
  }

  .node {
    font-family: var(--font-mono);
    font-size: 0.5625rem;
    color: var(--text-dim);
  }

  .err {
    margin: 2px 0 4px;
    font-family: var(--font-mono);
    font-size: 0.625rem;
    color: var(--text-muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .link {
    font-size: 0.625rem;
    color: var(--text-dim);
    background: none;
    border: none;
    padding: 0;
    cursor: pointer;
  }
  .link:hover:not(:disabled) { color: var(--accent); }
  .link:disabled { opacity: 0.6; cursor: default; }

</style>
