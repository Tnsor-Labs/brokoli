<script lang="ts">
  export let items: Array<{ label: string; href?: string }> = [];
</script>

<nav class="breadcrumb">
  {#each items as item, i}
    {#if i > 0}
      <span class="bc-sep">/</span>
    {/if}
    {#if item.href && i < items.length - 1}
      <a href={item.href} class="bc-link">{item.label}</a>
    {:else}
      <span class="bc-current">{item.label}</span>
    {/if}
  {/each}
</nav>

<style>
  .breadcrumb {
    display: flex; align-items: center; gap: 6px;
    font-size: 13px; margin-bottom: 12px;
  }
  .bc-sep { color: var(--text-ghost); font-size: 11px; }
  .bc-link {
    color: var(--text-muted); text-decoration: none;
    transition: color 150ms ease;
  }
  .bc-link:hover { color: var(--text-primary); }
  .bc-current { color: var(--text-primary); font-weight: 500; }
</style>
