<script lang="ts">
  import { afterUpdate } from "svelte";

  export let title: string;
  export let subtitle = "";
  export let count: number | null = null;
  export let tone: "neutral" | "warn" | "fail" | "accent" = "neutral";
  // Collapsed cap. Panels on a dashboard must not grow with their data —
  // one pipeline failing 200 times used to push everything below it off the
  // page and leave the column beside it blank.
  export let maxHeight = "300px";
  export let href = "";
  export let linkLabel = "View all";
  export let expandable = true;
  // Optional wider layout for the expanded view (tables want the room).
  export let wide = false;
  // Fill the height the parent gives it, instead of sizing to content. Used
  // where two columns sit side by side and their bottoms should line up.
  export let fill = false;

  let expanded = false;
  let bodyEl: HTMLDivElement;
  let overflowing = false;

  // Only offer "expand" when there is something the cap is actually hiding.
  // An expand control on a two-row panel is noise, and this is cheaper and
  // more honest than asking every caller to predict its own height.
  afterUpdate(() => {
    if (!bodyEl || expanded) return;
    const o = bodyEl.scrollHeight > bodyEl.clientHeight + 1;
    if (o !== overflowing) overflowing = o;
  });

  function onKeydown(e: KeyboardEvent) {
    if (e.key === "Escape" && expanded) expanded = false;
  }
</script>

<svelte:window on:keydown={onKeydown} />

{#if expanded}
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="scrim" on:click={() => (expanded = false)} on:keydown={() => {}}></div>
{/if}

<!-- The whole card is one bordered container: a tinted header band, then
     the body. The title used to float on the page background above a
     borderless body, which left cards with no visible top edge — you could
     not tell where one ended and the next began. Separation is carried by a
     monochrome tone step (header is --bg-tertiary against a white body),
     not by colour.

     Expanding promotes this same element to an overlay rather than mounting
     a second copy of the list in a modal. One DOM tree means the expanded
     view can never drift from the inline one. -->
<section class="panel" class:expanded class:wide class:fill>
  <header class="head">
    <h2 class="title">{title}</h2>
    {#if count !== null}
      <span class="count tone-{tone}">{count}</span>
    {/if}
    {#if subtitle}
      <span class="sub">{subtitle}</span>
    {/if}

    <div class="head-right">
      <slot name="actions" />
      {#if href}
        <a class="link" {href}>{linkLabel} →</a>
      {/if}
      {#if expandable && (overflowing || expanded)}
        <button
          class="icon-btn"
          on:click={() => (expanded = !expanded)}
          title={expanded ? "Collapse (Esc)" : "Expand"}
          aria-label={expanded ? "Collapse panel" : "Expand panel"}
        >
          {#if expanded}
            <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round">
              <path d="M9 3v6H3M15 21v-6h6M21 9h-6V3M3 15h6v6" />
            </svg>
          {:else}
            <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round">
              <path d="M15 3h6v6M9 21H3v-6M21 3l-7 7M3 21l7-7" />
            </svg>
          {/if}
        </button>
      {/if}
    </div>
  </header>

  <div class="body" bind:this={bodyEl} style="--panel-max: {maxHeight}">
    <slot />
  </div>

  {#if expanded}
    <footer class="foot">
      <span class="foot-hint">Esc to close</span>
    </footer>
  {/if}
</section>

<style>
  .panel {
    display: flex;
    flex-direction: column;
    min-width: 0;
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius-lg);
    background: var(--bg-secondary);
    /* Dark themes get depth from a lighter fill; light themes have nothing
       lighter than the card, so the lift has to come from shadow. */
    box-shadow: var(--shadow-card);
    overflow: hidden;
  }

  .head {
    display: flex;
    align-items: baseline;
    gap: var(--space-sm);
    padding: 7px 12px;
    background: var(--bg-tertiary);
    border-bottom: 1px solid var(--border-subtle);
  }

  .title {
    font-size: 0.6875rem;
    font-weight: 700;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: var(--text-muted);
    margin: 0;
    white-space: nowrap;
  }

  .count {
    font-family: var(--font-mono);
    font-size: 0.625rem;
    font-weight: 700;
    border-radius: var(--radius-sm);
    padding: 1px 5px;
    color: var(--text-secondary);
    background: var(--bg-tertiary);
  }
  .count.tone-warn   { color: var(--warning); background: var(--warning-bg); }
  .count.tone-fail   { color: var(--failed);  background: var(--failed-bg); }
  .count.tone-accent { color: var(--accent);  background: var(--accent-glow); }

  .sub {
    font-size: 0.6875rem;
    color: var(--text-dim);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .head-right {
    margin-left: auto;
    display: flex;
    align-items: center;
    gap: var(--space-sm);
    flex-shrink: 0;
  }

  .link {
    font-size: 0.6875rem;
    color: var(--text-dim);
    text-decoration: none;
    white-space: nowrap;
  }
  .link:hover { color: var(--accent); }

  .icon-btn {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 20px;
    height: 20px;
    padding: 0;
    border: none;
    background: none;
    color: var(--text-dim);
    cursor: pointer;
    border-radius: var(--radius-sm);
  }
  .icon-btn:hover { color: var(--text-primary); background: var(--bg-tertiary); }

  /* Fill mode: the panel takes the height the grid row gives it and the
     body absorbs the slack, so neighbouring columns end on the same line
     regardless of how much content each holds. */
  .panel.fill { height: 100%; min-height: 0; }
  /* max-height is dropped here on purpose: the grid container caps the row,
     and re-applying the per-panel cap would leave dead space inside the
     card whenever the neighbouring column is taller. */
  .panel.fill .body { flex: 1 1 auto; max-height: none; }
  .panel.fill.expanded { height: auto; }
  .panel.fill.expanded .body { flex: 0 1 auto; }

  .body {
    background: var(--bg-secondary);
    overflow-y: auto;
    /* auto, not hidden: a table too wide for a phone should scroll inside
       its own card, not have its right-hand columns silently cut off. */
    overflow-x: auto;
    max-height: var(--panel-max);
    /* The cap only bites once there is enough content to reach it, so short
       panels still size to their content instead of leaving a gap. */
    min-height: 0;
  }

  /* ── Expanded ───────────────────────────────────────────────────────── */

  .scrim {
    position: fixed;
    inset: 0;
    background: var(--bg-overlay);
    z-index: 60;
  }

  .panel.expanded {
    position: fixed;
    top: 5vh;
    left: 50%;
    transform: translateX(-50%);
    width: min(760px, 92vw);
    max-height: 90vh;
    z-index: 70;
    border-color: var(--border);
    box-shadow: var(--shadow-lg);
  }
  .panel.expanded.wide { width: min(1200px, 95vw); }

  .panel.expanded .body { max-height: calc(90vh - 70px); }

  .foot {
    display: flex;
    justify-content: flex-end;
    padding: 6px 12px;
    background: var(--bg-tertiary);
    border-top: 1px solid var(--border-subtle);
  }
  .foot-hint {
    font-size: 0.625rem;
    color: var(--text-dim);
    font-family: var(--font-mono);
  }

  @media (max-width: 768px) {
    .panel.expanded {
      top: 2vh;
      width: 96vw;
      max-height: 96vh;
    }
    .panel.expanded .body { max-height: calc(96vh - 70px); }
  }
</style>
