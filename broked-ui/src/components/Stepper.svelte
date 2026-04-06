<script lang="ts">
  import { createEventDispatcher } from "svelte";
  export let value: number = 1;
  export let min: number = 1;
  export let max: number = 999;
  export let step: number = 1;
  export let label: string = "";

  const dispatch = createEventDispatcher();

  function decrement() {
    if (value - step >= min) { value -= step; dispatch("change", value); }
  }
  function increment() {
    if (value + step <= max) { value += step; dispatch("change", value); }
  }
</script>

<div class="stepper-wrap">
  {#if label}<span class="stepper-label">{label}</span>{/if}
  <div class="stepper">
    <button class="stepper-btn" on:click={decrement} disabled={value <= min}>-</button>
    <span class="stepper-value">{value}</span>
    <button class="stepper-btn" on:click={increment} disabled={value >= max}>+</button>
  </div>
</div>

<style>
  .stepper-wrap { display: flex; flex-direction: column; gap: 4px; }
  .stepper-label {
    font-size: 11px; font-weight: 500; color: var(--text-muted);
    text-transform: uppercase; letter-spacing: 0.06em;
  }
  .stepper {
    display: flex; align-items: center;
    border: 1px solid var(--border); border-radius: var(--radius-md);
    overflow: hidden; height: 38px;
  }
  .stepper-btn {
    width: 38px; height: 100%; display: flex; align-items: center; justify-content: center;
    font-size: 16px; font-weight: 600; color: var(--text-secondary);
    background: var(--bg-tertiary); border: none; cursor: pointer;
    transition: all 150ms ease; flex-shrink: 0;
  }
  .stepper-btn:hover:not(:disabled) { background: var(--accent-glow); color: var(--accent); }
  .stepper-btn:disabled { opacity: 0.25; cursor: default; }
  .stepper-value {
    flex: 1; text-align: center; font-family: var(--font-mono);
    font-size: 14px; font-weight: 600; color: var(--text-primary);
    background: var(--bg-secondary); min-width: 40px;
  }
</style>
