<script lang="ts">
  import { onMount } from "svelte";
  import { api } from "../lib/api";
  import type { Plugin, PluginIndexEntry } from "../lib/types";
  import { notify } from "../lib/toast";
  import { icons } from "../lib/icons";
  import ConfirmDialog from "../components/ConfirmDialog.svelte";
  import EmptyState from "../components/EmptyState.svelte";
  import Skeleton from "../components/Skeleton.svelte";

  let plugins: Plugin[] = [];
  let loading = true;
  let installing = false;
  let fileInput: HTMLInputElement;

  // Curated index (browse + install-by-name)
  let indexEntries: PluginIndexEntry[] = [];
  let indexLoading = true;
  let indexAvailable = false;
  let installingName = "";

  // Delete
  let confirmDelete = false;
  let deleteTarget = "";

  $: installedNames = new Set(plugins.map((p) => p.name));

  onMount(async () => {
    await load();
    await loadIndex();
  });

  async function loadIndex() {
    indexLoading = true;
    try {
      const idx = await api.plugins.index();
      indexEntries = idx.plugins || [];
      indexAvailable = true;
    } catch {
      // 502 (index unreachable/unconfigured) or 503 (no plugin support):
      // there's simply no catalog to browse. Not an error the user acts on.
      indexEntries = [];
      indexAvailable = false;
    } finally {
      indexLoading = false;
    }
  }

  async function installFromIndex(name: string) {
    installingName = name;
    try {
      const installed = await api.plugins.installByName(name);
      notify.success(`Installed ${installed.name} ${installed.version}`);
      await load();
    } catch (e: any) {
      // Named reason from the server: digest mismatch, wrong platform,
      // missing runtime.
      notify.error(e?.message || "Install failed");
    } finally {
      installingName = "";
    }
  }

  async function load() {
    loading = true;
    try {
      plugins = await api.plugins.list();
    } catch (e: any) {
      // A server without plugin support returns 503 — an empty list is the
      // honest view, not an error the user needs to act on.
      if (e?.status === 503) {
        plugins = [];
      } else {
        notify.error(e?.message || "Failed to load plugins");
      }
    } finally {
      loading = false;
    }
  }

  function pickFile() {
    fileInput?.click();
  }

  async function onFileChosen(e: Event) {
    const input = e.currentTarget as HTMLInputElement;
    const file = input.files?.[0];
    input.value = ""; // allow re-selecting the same file later
    if (!file) return;
    installing = true;
    try {
      const installed = await api.plugins.install(file);
      notify.success(`Installed ${installed.name} ${installed.version}`);
      await load();
    } catch (e: any) {
      // The server names the reason: wrong platform, missing runtime,
      // tampered archive, drift. Surface it verbatim.
      notify.error(e?.message || "Install failed");
    } finally {
      installing = false;
    }
  }

  async function removePlugin(name: string) {
    try {
      await api.plugins.remove(name);
      notify.success(`Removed ${name}`);
      await load();
    } catch (e: any) {
      notify.error(e?.message || "Remove failed");
    }
  }

  function nodeTypeLabel(p: Plugin): string {
    if (!p.node_types?.length) return "—";
    return p.node_types.map((n) => n.display_name || n.type).join(", ");
  }
</script>

