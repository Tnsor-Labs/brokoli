<script lang="ts">
  export let trends: { date: string; success: number; failed: number; total: number }[] = [];

  // Compact by design. The previous 7-day chart consumed a third of the
  // screen to show six zero-bars; a sparkline carries the same shape in a
  // strip, and the detail lives in the tooltip.
  $: max = Math.max(...trends.map(t => t.total), 1);
  $: hasAny = trends.some(t => t.total > 0);

  let hovered = -1;

  function label(t: { date: string; success: number; failed: number; total: number }): string {
    return `${t.date}: ${t.total} run${t.total === 1 ? "" : "s"}, ${t.success} ok, ${t.failed} failed`;
  }
</script>

<div class="spark" title={hasAny ? "" : "No runs in the last 7 days"}>
  <span class="cap">7d</span>
  <div class="bars">
    {#each trends as t, i}
      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <div
        class="bar-slot"
        title={label(t)}
        on:mouseenter={() => (hovered = i)}
        on:mouseleave={() => (hovered = -1)}
      >
        {#if t.failed > 0}
          <div class="bar bar-failed" style="height: {Math.max(2, (t.failed / max) * 100)}%"></div>
        {/if}
        {#if t.success > 0}
          <div class="bar bar-success" style="height: {Math.max(2, (t.success / max) * 100)}%"></div>
        {/if}
        {#if t.total === 0}
          <div class="bar bar-empty"></div>
        {/if}
      </div>
    {/each}
  </div>
  <span class="cap cap-value">
    {hovered >= 0 && trends[hovered] ? trends[hovered].total : trends.reduce((s, t) => s + t.total, 0)}
  </span>
</div>

<style>
  .spark {
    display: flex;
    align-items: center;
    gap: var(--space-sm);
    padding: 10px 14px;
    min-width: 130px;
  }

  .cap {
    font-family: var(--font-mono);
    font-size: 0.625rem;
    color: var(--text-dim);
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }
  .cap-value {
    color: var(--text-secondary);
    font-weight: 700;
    min-width: 22px;
    text-align: right;
  }

  .bars {
    display: flex;
    align-items: flex-end;
    gap: 2px;
    height: 24px;
    flex: 1;
  }

  .bar-slot {
    flex: 1;
    height: 100%;
    display: flex;
    flex-direction: column;
    justify-content: flex-end;
    gap: 1px;
    min-width: 4px;
    cursor: default;
  }

  .bar { width: 100%; border-radius: 1px; }
  .bar-success { background: var(--success); opacity: 0.75; }
  .bar-failed  { background: var(--failed); }
  .bar-empty {
    height: 2px;
    background: var(--border-subtle);
  }
  .bar-slot:hover .bar { opacity: 1; }
</style>
