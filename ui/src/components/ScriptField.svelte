<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import { icons } from "../lib/icons";

  /**
   * A config field whose value is code: a script, a query.
   *
   * The code node already had the right shape for this, a full-width
   * button into the editor with a preview of what is in there, while the
   * SQL fields had a four-line textarea with a small "Expand" link beside
   * the label. Two affordances for the same job, and the smaller one was
   * the worse place to write the longer thing.
   *
   * The button is tinted with the node's own taxonomy colour rather than
   * the code node's yellow, so the control reads as part of the node it
   * belongs to.
   */
  export let title: string;
  export let value: string = "";
  export let emptyLabel: string = "Nothing written yet";
  export let buttonLabel: string = "Open Full Editor";
  export let accent: string = "var(--bk-tax-processing)";

  const dispatch = createEventDispatcher();

  /** Enough to recognise a query by, short enough to leave the panel usable. */
  const PREVIEW_LINES = 6;

  $: lines = (value || "").split("\n");
  $: preview =
    lines.slice(0, PREVIEW_LINES).join("\n") + (lines.length > PREVIEW_LINES ? "\n..." : "");
  $: filled = (value || "").trim().length > 0;
</script>

<div class="field-group" style="--field-accent: {accent}">
  <span class="group-title">{title}</span>
  <button
    class="btn-open-editor"
    on:click={() => dispatch("open")}
    title="Open the {title.toLowerCase()} editor"
  >
    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" aria-hidden="true">
      <path
        d={icons.code.d}
        stroke="currentColor"
        stroke-width="1.8"
        stroke-linecap="round"
        stroke-linejoin="round"
      />
    </svg>
    {buttonLabel}
  </button>
  {#if filled}
    <pre class="code-preview">{preview}</pre>
  {:else}
    <div class="code-empty">{emptyLabel}</div>
  {/if}
</div>

<style>
  .field-group {
    padding: var(--space-sm) var(--space-lg);
    border-top: 1px solid var(--border);
    margin-top: var(--space-sm);
  }
  .group-title {
    display: block;
    font-size: 0.625rem;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.08em;
    font-weight: 600;
    margin-bottom: var(--space-sm);
  }

  .btn-open-editor {
    display: flex;
    align-items: center;
    gap: 6px;
    width: 100%;
    padding: 8px 12px;
    border-radius: 6px;
    font-size: 12px;
    font-weight: 500;
    /* color-mix keeps one accent variable driving fill, border and text,
       so a node's colour arrives as a single value from dag.ts. */
    background: color-mix(in srgb, var(--field-accent) 8%, transparent);
    border: 1px solid color-mix(in srgb, var(--field-accent) 24%, transparent);
    color: var(--field-accent);
    transition:
      background 150ms ease,
      border-color 150ms ease;
    margin-bottom: 8px;
  }
  .btn-open-editor:hover {
    background: color-mix(in srgb, var(--field-accent) 14%, transparent);
    border-color: color-mix(in srgb, var(--field-accent) 45%, transparent);
  }
  .btn-open-editor:focus-visible {
    outline: 2px solid var(--field-accent);
    outline-offset: 2px;
  }

  .code-preview {
    font-family: var(--font-mono);
    font-size: 10px;
    line-height: 1.5;
    color: var(--text-dim);
    background: var(--bg-code-line);
    border: 1px solid var(--border-sidebar);
    border-radius: 6px;
    padding: 8px 10px;
    margin: 0;
    /* A long line scrolls in its own box rather than widening the panel. */
    overflow-x: auto;
    overflow-y: hidden;
    white-space: pre;
    max-height: 100px;
  }
  .code-empty {
    font-size: 11px;
    color: var(--text-ghost);
    padding: 12px;
    text-align: center;
    background: var(--bg-code-line);
    border: 1px dashed var(--border-sidebar);
    border-radius: 6px;
  }

  @media (prefers-reduced-motion: reduce) {
    .btn-open-editor {
      transition: none;
    }
  }
</style>