<div class="plugins-page animate-in">
  <header class="page-header">
    <h1>Plugins</h1>
    <button class="btn-primary" on:click={pickFile} disabled={installing}>
      {installing ? "Installing…" : "+ Install Plugin"}
    </button>
    <input
      bind:this={fileInput}
      type="file"
      accept=".bkg"
      class="hidden-file"
      on:change={onFileChosen}
    />
  </header>

  {#if loading}
    <div class="skeleton-rows">
      {#each Array(3) as _}
        <Skeleton height="48px" width="100%" />
      {/each}
    </div>
  {:else if plugins.length === 0}
    <EmptyState
      icon={icons.plugin.d}
      title="No plugins installed"
      description="Plugins add node types your pipelines can use. Install a signed .bkg package to add source, transform, or sink nodes."
      ctaLabel="+ Install Plugin"
      on:click={pickFile}
    />
  {:else}
    <div class="table">
      <div class="table-header">
        <span class="col-name">Name</span>
        <span class="col-version">Version</span>
        <span class="col-nodes">Node types</span>
        <span class="col-source">Source</span>
        <span class="col-actions">Actions</span>
      </div>
      {#each plugins as p}
        <div class="table-row">
          <span class="col-name">
            <code class="plugin-name-badge">{p.name}</code>
            {#if p.description}<span class="plugin-desc">{p.description}</span>{/if}
          </span>
          <span class="col-version mono">{p.version || "—"}</span>
          <span class="col-nodes">{nodeTypeLabel(p)}</span>
          <span class="col-source">
            {#if p.packaged}
              <span class="type-badge" title={p.archive_sha256 || ""}>packaged</span>
            {:else}
              <span class="type-badge muted">directory</span>
            {/if}
          </span>
          <span class="col-actions">
            <button
              class="btn-icon danger"
              title="Remove"
              on:click={() => { deleteTarget = p.name; confirmDelete = true; }}
            >
              <svg width="12" height="12" viewBox="0 0 24 24" fill="none"><path d="M3 6h18M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2M10 11v6M14 11v6" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" /></svg>
            </button>
          </span>
        </div>
      {/each}
    </div>
  {/if}

  {#if !indexLoading && indexAvailable && indexEntries.length > 0}
    <section class="index-section">
      <h2 class="section-title">Available in the index</h2>
      <div class="table index-table">
        <div class="table-header">
          <span class="col-name">Name</span>
          <span class="col-version">Version</span>
          <span class="col-idx-desc">Description</span>
          <span class="col-actions">Actions</span>
        </div>
        {#each indexEntries as entry}
          <div class="table-row">
            <span class="col-name"><code class="plugin-name-badge">{entry.name}</code></span>
            <span class="col-version mono">{entry.version || "—"}</span>
            <span class="col-idx-desc">{entry.description || "—"}</span>
            <span class="col-actions">
              {#if installedNames.has(entry.name)}
                <span class="type-badge muted">installed</span>
              {:else}
                <button
                  class="btn-secondary"
                  disabled={installingName === entry.name}
                  on:click={() => installFromIndex(entry.name)}
                >
                  {installingName === entry.name ? "Installing…" : "Install"}
                </button>
              {/if}
            </span>
          </div>
        {/each}
      </div>
    </section>
  {/if}
</div>

<ConfirmDialog
  bind:visible={confirmDelete}
  title="Remove Plugin"
  message="Remove this plugin? Pipelines using its node types will fail to run until it is reinstalled."
  confirmLabel="Remove"
  destructive={true}
  on:confirm={() => removePlugin(deleteTarget)}
/>

<style>
  .plugins-page {
    padding: 1.5rem 2rem;
    max-width: 1100px;
    margin: 0 auto;
  }
  .page-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 1.5rem;
  }
  .page-header h1 {
    font-size: 1.5rem;
    font-weight: 600;
  }
  .hidden-file {
    display: none;
  }
  .skeleton-rows {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }
  .table {
    border: 1px solid var(--border, #2a2a2a);
    border-radius: 8px;
    overflow: hidden;
  }
  .table-header,
  .table-row {
    display: grid;
    grid-template-columns: 2fr 1fr 2fr 1fr 80px;
    align-items: center;
    gap: 1rem;
    padding: 0.75rem 1rem;
  }
  .table-header {
    font-size: 0.75rem;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--text-muted, #888);
    border-bottom: 1px solid var(--border, #2a2a2a);
  }
  .table-row {
    border-bottom: 1px solid var(--border, #2a2a2a);
  }
  .table-row:last-child {
    border-bottom: none;
  }
  .col-name {
    display: flex;
    flex-direction: column;
    gap: 0.2rem;
  }
  .plugin-name-badge {
    font-family: var(--font-mono, monospace);
    font-size: 0.85rem;
  }
  .plugin-desc {
    font-size: 0.75rem;
    color: var(--text-muted, #888);
  }
  .mono {
    font-family: var(--font-mono, monospace);
  }
  .type-badge {
    display: inline-block;
    padding: 0.15rem 0.5rem;
    border-radius: 4px;
    font-size: 0.75rem;
    background: var(--accent-subtle, rgba(34, 197, 94, 0.15));
    color: var(--accent, #22c55e);
  }
  .type-badge.muted {
    background: var(--surface-2, rgba(255, 255, 255, 0.06));
    color: var(--text-muted, #888);
  }
  .col-actions {
    display: flex;
    justify-content: flex-end;
    align-items: center;
  }

  /* Index browse section: its own 4-column grid. */
  .index-section {
    margin-top: 2rem;
  }
  .section-title {
    font-size: 1.05rem;
    font-weight: 600;
    margin-bottom: 0.75rem;
  }
  .index-table .table-header,
  .index-table .table-row {
    grid-template-columns: 2fr 1fr 3fr 100px;
  }
  .col-idx-desc {
    color: var(--text-muted, #888);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .btn-secondary {
    padding: 0.3rem 0.8rem;
    border-radius: 6px;
    border: 1px solid var(--border, #2a2a2a);
    background: var(--surface-2, rgba(255, 255, 255, 0.06));
    color: var(--text, inherit);
    font-size: 0.8rem;
    cursor: pointer;
  }
  .btn-secondary:hover:not(:disabled) {
    background: var(--accent-subtle, rgba(34, 197, 94, 0.15));
    border-color: var(--accent, #22c55e);
  }
  .btn-secondary:disabled {
    opacity: 0.6;
    cursor: default;
  }
</style>
