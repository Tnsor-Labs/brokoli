<script lang="ts">
  export let icon: string = "";
  export let title: string = "Nothing here yet";
  export let description: string = "";
  export let ctaLabel: string = "";
  export let ctaHref: string = "";
</script>

<div class="empty-block">
  {#if icon}
    <div class="empty-icon">
      <svg width="28" height="28" viewBox="0 0 24 24" fill="none">
        <path d={icon} stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
      </svg>
    </div>
  {/if}
  <h3 class="empty-title">{title}</h3>
  {#if description}
    <p class="empty-desc">{description}</p>
  {/if}
  {#if ctaLabel && ctaHref}
    <a href={ctaHref} class="empty-cta">{ctaLabel}</a>
  {:else if ctaLabel}
    <button class="empty-cta" on:click><slot name="action">{ctaLabel}</slot></button>
  {/if}
</div>

<style>
  .empty-block {
    display: flex; flex-direction: column; align-items: center;
    text-align: center; padding: 56px 24px;
  }
  .empty-icon {
    width: 56px; height: 56px; border-radius: 14px;
    background: var(--bg-secondary); border: 1px solid var(--border-subtle);
    display: flex; align-items: center; justify-content: center;
    color: var(--text-dim); margin-bottom: 20px;
  }
  .empty-title {
    font-size: 15px; font-weight: 600; color: var(--text-primary);
    margin-bottom: 6px; letter-spacing: -0.01em;
  }
  .empty-desc {
    font-size: 13px; color: var(--text-muted); line-height: 1.6;
    max-width: 360px; margin-bottom: 20px;
  }
  .empty-cta {
    display: inline-flex; align-items: center; gap: 6px;
    padding: 8px 18px; border-radius: 8px; font-size: 13px; font-weight: 500;
    background: var(--accent); color: white; border: none;
    text-decoration: none; cursor: pointer;
    transition: opacity 150ms ease;
  }
  .empty-cta:hover { opacity: 0.9; color: white; }
</style>
