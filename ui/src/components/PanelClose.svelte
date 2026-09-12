<script lang="ts">
  import { icons } from "../lib/icons";

  /** Accessible name, in case a panel needs something more specific. */
  export let label: string = "Close";
  /** Nudges the control to the panel's top-right corner when absolute. */
  export let floating: boolean = false;
</script>

<!--
  One close control, used by every panel the editor can open.

  It exists as a component rather than a CSS class because the three
  panels had drifted into three different affordances: two text links
  reading "Close" and, in the YAML view, one sitting to the left of the
  format tabs where nothing else lives. An X in the top-right corner is
  the shape people already know, and sharing it means the next panel
  cannot invent a fourth.
-->
<button
  class="panel-close"
  class:floating
  type="button"
  aria-label={label}
  title="{label} (Esc)"
  on:click
>
  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" aria-hidden="true">
    <path
      d={icons.close.d}
      stroke="currentColor"
      stroke-width="2"
      stroke-linecap="round"
      stroke-linejoin="round"
    />
  </svg>
</button>

<style>
  .panel-close {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 26px;
    height: 26px;
    padding: 0;
    border: 1px solid transparent;
    border-radius: 6px;
    background: transparent;
    color: var(--text-dim);
    cursor: pointer;
    transition:
      background 150ms ease,
      color 150ms ease,
      border-color 150ms ease;
  }

  .panel-close.floating {
    position: absolute;
    top: 10px;
    right: 10px;
    z-index: 2;
  }

  .panel-close:hover {
    background: var(--border-subtle);
    color: var(--text-primary);
  }

  /* Keyboard focus is drawn, not borrowed from the hover state: a
     keyboard user needs to see where they are without a pointer. */
  .panel-close:focus-visible {
    outline: none;
    border-color: var(--accent-text, #4ade80);
    color: var(--text-primary);
  }

  .panel-close:active {
    transform: scale(0.94);
  }

  @media (prefers-reduced-motion: reduce) {
    .panel-close {
      transition: none;
    }
    .panel-close:active {
      transform: none;
    }
  }
</style>
