<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import type { Alert } from "../../lib/types";
  import { api } from "../../lib/api";
  import { notify } from "../../lib/toast";
  import { getSodpClient } from "../../lib/sodp";
  import { dashboardKey } from "../../lib/auth";

  let open = false;
  let alerts: Alert[] = [];
  let unread = 0;
  let loading = false;
  let unsub: (() => void) | null = null;
  let reloadTimer: ReturnType<typeof setTimeout> | null = null;

  async function load() {
    loading = true;
    try {
      const res = await api.alerts.list({ limit: 30 });
      alerts = res.alerts ?? [];
      unread = res.unread_count ?? 0;
    } catch {
      // Leave what we have rather than blanking the list on a blip.
    } finally {
      loading = false;
    }
  }

  onMount(() => {
    load();
    // Alerts are created server-side as runs fail, so the same state-change
    // tripwire the dashboard uses tells us to refetch. Debounced, since one
    // run emits several writes.
    const client = getSodpClient();
    let first = true;
    unsub = client.watch(dashboardKey(), () => {
      if (first) { first = false; return; }
      if (reloadTimer) clearTimeout(reloadTimer);
      reloadTimer = setTimeout(() => { load(); reloadTimer = null; }, 400);
    });
  });

  onDestroy(() => {
    unsub?.();
    if (reloadTimer) clearTimeout(reloadTimer);
  });

  async function markRead(a: Alert) {
    if (a.read_at) return;
    try {
      await api.alerts.markRead(a.id);
      alerts = alerts.map(x => (x.id === a.id ? { ...x, read_at: new Date().toISOString() } : x));
      unread = Math.max(0, unread - 1);
    } catch (e: any) {
      notify.error(e?.message || "Failed to mark read");
    }
  }

  async function markAllRead() {
    try {
      await api.alerts.markAllRead();
      const now = new Date().toISOString();
      alerts = alerts.map(x => ({ ...x, read_at: x.read_at ?? now }));
      unread = 0;
    } catch (e: any) {
      notify.error(e?.message || "Failed to mark all read");
    }
  }

  async function dismiss(a: Alert) {
    try {
      await api.alerts.dismiss(a.id);
      if (!a.read_at) unread = Math.max(0, unread - 1);
      alerts = alerts.filter(x => x.id !== a.id);
    } catch (e: any) {
      notify.error(e?.message || "Failed to dismiss");
    }
  }

  function timeAgo(dateStr: string): string {
    const s = Math.floor((Date.now() - new Date(dateStr).getTime()) / 1000);
    if (s < 60) return `${s}s ago`;
    if (s < 3600) return `${Math.floor(s / 60)}m ago`;
    if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
    return `${Math.floor(s / 86400)}d ago`;
  }

  function onKeydown(e: KeyboardEvent) {
    if (e.key === "Escape" && open) open = false;
  }
</script>

<svelte:window on:keydown={onKeydown} />

