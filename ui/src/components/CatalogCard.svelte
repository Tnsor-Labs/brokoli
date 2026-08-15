<script lang="ts">
  // A catalog entry card: icon tile + name + one-line description, the
  // click target for "create X". Shared so every catalog (connections
  // today, node palette or plugin browser tomorrow) renders identically.
  export let title: string;
  export let description = "";
  export let monogram = ""; // fallback tile text when no glyph is available
  export let iconD = ""; // icons.ts path data — wins over monogram
  export let category = "other"; // tint: database | storage | api | other
  export let selected = false;
</script>

<button type="button" class="catalog-card cat-{category}" class:selected on:click>
  <span class="tile" aria-hidden="true">
    {#if iconD}
      <svg width="22" height="22" viewBox="0 0 24 24" fill="none">
        <path
          d={iconD}
          stroke="currentColor"
          stroke-width="1.5"
          stroke-linecap="round"
          stroke-linejoin="round"
        />
      </svg>
    {:else}
      <span class="monogram">{monogram || title.slice(0, 2).toUpperCase()}</span>
    {/if}
  </span>
  <span class="copy">
    <strong>{title}</strong>
    {#if description}<small>{description}</small>{/if}
  </span>
</button>

<style>
  .catalog-card {
    display: flex;
    align-items: center;
    gap: 12px;
    width: 100%;
    padding: 12px 14px;
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius-lg);
    background: var(--bg-secondary);
    text-align: left;
    cursor: pointer;
    transition:
      border-color 150ms ease,
      background 150ms ease,
      transform 150ms ease;
  }
  .catalog-card:hover {
    border-color: var(--border-hover);
    background: var(--bg-card-hover);
    transform: translateY(-1px);
  }
  .catalog-card:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: 1px;
  }
  .catalog-card.selected {
    border-color: var(--accent);
    background: var(--accent-glow);
  }

  .tile {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 40px;
    height: 40px;
    border-radius: var(--radius-md);
    flex-shrink: 0;
  }
  .monogram {
    font-family: var(--font-mono);
    font-size: 13px;
    font-weight: 700;
    letter-spacing: 0.02em;
  }

  /* Category tints — existing status/accent tokens only. */
  .cat-database .tile {
    background: var(--accent-glow);
    color: var(--accent);
  }
  .cat-storage .tile {
    background: var(--success-bg);
    color: var(--success);
  }
  .cat-api .tile {
    background: var(--warning-bg);
    color: var(--warning);
  }
  .cat-other .tile {
    background: var(--bg-tertiary);
    color: var(--text-muted);
  }

  .copy {
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
  }
  .copy strong {
    color: var(--text-primary);
    font-size: 13px;
    font-weight: 600;
  }
  .copy small {
    color: var(--text-muted);
    font-size: 11px;
    line-height: 1.4;
    display: -webkit-box;
    -webkit-line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }
</style>
