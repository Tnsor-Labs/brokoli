<script lang="ts">
  import { createEventDispatcher } from "svelte";

  export let total: number = 0;
  export let page: number = 1;
  export let pageSize: number = 25;

  const dispatch = createEventDispatcher();

  $: totalPages = Math.max(1, Math.ceil(total / pageSize));
  $: start = (page - 1) * pageSize + 1;
  $: end = Math.min(page * pageSize, total);

  // Visible page buttons — show max 7, with ellipsis
  $: visiblePages = computeVisiblePages(page, totalPages);

  function computeVisiblePages(current: number, last: number): (number | "...")[] {
    if (last <= 7) return Array.from({ length: last }, (_, i) => i + 1);
    const pages: (number | "...")[] = [];
    if (current <= 4) {
      for (let i = 1; i <= 5; i++) pages.push(i);
      pages.push("...", last);
    } else if (current >= last - 3) {
      pages.push(1, "...");
      for (let i = last - 4; i <= last; i++) pages.push(i);
    } else {
      pages.push(1, "...", current - 1, current, current + 1, "...", last);
    }
    return pages;
  }

  function goTo(p: number) {
    if (p < 1 || p > totalPages || p === page) return;
    dispatch("page", p);
  }

  function changePageSize(size: number) {
    dispatch("pagesize", size);
  }
</script>

{#if total > 0}
  <div class="pagination">
    <div class="pg-info">
      <span class="pg-range">{start}–{end}</span>
      <span class="pg-of">of</span>
      <span class="pg-total">{total}</span>
    </div>

    <div class="pg-controls">
      <button class="pg-btn" disabled={page <= 1} on:click={() => goTo(page - 1)} title="Previous">
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none">
          <path d="M15 18l-6-6 6-6" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" />
        </svg>
      </button>
      {#each visiblePages as p}
        {#if p === "..."}
          <span class="pg-ellipsis">...</span>
        {:else}
          <button class="pg-btn pg-num" class:active={p === page} on:click={() => goTo(p)}>
            {p}
          </button>
        {/if}
      {/each}
      <button class="pg-btn" disabled={page >= totalPages} on:click={() => goTo(page + 1)} title="Next">
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none">
          <path d="M9 18l6-6-6-6" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" />
        </svg>
      </button>
    </div>

    <div class="pg-size">
      {#each [10, 25, 50, 100] as size}
        <button
          class="pg-size-btn"
          class:active={pageSize === size}
          on:click={() => changePageSize(size)}
        >{size}</button>
      {/each}
      <span class="pg-size-label">/ page</span>
    </div>
  </div>
{/if}

<style>
  .pagination {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 12px 0;
    margin-top: 8px;
    border-top: 1px solid var(--border-subtle);
    gap: 12px;
  }
  .pg-info {
    font-size: 12px; color: var(--text-muted);
    display: flex; align-items: center; gap: 4px;
    min-width: 100px;
  }
  .pg-range { font-family: var(--font-mono); font-weight: 600; color: var(--text-secondary); }
  .pg-total { font-family: var(--font-mono); font-weight: 600; color: var(--text-secondary); }
  .pg-of { color: var(--text-dim); }

  .pg-controls {
    display: flex; align-items: center; gap: 2px;
  }
  .pg-btn {
    display: flex; align-items: center; justify-content: center;
    width: 30px; height: 30px; border-radius: 6px;
    color: var(--text-muted); font-size: 12px; font-weight: 500;
    transition: all 120ms ease;
  }
  .pg-btn:hover:not(:disabled):not(.active) {
    background: var(--bg-tertiary); color: var(--text-primary);
  }
  .pg-btn:disabled { opacity: 0.3; cursor: default; }
  .pg-btn.pg-num { font-family: var(--font-mono); }
  .pg-btn.active {
    background: var(--accent); color: white; font-weight: 600;
  }
  .pg-ellipsis {
    width: 24px; text-align: center;
    color: var(--text-ghost); font-size: 12px;
  }

  .pg-size {
    display: flex; align-items: center; gap: 2px;
  }
  .pg-size-btn {
    padding: 4px 8px; border-radius: 4px;
    font-size: 11px; font-family: var(--font-mono);
    color: var(--text-dim); transition: all 120ms ease;
  }
  .pg-size-btn:hover { background: var(--bg-tertiary); color: var(--text-secondary); }
  .pg-size-btn.active { background: var(--bg-tertiary); color: var(--text-primary); font-weight: 600; }
  .pg-size-label { font-size: 10px; color: var(--text-ghost); margin-left: 2px; }
</style>
