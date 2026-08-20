<script lang="ts">
  import { onMount } from "svelte";
  import { api } from "../lib/api";
  import type { Plugin, PluginIndexEntry } from "../lib/types";
  import { notify } from "../lib/toast";
  import { icons } from "../lib/icons";
  import ConfirmDialog from "../components/ConfirmDialog.svelte";
  import EmptyState from "../components/EmptyState.svelte";
  import BrandIcon from "../components/BrandIcon.svelte";
  import Skeleton from "../components/Skeleton.svelte";

  let plugins: Plugin[] = [];
  let loading = true;
  let loadError = false;
  let pluginSupportUnavailable = false;
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
  $: nodeTypeCount = plugins.reduce((count, plugin) => count + (plugin.node_types?.length || 0), 0);
  $: packagedCount = plugins.filter((plugin) => plugin.packaged).length;

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
    loadError = false;
    pluginSupportUnavailable = false;
    try {
      plugins = await api.plugins.list();
    } catch (e: any) {
      // A server without plugin support returns 503 — an empty list is the
      // honest view, not an error the user needs to act on.
      if (e?.status === 503) {
        plugins = [];
        pluginSupportUnavailable = true;
      } else {
        loadError = true;
        notify.error(e?.message || "Failed to load plugins");
      }
    } finally {
      loading = false;
    }
  }

  async function retryPlugins() {
    await Promise.all([load(), loadIndex()]);
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
  <header class="identity-hero">
    <div class="hero-main">
      <div class="hero-mark" aria-hidden="true">PKG</div>
      <div class="header-copy">
        <span class="eyebrow">Organization control center / Extension catalog</span>
        <h1>Plugins</h1>
        <p>Install and manage signed node packages for your pipelines.</p>
      </div>
      <button class="btn-primary" on:click={pickFile} disabled={installing}>
        {installing ? "Installing…" : "+ Install Plugin"}
      </button>
    </div>
    <div class="status-summary" aria-label="Plugin summary">
      <div class="status-segment accent">
        <span>Installed packages</span><strong
          >{loading || loadError || pluginSupportUnavailable ? "—" : plugins.length}</strong
        ><small>organization extensions</small>
      </div>
      <div class="status-segment">
        <span>Node types</span><strong
          >{loading || loadError || pluginSupportUnavailable ? "—" : nodeTypeCount}</strong
        ><small>capabilities contributed</small>
      </div>
      <div class="status-segment">
        <span>Signed archives</span><strong
          >{loading || loadError || pluginSupportUnavailable ? "—" : packagedCount}</strong
        ><small>packaged installations</small>
      </div>
      <div class="status-segment catalog">
        <span>Catalog</span><strong
          >{indexLoading
            ? "Checking"
            : indexAvailable
              ? `${indexEntries.length} listed`
              : "Unavailable"}</strong
        ><small>curated package index</small>
      </div>
    </div>
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
        <span class="panel-kicker">Runtime inventory</span>
        <h2>Installed plugins</h2>
        <span>Packages available to pipeline nodes</span>
      </div>
      <strong>{loading || loadError || pluginSupportUnavailable ? "—" : plugins.length}</strong>
    </div>
    {#if loading}
      <div class="state-intro">
        <span class="state-pulse"></span>
        <div>
          <strong>Loading installed packages</strong><small
            >Inspecting organization plugin capabilities.</small
          >
        </div>
      </div>
      <div class="skeleton-rows" aria-label="Loading installed plugins">
        {#each Array(3) as _}
          <Skeleton height="48px" width="100%" />
        {/each}
      </div>
    {:else if loadError || pluginSupportUnavailable}
      <div class="unavailable-state">
        <span class="state-icon">!</span><strong
          >{pluginSupportUnavailable
            ? "Plugin runtime unavailable"
            : "Installed plugins unavailable"}</strong
        >
        <p>
          {pluginSupportUnavailable
            ? "This server does not currently expose plugin support. The catalog remains shown below for context."
            : "The installed package inventory could not be retrieved. No packages were changed."}
        </p>
        <button class="btn-secondary" on:click={retryPlugins}>Try again</button>
      </div>
    {:else if plugins.length === 0}
      <div class="empty-wrap">
        <EmptyState
          brandIcon="plugins"
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

  <section class="inventory index-section" aria-label="Plugin index inventory">
    <div class="inventory-heading">
      <div>
        <span class="panel-kicker">Curated catalog</span>
        <h2>Available in the index</h2>
        <span>Verified packages ready to install by name</span>
      </div>
      <strong>{indexLoading ? "—" : indexEntries.length}</strong>
    </div>
    {#if indexLoading}
      <div class="catalog-state loading-state">
         <span class="catalog-glyph"><BrandIcon name="api" size={20} /></span>
        <div>
          <strong>Connecting to the plugin index</strong>
          <p>The curated catalog will stay here while availability is checked.</p>
        </div>
      </div>
      <div class="catalog-preview" aria-hidden="true">
        {#each Array(3) as _}<Skeleton height="52px" width="100%" />{/each}
      </div>
    {:else if !indexAvailable}
      <div class="catalog-state">
         <span class="catalog-glyph unavailable"><BrandIcon name="api" size={20} /></span>
        <div>
          <strong>Catalog unavailable</strong>
          <p>
            The index is unreachable or not configured. Signed <code>.bkg</code> packages can still be
            installed from a local file when runtime support is available.
          </p>
          <button class="btn-secondary" on:click={loadIndex}>Check again</button>
        </div>
      </div>
    {:else if indexEntries.length === 0}
      <div class="catalog-state">
         <span class="catalog-glyph"><BrandIcon name="api" size={20} /></span>
        <div>
          <strong>The catalog is connected but empty</strong>
          <p>
            No curated packages are currently published. Local signed packages remain available
            through Install Plugin.
          </p>
        </div>
      </div>
    {:else}
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
    {/if}
  </section>
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
    width: 100%;
    min-width: 0;
  }
  .identity-hero {
    margin-bottom: 18px;
    overflow: hidden;
    border: 1px solid var(--border-subtle);
    border-radius: 11px;
    background:
      linear-gradient(
        120deg,
        color-mix(in srgb, var(--accent-glow), transparent 22%),
        transparent 52%
      ),
      var(--bg-secondary);
    box-shadow: var(--shadow-card);
  }
  .hero-main {
    display: flex;
    min-height: 112px;
    align-items: center;
    gap: 15px;
    padding: 20px;
  }
  .hero-mark {
    display: grid;
    width: 48px;
    height: 48px;
    flex: none;
    place-items: center;
    border: 1px solid color-mix(in srgb, var(--accent), transparent 60%);
    border-radius: 10px;
    background: var(--accent-glow);
    color: var(--accent);
    font: 700 9px var(--font-mono);
    letter-spacing: 0.08em;
  }
  .hero-main .header-copy {
    flex: 1;
  }
  .status-summary {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    border-top: 1px solid var(--border-subtle);
    background: color-mix(in srgb, var(--bg-primary), transparent 38%);
  }
  .status-segment {
    position: relative;
    display: grid;
    min-height: 72px;
    align-content: center;
    gap: 2px;
    padding: 11px 18px;
    border-right: 1px solid var(--border-subtle);
  }
  .status-segment:last-child {
    border-right: 0;
  }
  .status-segment::before {
    content: "";
    position: absolute;
    inset: 14px auto 14px 0;
    width: 2px;
    background: var(--border);
  }
  .status-segment.accent::before {
    background: var(--accent);
  }
  .status-segment.catalog::before {
    background: var(--running);
  }
  .status-segment span,
  .panel-kicker {
    color: var(--text-muted);
    font: 600 8.5px var(--font-mono);
    letter-spacing: 0.09em;
    text-transform: uppercase;
  }
  .status-segment strong {
    color: var(--text-primary);
    font-size: 17px;
    font-weight: 650;
  }
  .status-segment.catalog strong {
    font-size: 12px;
  }
  .status-segment small {
    color: var(--text-dim);
    font-size: 9px;
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
  .identity-hero h1 {
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
    margin-top: 3px;
    font-size: 12px;
    font-weight: 650;
  }
  .inventory-heading span {
    color: var(--text-muted);
    font-size: 9.5px;
  }
  .inventory-heading .panel-kicker {
    display: block;
    margin-bottom: 1px;
    color: var(--accent);
    font-size: 8px;
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
  .state-intro {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 13px 15px 0;
  }
  .state-intro div {
    display: grid;
    gap: 2px;
  }
  .state-intro strong {
    color: var(--text-secondary);
    font-size: 10.5px;
  }
  .state-intro small {
    color: var(--text-dim);
    font-size: 9px;
  }
  .state-pulse {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--accent);
    box-shadow: 0 0 0 5px var(--accent-glow);
  }
  .unavailable-state {
    display: flex;
    min-height: 230px;
    align-items: center;
    justify-content: center;
    flex-direction: column;
    gap: 7px;
    padding: 28px;
    text-align: center;
  }
  .unavailable-state .state-icon {
    display: grid;
    width: 36px;
    height: 36px;
    margin-bottom: 4px;
    place-items: center;
    border: 1px solid color-mix(in srgb, var(--failed), transparent 65%);
    border-radius: 50%;
    background: var(--failed-bg);
    color: var(--failed);
    font-weight: 700;
  }
  .unavailable-state strong,
  .catalog-state strong {
    color: var(--text-primary);
    font-size: 12px;
  }
  .unavailable-state p {
    max-width: 450px;
    color: var(--text-muted);
    font-size: 10px;
    line-height: 1.5;
  }
  .unavailable-state button {
    margin-top: 7px;
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
  .catalog-state {
    display: flex;
    min-height: 190px;
    align-items: center;
    justify-content: center;
    flex-direction: column;
    gap: 14px;
    padding: 28px;
    text-align: center;
  }
  .catalog-state div {
    max-width: 500px;
    text-align: center;
  }
  .catalog-state p {
    margin-top: 5px;
    color: var(--text-muted);
    font-size: 10px;
    line-height: 1.55;
  }
  .catalog-state code {
    color: var(--accent);
    font: 9.5px var(--font-mono);
  }
  .catalog-state button {
    margin-top: 12px;
  }
  .catalog-glyph {
    display: grid;
    width: 42px;
    height: 42px;
    flex: none;
    place-items: center;
    border: 1px solid color-mix(in srgb, var(--running), transparent 60%);
    border-radius: 9px;
    background: var(--running-bg);
    color: var(--running);
    font: 700 8px var(--font-mono);
    letter-spacing: 0.08em;
  }
  .catalog-glyph.unavailable {
    border-color: var(--border);
    background: var(--bg-tertiary);
    color: var(--text-muted);
  }
  .loading-state {
    min-height: 94px;
    justify-content: flex-start;
    border-bottom: 1px solid var(--border-subtle);
  }
  .catalog-preview {
    display: grid;
    gap: 8px;
    padding: 12px 14px 14px;
    opacity: 0.58;
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
    .hero-main {
      align-items: flex-start;
    }
    .status-summary {
      grid-template-columns: repeat(2, 1fr);
    }
    .status-segment:nth-child(2) {
      border-right: 0;
    }
    .status-segment:nth-child(-n + 2) {
      border-bottom: 1px solid var(--border-subtle);
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
    .hero-main {
      flex-wrap: wrap;
      padding: 16px;
    }
    .hero-mark {
      width: 40px;
      height: 40px;
    }
    .hero-main .header-copy {
      width: calc(100% - 56px);
      flex: none;
    }
    .hero-main .btn-primary {
      width: 100%;
    }
    .status-summary {
      grid-template-columns: 1fr;
    }
    .status-segment,
    .status-segment:nth-child(2) {
      min-height: 58px;
      border-right: 0;
      border-bottom: 1px solid var(--border-subtle);
    }
    .status-segment:last-child {
      border-bottom: 0;
    }
    .catalog-state {
      align-items: flex-start;
      padding: 22px 18px;
    }
    .inventory-heading span {
      display: none;
    }
  }
</style>
