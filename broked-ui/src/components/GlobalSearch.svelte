<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { icons } from "../lib/icons";
  import { authHeaders } from "../lib/auth";

  let visible = false;
  let query = "";
  let results: SearchResult[] = [];
  let selectedIndex = 0;
  let loading = false;
  let inputEl: HTMLInputElement;
  let searchTimer: ReturnType<typeof setTimeout>;

  interface SearchResult {
    type: "pipeline" | "connection" | "variable" | "page";
    id: string;
    name: string;
    description: string;
    icon: string;
    href: string;
    meta?: string;
  }

  // Static pages for navigation
  const pages: SearchResult[] = [
    { type: "page", id: "dash", name: "Dashboard", description: "Overview and metrics", icon: icons.dashboard.d, href: "#/" },
    { type: "page", id: "pipe", name: "Pipelines", description: "Manage pipelines", icon: icons.pipeline.d, href: "#/pipelines" },
    { type: "page", id: "cal", name: "Calendar", description: "Run activity heatmap", icon: icons.calendar.d, href: "#/calendar" },
    { type: "page", id: "lin", name: "Lineage", description: "Data lineage graph", icon: icons.lineage.d, href: "#/lineage" },
    { type: "page", id: "var", name: "Variables", description: "Manage variables", icon: icons.variable.d, href: "#/variables" },
    { type: "page", id: "conn", name: "Connections", description: "Manage connections", icon: icons.connection.d, href: "#/connections" },
    { type: "page", id: "set", name: "Settings", description: "System configuration", icon: icons.settings.d, href: "#/settings" },
  ];

  // Cache fetched data
  let pipelines: any[] = [];
  let connections: any[] = [];
  let variables: any[] = [];
  let dataLoaded = false;

  async function loadData() {
    if (dataLoaded) return;
    loading = true;
    try {
      const headers = authHeaders();
      const [pRes, cRes, vRes] = await Promise.all([
        fetch("/api/pipelines", { headers }),
        fetch("/api/connections", { headers }),
        fetch("/api/variables", { headers }),
      ]);
      if (pRes.ok) pipelines = await pRes.json();
      if (cRes.ok) connections = await cRes.json();
      if (vRes.ok) variables = await vRes.json();
      dataLoaded = true;
    } catch {}
    loading = false;
  }

  function search(q: string) {
    if (!q.trim()) {
      results = pages;
      return;
    }
    const s = q.toLowerCase();
    const matched: SearchResult[] = [];

    // Pipelines
    for (const p of pipelines) {
      if (p.name?.toLowerCase().includes(s) || p.description?.toLowerCase().includes(s) || (p.tags || []).some((t: string) => t.toLowerCase().includes(s))) {
        matched.push({
          type: "pipeline",
          id: p.id,
          name: p.name,
          description: p.description || "",
          icon: icons.pipeline.d,
          href: `#/pipelines/${p.id}/edit`,
          meta: p.schedule || "manual",
        });
      }
    }

    // Connections
    for (const c of connections) {
      if (c.conn_id?.toLowerCase().includes(s) || c.type?.toLowerCase().includes(s) || c.description?.toLowerCase().includes(s)) {
        matched.push({
          type: "connection",
          id: c.id || c.conn_id,
          name: c.conn_id,
          description: `${c.type} — ${c.description || c.host || ""}`,
          icon: icons.connection.d,
          href: "#/connections",
          meta: c.type,
        });
      }
    }

    // Variables
    for (const v of variables) {
      if (v.key?.toLowerCase().includes(s) || v.description?.toLowerCase().includes(s)) {
        matched.push({
          type: "variable",
          id: v.key,
          name: v.key,
          description: v.description || v.type,
          icon: icons.variable.d,
          href: "#/variables",
          meta: v.type,
        });
      }
    }

    // Pages
    for (const p of pages) {
      if (p.name.toLowerCase().includes(s)) {
        matched.push(p);
      }
    }

    results = matched.slice(0, 20);
    selectedIndex = 0;
  }

  $: {
    clearTimeout(searchTimer);
    if (query.trim()) {
      searchTimer = setTimeout(() => search(query), 200);
    } else {
      results = pages;
      selectedIndex = 0;
    }
  }

  function open() {
    visible = true;
    query = "";
    results = pages;
    selectedIndex = 0;
    loadData();
    setTimeout(() => inputEl?.focus(), 50);
  }

  function close() {
    visible = false;
    query = "";
  }

  function navigate(result: SearchResult) {
    window.location.hash = result.href.replace("#", "");
    close();
  }

  function handleKeydown(e: KeyboardEvent) {
    if ((e.metaKey || e.ctrlKey) && e.key === "k") {
      e.preventDefault();
      if (visible) close(); else open();
      return;
    }
    if (!visible) return;

    if (e.key === "Escape") { close(); return; }
    if (e.key === "ArrowDown") { e.preventDefault(); selectedIndex = Math.min(selectedIndex + 1, results.length - 1); return; }
    if (e.key === "ArrowUp") { e.preventDefault(); selectedIndex = Math.max(selectedIndex - 1, 0); return; }
    if (e.key === "Enter" && results[selectedIndex]) { navigate(results[selectedIndex]); return; }
  }

  function typeColor(type: string): string {
    switch (type) {
      case "pipeline": return "var(--accent)";
      case "connection": return "#22c55e";
      case "variable": return "#f59e0b";
      case "page": return "var(--text-muted)";
      default: return "var(--text-muted)";
    }
  }
