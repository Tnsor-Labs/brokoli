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
    <div class="header-copy">
      <span class="eyebrow">Extension catalog</span>
      <h1>Plugins</h1>
      <p>Install and manage signed node packages for your pipelines.</p>
    </div>
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

  <section class="inventory" aria-label="Installed plugin inventory">
    <div class="inventory-heading">
      <div>
        <h2>Installed plugins</h2>
        <span>Packages available to pipeline nodes</span>
      </div>
      <strong>{plugins.length}</strong>
    </div>
    {#if loading}
      <div class="skeleton-rows">
        {#each Array(3) as _}
          <Skeleton height="48px" width="100%" />
        {/each}
      </div>
    {:else if plugins.length === 0}
      <div class="empty-wrap">
        <EmptyState
          icon={icons.plugin.d}
          title="No plugins installed"
          description="Plugins add node types your pipelines can use. Install a signed .bkg package to add source, transform, or sink nodes."
          ctaLabel="+ Install Plugin"
          on:click={pickFile}
        />
      </div>
    {:else}
      <div class="table-scroll">
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
                  aria-label="Remove {p.name}"
                  on:click={() => {
                    deleteTarget = p.name;
                    confirmDelete = true;
                  }}
                >
                  <svg width="12" height="12" viewBox="0 0 24 24" fill="none"
                    ><path
                      d="M3 6h18M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2M10 11v6M14 11v6"
                      stroke="currentColor"
                      stroke-width="1.5"
                      stroke-linecap="round"
                      stroke-linejoin="round"
                    /></svg
                  >
                </button>
              </span>
            </div>
          {/each}
        </div>
      </div>
    {/if}
  </section>

  {#if !indexLoading && indexAvailable && indexEntries.length > 0}
    <section class="inventory index-section" aria-label="Plugin index inventory">
      <div class="inventory-heading">
        <div>
          <h2>Available in the index</h2>
          <span>Curated packages ready to install</span>
        </div>
        <strong>{indexEntries.length}</strong>
      </div>
      <div class="table-scroll">
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
    max-width: 1100px;
    margin: 0 auto;
  }
  .page-header {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
    gap: 24px;
    margin-bottom: 18px;
  }
  .header-copy {
    min-width: 0;
  }
  .eyebrow {
    display: block;
    margin-bottom: 5px;
    color: var(--accent);
    font: 650 9px var(--font-mono);
    letter-spacing: 0.14em;
    text-transform: uppercase;
  }
  .page-header h1 {
    font-size: 24px;
    font-weight: 650;
    letter-spacing: -0.035em;
  }
  .header-copy p {
    margin-top: 4px;
    color: var(--text-muted);
    font-size: 12px;
  }
  .btn-primary,
  .btn-secondary {
    display: inline-flex;
    min-height: 34px;
    align-items: center;
    justify-content: center;
    padding: 0 14px;
    border: 1px solid var(--accent);
    border-radius: 6px;
    background: var(--accent);
    color: white;
    font-size: 11px;
    font-weight: 550;
    transition: all 150ms ease;
  }
  .btn-primary:hover:not(:disabled) {
    background: var(--accent-hover);
  }
  .btn-primary:disabled {
    opacity: 0.55;
    cursor: default;
  }
  .hidden-file {
    display: none;
  }
  .skeleton-rows {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    padding: 14px;
  }
  .inventory {
    display: flex;
    min-height: 280px;
    flex-direction: column;
    overflow: hidden;
    border: 1px solid var(--border-subtle);
    border-radius: 9px;
    background: var(--bg-secondary);
    box-shadow: var(--shadow-card);
  }
  .inventory-heading {
    display: flex;
    min-height: 54px;
    align-items: center;
    justify-content: space-between;
    gap: 16px;
    padding: 9px 14px;
    border-bottom: 1px solid var(--border-subtle);
  }
  .inventory-heading h2 {
    font-size: 12px;
    font-weight: 650;
  }
  .inventory-heading span {
    color: var(--text-muted);
    font-size: 9.5px;
  }
  .inventory-heading strong {
    display: grid;
    min-width: 28px;
    height: 24px;
    place-items: center;
    border-radius: 5px;
    background: var(--bg-tertiary);
    color: var(--text-muted);
    font: 600 10px var(--font-mono);
  }
  .empty-wrap {
    display: grid;
    min-height: 230px;
    place-items: center;
    padding: 24px;
  }
  .table-scroll {
    overflow-x: auto;
  }
  .table-scroll .table {
    min-width: 760px;
  }
  .table {
    overflow: visible;
  }
  .table-header,
  .table-row {
    display: grid;
    grid-template-columns: 2fr 1fr 2fr 1fr 80px;
    align-items: center;
    gap: 14px;
    min-height: 42px;
    padding: 0 16px;
  }
  .table-header {
    font-size: 9px;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--text-muted);
    border-bottom: 1px solid var(--border-subtle);
  }
  .table-row {
    min-height: 58px;
    border-bottom: 1px solid var(--border-subtle);
    color: var(--text-secondary);
    font-size: 10.5px;
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
    padding: 0;
    background: transparent;
    color: var(--text-primary);
    font: 650 11px var(--font-mono);
  }
  .plugin-desc {
    font-size: 0.75rem;
    color: var(--text-muted);
  }
  .mono {
    font-family: var(--font-mono);
  }
  .type-badge {
    display: inline-block;
    padding: 0.15rem 0.5rem;
    border-radius: 4px;
    font-size: 0.75rem;
    background: var(--accent-glow);
    color: var(--accent);
  }
  .type-badge.muted {
    background: var(--bg-tertiary);
    color: var(--text-muted);
  }
  .col-actions {
    display: flex;
    justify-content: flex-end;
    align-items: center;
  }
  .btn-icon {
    display: grid;
    width: 29px;
    height: 29px;
    place-items: center;
    border-radius: 5px;
    color: var(--text-muted);
    transition: all 150ms ease;
  }
  .btn-icon.danger:hover {
    background: var(--failed-bg);
    color: var(--failed);
  }

  .index-section {
    margin-top: 18px;
  }
  .index-table .table-header,
  .index-table .table-row {
    grid-template-columns: 2fr 1fr 3fr 100px;
  }
  .col-idx-desc {
    color: var(--text-muted);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .btn-secondary {
    border: 1px solid var(--border);
    background: var(--bg-tertiary);
    color: var(--text-secondary);
  }
  .btn-secondary:hover:not(:disabled) {
    background: var(--accent-glow);
    border-color: var(--accent);
  }
  .btn-secondary:disabled {
    opacity: 0.6;
    cursor: default;
  }
  @media (max-width: 768px) {
    .page-header {
      align-items: flex-start;
    }
    .table-scroll .table {
      min-width: 0;
    }
    .table-header {
      display: none;
    }
    .table-row {
      grid-template-columns: minmax(0, 1fr) auto;
      gap: 9px 14px;
      padding: 13px 14px;
    }
    .table-row .col-name {
      grid-column: 1;
      grid-row: 1;
    }
    .table-row .col-actions {
      grid-column: 2;
      grid-row: 1;
    }
    .table-row .col-version {
      grid-column: 1;
      grid-row: 2;
      color: var(--text-muted);
    }
    .table-row .col-nodes {
      grid-column: 1 / -1;
      grid-row: 3;
    }
    .table-row .col-source {
      grid-column: 2;
      grid-row: 2;
      text-align: right;
    }
    .index-table .table-row {
      grid-template-columns: minmax(0, 1fr) auto;
    }
    .index-table .col-idx-desc {
      grid-column: 1 / -1;
      grid-row: 3;
      white-space: normal;
    }
  }
  @media (max-width: 520px) {
    .page-header {
      flex-direction: column;
    }
    .page-header .btn-primary {
      width: 100%;
    }
    .inventory-heading span {
      display: none;
    }
  }
</style>
