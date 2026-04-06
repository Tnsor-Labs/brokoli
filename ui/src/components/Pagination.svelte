<script lang="ts">
  import { createEventDispatcher } from "svelte";

  export let total: number = 0;
  export let page: number = 1;
  export let pageSize: number = 25;

  const dispatch = createEventDispatcher();

  $: totalPages = Math.max(1, Math.ceil(total / pageSize));
  $: showPagination = total > pageSize;

  function goTo(p: number) {
    if (p < 1 || p > totalPages || p === page) return;
    dispatch("page", p);
  }

  function changePageSize(size: number) {
    dispatch("pagesize", size);
  }
</script>

{#if showPagination}
  <div class="pagination">
    <button class="pg-nav" disabled={page <= 1} on:click={() => goTo(page - 1)}>
      <svg width="14" height="14" viewBox="0 0 24 24" fill="none">
        <path d="M15 18l-6-6 6-6" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" />
      </svg>
      Previous
    </button>

    <span class="pg-info">Page {page} of {totalPages}</span>

    <button class="pg-nav" disabled={page >= totalPages} on:click={() => goTo(page + 1)}>
      Next
      <svg width="14" height="14" viewBox="0 0 24 24" fill="none">
        <path d="M9 18l6-6-6-6" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" />
      </svg>
    </button>
  </div>
{/if}

<style>
  .pagination {
    display: flex; align-items: center; justify-content: center;
    gap: 16px; padding: 14px 0; margin-top: 4px;
  }
  .pg-nav {
    display: flex; align-items: center; gap: 4px;
    padding: 6px 14px; border-radius: 8px;
    font-size: 12px; font-weight: 500; color: var(--text-muted);
    background: none; border: 1px solid var(--border-subtle);
    transition: all 150ms ease; cursor: pointer;
  }
  .pg-nav:hover:not(:disabled) {
    border-color: var(--border); color: var(--text-primary);
  }
  .pg-nav:disabled { opacity: 0.25; cursor: default; }
  .pg-info {
    font-size: 12px; color: var(--text-dim);
    font-family: var(--font-mono);
  }
</style>
