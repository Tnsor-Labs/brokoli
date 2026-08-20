<script lang="ts">
  import BrandIcon from "./BrandIcon.svelte";

  export let brandIcon: string;
  export let kicker: string = "";
  export let title: string;
  export let description: string = "";
  export let actionLabel: string = "";
  export let actionHref: string = "";
  export let actionVariant: "primary" | "secondary" = "primary";
  export let actionDisabled: boolean = false;
</script>

<!--
  Standard page header (v1.5). Replaces what used to be three
  inconsistent treatments across the app — a gradient hero, a
  free-floating title, and several card headers — with one component.
  The KPI rail some pages attach below is intentionally a separate
  concern (the "kpi-rail" slot), not baked into this component: a page
  with metrics doesn't get a bespoke hero, it gets this header plus a
  KPI rail underneath it.
-->
<div class="page-header-card">
  <header class="std-header" class:has-kpi-rail={$$slots["kpi-rail"]}>
    <div class="header-icon" aria-hidden="true">
      <BrandIcon name={brandIcon} size={20} />
    </div>
    <div class="header-text">
      {#if kicker}
        <span class="hk">{kicker}</span>
      {/if}
      <h4>{title}</h4>
      {#if description}
        <p>{description}</p>
      {/if}
    </div>
    <div class="header-actions">
      <slot name="extra-action" />
      <slot>
        {#if actionLabel && actionHref}
          <a href={actionHref} class="ui-btn {actionVariant}">{actionLabel}</a>
        {:else if actionLabel}
          <button type="button" class="ui-btn {actionVariant}" disabled={actionDisabled} on:click
            ><slot name="action">{actionLabel}</slot></button
          >
        {/if}
      </slot>
    </div>
  </header>
  {#if $$slots["kpi-rail"]}
    <div class="kpi-rail-clip">
      <slot name="kpi-rail" />
    </div>
  {/if}
</div>

<style>
  .page-header-card {
    border-radius: var(--radius-2xl);
    border: 1px solid var(--bk-border);
    background: var(--bk-surface-1);
    /* No overflow:hidden here -- it used to clip anything positioned
       absolutely inside the header row (e.g. AlertDrawer's dropdown
       panel, meant to hang below the header), cutting it off instead
       of letting it float over the page. Corner-radius clipping is
       handled per-section below instead: .std-header gets its own
       top radius (it's always the top element), and .kpi-rail-clip
       (below the header, only ever containing static content, never
       an overlay) clips its own bottom corners. */
  }
  .std-header {
    position: relative;
    display: flex;
    align-items: center;
    gap: 12px;
    min-height: 92px;
    padding: 14px 16px;
    background: var(--bk-surface-1);
    border-radius: var(--radius-2xl);
  }
  .std-header.has-kpi-rail {
    border-radius: var(--radius-2xl) var(--radius-2xl) 0 0;
  }
  .kpi-rail-clip {
    border-radius: 0 0 var(--radius-2xl) var(--radius-2xl);
    overflow: hidden;
  }
  .std-header::before {
    content: "";
    position: absolute;
    left: 0;
    top: 18px;
    bottom: 18px;
    width: 2px;
    border-radius: 2px;
    background: var(--bk-brand-signal);
  }
  .header-icon {
    flex-shrink: 0;
    width: 38px;
    height: 38px;
    border-radius: 9px;
    display: flex;
    align-items: center;
    justify-content: center;
    background: var(--bk-brand-soft);
    color: var(--bk-brand-signal);
    border: 1px solid var(--bk-brand-tile-border);
  }
  .header-text {
    min-width: 0;
    flex: 1;
  }
  .hk {
    display: block;
    margin-bottom: 3px;
    color: var(--bk-brand-signal);
    font: 650 9px var(--font-mono);
    letter-spacing: 0.14em;
    text-transform: uppercase;
  }
  h4 {
    margin: 0 0 3px;
    line-height: 1.1;
    font-size: 1.15rem;
    font-weight: 650;
    letter-spacing: -0.02em;
    color: var(--text-primary);
  }
  p {
    margin: 0;
    font-size: 12.5px;
    color: var(--bk-text-tertiary);
    max-width: 52ch;
  }
  .header-actions {
    flex-shrink: 0;
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .ui-btn {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    height: 30px;
    padding: 0 14px;
    border-radius: var(--radius-md);
    font-size: 12.5px;
    font-weight: 650;
    text-decoration: none;
    cursor: pointer;
    transition: opacity 150ms ease;
  }
  .ui-btn:hover {
    opacity: 0.9;
  }
  .ui-btn:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }
  .ui-btn.primary {
    background: var(--bk-action-primary-bg);
    color: var(--bk-action-primary-fg);
    border: none;
  }
  .ui-btn.secondary {
    background: var(--bk-surface-2);
    border: 1px solid var(--bk-border-strong);
    color: var(--text-primary);
  }

  @media (max-width: 720px) {
    .std-header {
      flex-wrap: wrap;
      min-height: 0;
    }
    .header-actions {
      width: 100%;
      justify-content: flex-start;
    }
  }
</style>