</script>

<svelte:window on:keydown={handleKeydown} />

{#if visible}
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="search-overlay" on:click={close} on:keydown={() => {}}>
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <div class="search-modal" on:click|stopPropagation on:keydown={() => {}}>
      <div class="search-input-wrap">
        <svg class="search-icon" width="18" height="18" viewBox="0 0 24 24" fill="none">
          <path d={icons.search.d} stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
        </svg>
        <input
          bind:this={inputEl}
          bind:value={query}
          class="search-input"
          placeholder="Search pipelines, connections, variables, pages..."
          spellcheck="false"
        />
        <kbd class="search-kbd">ESC</kbd>
      </div>

      <div class="search-results">
        {#if loading}
          <div class="search-empty">Loading...</div>
        {:else if results.length === 0}
          <div class="search-empty">No results for "{query}"</div>
        {:else}
          {#each results as result, i}
            <!-- svelte-ignore a11y_no_static_element_interactions -->
            <div
              class="search-result"
              class:selected={i === selectedIndex}
              on:click={() => navigate(result)}
              on:mouseenter={() => selectedIndex = i}
              on:keydown={() => {}}
            >
              <svg class="result-icon" width="16" height="16" viewBox="0 0 24 24" fill="none" style="color: {typeColor(result.type)}">
                <path d={result.icon} stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
              </svg>
              <div class="result-content">
                <span class="result-name">{result.name}</span>
                {#if result.description}
                  <span class="result-desc">{result.description.slice(0, 60)}</span>
                {/if}
              </div>
              <div class="result-meta">
                {#if result.meta}
                  <span class="result-meta-text">{result.meta}</span>
                {/if}
                <span class="result-type" style="color: {typeColor(result.type)}">{result.type}</span>
              </div>
            </div>
          {/each}
        {/if}
      </div>

      <div class="search-footer">
        <span><kbd>↑↓</kbd> navigate</span>
        <span><kbd>↵</kbd> open</span>
        <span><kbd>esc</kbd> close</span>
      </div>
    </div>
  </div>
{/if}

<style>
  .search-overlay {
    position: fixed; inset: 0;
    background: rgba(0,0,0,0.5);
    z-index: 9999;
    display: flex; justify-content: center;
    padding-top: 15vh;
  }
  .search-modal {
    width: 560px; max-width: 95vw;
    max-height: 480px;
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    border-radius: 12px;
    overflow: hidden;
    display: flex; flex-direction: column;
    box-shadow: 0 20px 60px rgba(0,0,0,0.4);
  }

  .search-input-wrap {
    display: flex; align-items: center; gap: 10px;
    padding: 14px 18px;
    border-bottom: 1px solid var(--border);
  }
  .search-icon { color: var(--text-ghost); flex-shrink: 0; }
  .search-input {
    flex: 1; border: none; background: none;
    font-size: 15px; color: var(--text-primary);
    outline: none; font-family: var(--font-ui);
  }
  .search-input::placeholder { color: var(--text-ghost); }
  .search-kbd {
    font-family: var(--font-mono); font-size: 10px; font-weight: 600;
    padding: 2px 6px; border-radius: 4px;
    background: var(--bg-tertiary); color: var(--text-ghost);
    border: 1px solid var(--border-subtle);
  }

  .search-results {
    flex: 1; overflow-y: auto;
    padding: 6px;
  }
  .search-empty {
    padding: 24px; text-align: center;
    color: var(--text-dim); font-size: 13px;
  }
  .search-result {
    display: flex; align-items: center; gap: 10px;
    padding: 8px 12px; border-radius: 8px;
    cursor: pointer; transition: background 80ms ease;
  }
  .search-result.selected { background: var(--bg-tertiary); }
  .result-icon { flex-shrink: 0; opacity: 0.7; }
  .result-content { flex: 1; min-width: 0; }
  .result-name {
    display: block; font-size: 13px; font-weight: 500;
    color: var(--text-primary);
    overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  }
  .result-desc {
    display: block; font-size: 11px; color: var(--text-dim);
    overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  }
  .result-meta {
    display: flex; align-items: center; gap: 8px; flex-shrink: 0;
  }
  .result-meta-text {
    font-size: 10px; color: var(--text-ghost); font-family: var(--font-mono);
  }
  .result-type {
    font-size: 9px; font-weight: 600; text-transform: uppercase;
    letter-spacing: 0.06em;
  }

  .search-footer {
    display: flex; gap: 16px; padding: 8px 18px;
    border-top: 1px solid var(--border);
    font-size: 11px; color: var(--text-ghost);
  }
  .search-footer kbd {
    font-family: var(--font-mono); font-size: 10px;
    padding: 1px 4px; border-radius: 3px;
    background: var(--bg-tertiary); border: 1px solid var(--border-subtle);
    margin-right: 3px;
  }
</style>