<div class="wrap">
  <button
    class="bell"
    class:has-unread={unread > 0}
    on:click={() => { open = !open; if (open) load(); }}
    aria-label="Alerts{unread > 0 ? ` (${unread} unread)` : ''}"
    title="Alerts"
  >
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8">
      <path d="M18 8A6 6 0 0 0 6 8c0 7-3 9-3 9h18s-3-2-3-9" />
      <path d="M13.73 21a2 2 0 0 1-3.46 0" />
    </svg>
    {#if unread > 0}
      <span class="badge">{unread > 99 ? "99+" : unread}</span>
    {/if}
  </button>

  {#if open}
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <div class="backdrop" on:click={() => (open = false)} on:keydown={() => {}}></div>
    <div class="panel">
      <div class="panel-head">
        <span class="panel-title">Alerts</span>
        <div class="panel-actions">
          {#if unread > 0}
            <button class="link" on:click={markAllRead}>Mark all read</button>
          {/if}
          <button class="close" on:click={() => (open = false)} aria-label="Close">
            <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
              <path d="M18 6L6 18M6 6l12 12" />
            </svg>
          </button>
        </div>
      </div>

      <div class="panel-body">
        {#if loading && alerts.length === 0}
          <div class="state">Loading…</div>
        {:else if alerts.length === 0}
          <div class="state">
            <span class="state-title">Nothing to report</span>
            <span class="state-sub">Failures and other alerts show up here.</span>
          </div>
        {:else}
          {#each alerts as a (a.id)}
            <div class="alert" class:unread={!a.read_at} class:critical={a.severity === "critical"}>
              <span class="sev" class:critical={a.severity === "critical"} class:warning={a.severity === "warning"}></span>
              <div class="alert-main">
                <div class="alert-top">
                  <span class="alert-title">{a.title}</span>
                  <span class="alert-time">{timeAgo(a.created_at)}</span>
                </div>
                {#if a.body}
                  <p class="alert-body" title={a.body}>{a.body.split("\n")[0]}</p>
                {/if}
                <div class="alert-links">
                  {#if a.pipeline_id}
                    <a href="#/pipelines/{a.pipeline_id}/runs" on:click={() => (open = false)}>View runs</a>
                  {/if}
                  {#if !a.read_at}
                    <button class="link" on:click={() => markRead(a)}>Mark read</button>
                  {/if}
                  <button class="link" on:click={() => dismiss(a)}>Dismiss</button>
                </div>
              </div>
            </div>
          {/each}
        {/if}
      </div>
    </div>
  {/if}
</div>

<style>
  .wrap { position: relative; }

  .bell {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 30px;
    height: 30px;
    border-radius: var(--radius-md);
    border: 1px solid transparent;
    background: transparent;
    color: var(--text-muted);
    cursor: pointer;
    position: relative;
    transition: color var(--transition-fast), background var(--transition-fast);
  }
  .bell:hover { color: var(--text-primary); background: var(--bg-tertiary); }
  .bell.has-unread { color: var(--text-secondary); }

  .badge {
    position: absolute;
    top: -2px;
    right: -2px;
    min-width: 15px;
    height: 15px;
    padding: 0 3px;
    border-radius: 8px;
    background: var(--failed);
    color: #fff;
    font-family: var(--font-mono);
    font-size: 0.5625rem;
    font-weight: 700;
    line-height: 15px;
    text-align: center;
  }

  .backdrop { position: fixed; inset: 0; z-index: 40; }

  .panel {
    position: absolute;
    top: calc(100% + 8px);
    right: 0;
    width: 380px;
    max-width: calc(100vw - 32px);
    max-height: 70vh;
    display: flex;
    flex-direction: column;
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    box-shadow: var(--shadow-lg);
    z-index: 50;
    overflow: hidden;
  }

  .panel-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 9px 12px;
    border-bottom: 1px solid var(--border-subtle);
    background: var(--bg-tertiary);
  }

  .panel-title {
    font-size: 0.6875rem;
    font-weight: 700;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: var(--text-muted);
  }

  .panel-actions { display: flex; align-items: center; gap: var(--space-sm); }

  .close {
    display: flex;
    background: none;
    border: none;
    color: var(--text-muted);
    cursor: pointer;
    padding: 2px;
  }
  .close:hover { color: var(--text-primary); }

  .panel-body { overflow-y: auto; }

  .alert {
    display: flex;
    gap: var(--space-sm);
    padding: 9px 12px;
    border-bottom: 1px solid var(--border-subtle);
  }
  .alert:last-child { border-bottom: none; }
  .alert.unread { background: var(--accent-glow); }

  .sev {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    margin-top: 5px;
    background: var(--text-dim);
    flex-shrink: 0;
  }
  .sev.critical { background: var(--failed); }
  .sev.warning  { background: var(--warning); }

  .alert-main { min-width: 0; flex: 1; }

  .alert-top {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: var(--space-sm);
  }

  .alert-title {
    font-size: 0.75rem;
    font-weight: 600;
    color: var(--text-primary);
  }

  .alert-time {
    font-family: var(--font-mono);
    font-size: 0.625rem;
    color: var(--text-dim);
    white-space: nowrap;
  }

  .alert-body {
    margin: 2px 0 0;
    font-family: var(--font-mono);
    font-size: 0.625rem;
    color: var(--text-muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .alert-links {
    display: flex;
    gap: var(--space-sm);
    margin-top: 5px;
  }

  .link, .alert-links a {
    font-size: 0.625rem;
    color: var(--text-muted);
    background: none;
    border: none;
    padding: 0;
    cursor: pointer;
    text-decoration: none;
  }
  .link:hover, .alert-links a:hover { color: var(--accent); }

  .state {
    display: flex;
    flex-direction: column;
    gap: 3px;
    padding: var(--space-lg);
    text-align: center;
  }
  .state-title { font-size: 0.8125rem; color: var(--text-secondary); }
  .state-sub   { font-size: 0.6875rem; color: var(--text-dim); }
</style>
