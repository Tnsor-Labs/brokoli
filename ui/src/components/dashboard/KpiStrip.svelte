<script lang="ts">
  export let failed = 0;
  export let running = 0;
  export let successRate = 100;
  export let runsToday = 0;
  export let runsYesterday = 0;
  export let totalPipelines = 0;
  export let activePipelines = 0;

  // Ordered by urgency, not alphabetically: the number you need when
  // something is wrong comes first. The previous layout gave "Pipelines: 5"
  // the same visual weight as "Failed: 1".
  //
  // A metric only takes colour when its value actually warrants attention —
  // otherwise the strip stays neutral and a real problem stands out. All
  // colours come from theme variables, so light theme renders correctly
  // (the previous hardcoded hex values did not — brokoli#76).
  $: successTone =
    successRate >= 95 ? "" : successRate >= 80 ? "warn" : "fail";

  $: delta = runsToday - runsYesterday;
</script>

<!--
  Stat tiles, not a text strip. The values used to sit inline with their
  labels at 20px, which put every element on this page inside the same
  11-20px band — so the eye had no landmark and the screen read as uniform.

  The number is now the largest thing on the page by a wide margin, with the
  label above it as a caption. That is the one idea worth borrowing from
  metric dashboards like Grafana: size carries importance.

  What is deliberately NOT borrowed is filling every tile with colour.
  A tile only tints when its value warrants attention, so on a healthy
  system the band is monochrome and a single red tile is unmissable. Tint
  everything and you are back to uniform, just louder.
-->
<div class="strip">
  <div class="tile" class:fail={failed > 0}>
    <span class="label">Failed 24h</span>
    <span class="value">{failed}</span>
    <span class="foot">
      {#if failed > 0}needs attention{:else}none{/if}
    </span>
  </div>

  <div class="tile" class:running={running > 0}>
    <span class="label">Running</span>
    <span class="value">{running}</span>
    <span class="foot">{running > 0 ? "in flight" : "idle"}</span>
  </div>

  <div class="tile {successTone}">
    <span class="label">Success 24h</span>
    <span class="value">{successRate}<span class="unit">%</span></span>
    <!-- A bar, not another numeral. Shape variety is what lets you find a
         panel without reading it. -->
    <span class="foot">
      <span class="meter">
        <span class="meter-fill" style="width: {Math.max(0, Math.min(100, successRate))}%"></span>
      </span>
    </span>
  </div>

  <div class="tile">
    <span class="label">Runs today</span>
    <span class="value">{runsToday}</span>
    <span class="foot">
      {#if delta > 0}
        <span class="up">▲ {delta}</span> vs yest.
      {:else if delta < 0}
        <span class="down">▼ {Math.abs(delta)}</span> vs yest.
      {:else}
        same as yest.
      {/if}
    </span>
  </div>

  <div class="tile">
    <span class="label">Pipelines</span>
    <span class="value">{totalPipelines}</span>
    <span class="foot">{activePipelines} active</span>
  </div>

  <div class="tile tile-spark">
    <span class="label">Last 7 days</span>
    <slot />
  </div>
</div>

<style>
  .strip {
    display: flex;
    align-items: stretch;
    gap: 0;
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius-lg);
    background: var(--bg-secondary);
    box-shadow: var(--shadow-card);
    overflow: hidden;
  }

  .tile {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding: 10px 14px 11px;
    border-right: 1px solid var(--border-subtle);
    /* Reserved so a tile that takes a tone doesn't shift its neighbours. */
    border-top: 2px solid transparent;
  }
  .tile:last-child { border-right: none; }

  /* Tone: a tinted field plus a top rule. Both are derived from the status
     token, so this stays correct in either theme. */
  .tile.fail {
    background: var(--failed-bg);
    border-top-color: var(--failed);
  }
  .tile.warn {
    background: var(--warning-bg);
    border-top-color: var(--warning);
  }
  .tile.running {
    background: var(--running-bg);
    border-top-color: var(--running);
  }

  .label {
    font-size: 0.625rem;
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.07em;
    color: var(--text-dim);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .value {
    font-family: var(--font-mono);
    font-size: 2rem;
    font-weight: 700;
    line-height: 1.05;
    letter-spacing: -0.02em;
    color: var(--text-primary);
  }
  .unit {
    font-size: 1rem;
    font-weight: 600;
    color: var(--text-muted);
    margin-left: 1px;
  }

  .tile.fail .value    { color: var(--failed); }
  .tile.warn .value    { color: var(--warning); }
  .tile.running .value { color: var(--running); }

  .foot {
    font-size: 0.625rem;
    color: var(--text-dim);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    min-height: 12px;
  }
  .up   { color: var(--success); font-family: var(--font-mono); }
  .down { color: var(--text-muted); font-family: var(--font-mono); }

  .meter {
    display: block;
    width: 100%;
    max-width: 120px;
    height: 4px;
    margin-top: 4px;
    border-radius: 2px;
    background: var(--bg-tertiary);
    overflow: hidden;
  }
  .meter-fill {
    display: block;
    height: 100%;
    background: var(--success);
  }
  .tile.warn .meter-fill { background: var(--warning); }
  .tile.fail .meter-fill { background: var(--failed); }

  .tile-spark { flex: 0 0 auto; justify-content: flex-start; }

  @media (max-width: 1100px) {
    .strip { flex-wrap: wrap; }
    .tile { flex: 1 1 33%; border-bottom: 1px solid var(--border-subtle); }
    .value { font-size: 1.65rem; }
  }
  @media (max-width: 768px) {
    .tile { flex: 1 1 50%; }
    .value { font-size: 1.5rem; }
    .tile-spark { display: none; }
  }
</style>
