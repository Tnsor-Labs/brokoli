<script lang="ts">
  import { onMount } from "svelte";
  import { notify } from "../lib/toast";
  import { authHeaders } from "../lib/auth";
  import PageHeader from "../components/PageHeader.svelte";
  import ConfirmDialog from "../components/ConfirmDialog.svelte";
  import Pagination from "../components/Pagination.svelte";
  import Skeleton from "../components/Skeleton.svelte";
  import EmptyState from "../components/EmptyState.svelte";
  import { icons } from "../lib/icons";

  interface Connection {
    id: string;
    conn_id: string;
    type: string;
    description: string;
    host: string;
    port: number;
    schema: string;
    login: string;
    password?: string;
    extra?: string;
    max_concurrent?: number;
    created_at: string;
    updated_at: string;
  }

  import CatalogCard from "../components/CatalogCard.svelte";
  import { groupByCategory, describe, monogramFor } from "../lib/connectionCatalog";
  import type { ConnTypeMeta } from "../lib/connectionCatalog";

  type ConnType = ConnTypeMeta;

  let connections: Connection[] = [];
  let connPage = 1;
  let connPageSize = 25;
  let connTypes: ConnType[] = [];
  let loading = true;
  let loadError = false;
  let searchQuery = "";
  let typeFilter = "";

  // Modal state — creating is a two-step flow: pick a type from the
  // catalog, then fill the type's fields. Editing goes straight to the
  // form (type is immutable on an existing connection).
  let showModal = false;
  let editing = false;
  let modalStep: "catalog" | "form" = "catalog";
  let catalogQuery = "";
  let form: Partial<Connection> = {};
  let testing = false;
  let testResult: { success: boolean; message?: string; error?: string } | null = null;

  // Delete
  let confirmDelete = false;
  let deleteTarget = "";

  onMount(async () => {
    await Promise.all([loadConnections(), loadTypes()]);
    loading = false;
  });

  async function loadConnections() {
    loadError = false;
    try {
      const res = await fetch("/api/connections", { headers: authHeaders() });
      if (!res.ok) throw new Error("Connections unavailable");
      const data = await res.json();
      connections = Array.isArray(data) ? data : [];
    } catch {
      loadError = true;
      notify.error("Failed to load connections");
    }
  }

  async function retryLoad() {
    loading = true;
    await Promise.all([loadConnections(), loadTypes()]);
    loading = false;
  }

  async function loadTypes() {
    try {
      const res = await fetch("/api/connection-types", { headers: authHeaders() });
      connTypes = await res.json();
    } catch {
      connTypes = [];
    }
  }

  function openCreate() {
    editing = false;
    modalStep = "catalog";
    catalogQuery = "";
    form = {};
    testResult = null;
    showModal = true;
  }

  function openEdit(c: Connection) {
    editing = true;
    modalStep = "form";
    form = { ...c };
    testResult = null;
    showModal = true;
  }

  function chooseType(type: string) {
    onTypeChange(type);
    modalStep = "form";
  }

  function backToCatalog() {
    modalStep = "catalog";
    testResult = null;
  }

  function typeMeta(type: string): ConnType | undefined {
    return connTypes.find((t) => t.type === type);
  }

  function typeIconD(ct: ConnType | undefined): string {
    if (!ct?.icon) return "";
    const entry = (icons as Record<string, { d: string } | undefined>)[ct.icon];
    return entry?.d || "";
  }

  // Server-provided per-field hints double as placeholders in the form.
  function typeHint(field: string, fallback: string): string {
    return typeMeta(form.type || "")?.hints?.[field] || fallback;
  }

  $: catalogGroups = groupByCategory(
    connTypes.filter((ct) => {
      const q = catalogQuery.trim().toLowerCase();
      if (!q) return true;
      return (
        ct.label.toLowerCase().includes(q) ||
        ct.type.toLowerCase().includes(q) ||
        describe(ct).toLowerCase().includes(q)
      );
    }),
  );

  async function saveConnection() {
    if (!form.conn_id?.trim()) {
      notify.warning("Connection ID is required");
      return;
    }
    if (!form.type) {
      notify.warning("Type is required");
      return;
    }

    try {
      const method = editing ? "PUT" : "POST";
      const url = editing ? `/api/connections/${form.conn_id}` : "/api/connections";
      const res = await fetch(url, {
        method,
        headers: { "Content-Type": "application/json", ...authHeaders() },
        body: JSON.stringify(form),
      });
      if (!res.ok) {
        const err = await res.json();
        notify.error(err.error || "Failed to save");
        return;
      }
      notify.success(editing ? "Connection updated" : "Connection created");
      showModal = false;
      await loadConnections();
    } catch {
      notify.error("Failed to save connection");
    }
  }

  async function deleteConnection(connId: string) {
    try {
      await fetch(`/api/connections/${connId}`, { method: "DELETE", headers: authHeaders() });
      notify.success("Connection deleted");
      await loadConnections();
    } catch {
      notify.error("Failed to delete");
    }
  }

  async function testConnection() {
    if (!form.conn_id || !editing) {
      // For new connections, save first
      notify.warning("Save the connection first, then test it");
      return;
    }
    testing = true;
    testResult = null;
    try {
      const res = await fetch(`/api/connections/${form.conn_id}/test`, {
        method: "POST",
        headers: authHeaders(),
      });
      testResult = await res.json();
    } catch {
      testResult = { success: false, error: "Test failed" };
    } finally {
      testing = false;
    }
  }

  function typeLabel(type: string): string {
    return connTypes.find((t) => t.type === type)?.label || type;
  }

  function typeFields(type: string): string[] {
    return connTypes.find((t) => t.type === type)?.fields || [];
  }

  // Default ports per type
  function defaultPort(type: string): number {
    switch (type) {
      case "postgres":
        return 5432;
      case "mysql":
        return 3306;
      case "clickhouse":
        return 9000;
      case "http":
        return 443;
      case "sftp":
        return 22;
      default:
        return 0;
    }
  }

  function onTypeChange(type: string) {
    form.type = type;
    form.port = defaultPort(type);
  }

  $: filteredConnections = connections.filter((connection) => {
    const query = searchQuery.trim().toLowerCase();
    const matchesQuery =
      !query ||
      connection.conn_id.toLowerCase().includes(query) ||
      (connection.description || "").toLowerCase().includes(query) ||
      (connection.host || "").toLowerCase().includes(query) ||
      typeLabel(connection.type).toLowerCase().includes(query);
    return matchesQuery && (!typeFilter || connection.type === typeFilter);
  });
  $: paginatedConnections = filteredConnections.slice(
    (connPage - 1) * connPageSize,
    connPage * connPageSize,
  );
  $: configuredTypes = new Set(connections.map((connection) => connection.type)).size;
  $: hostedConnections = connections.filter((connection) => Boolean(connection.host)).length;
  $: if (searchQuery || typeFilter) connPage = 1;
</script>

<div class="connections-page animate-in">
  <PageHeader
    brandIcon="connections"
    kicker="Organization control center / Resource catalog"
    title="Connections"
    description="Manage the external systems and credentials used by your pipelines."
    actionLabel="New Connection"
    on:click={openCreate}
  >
    <div slot="kpi-rail" class="status-summary" aria-label="Connection summary">
      <div class="status-segment accent">
        <span>Total connections</span><strong
          >{loading || loadError ? "—" : connections.length}</strong
        ><small>configured resources</small>
      </div>
      <div class="status-segment">
        <span>Service types</span><strong>{loading || loadError ? "—" : configuredTypes}</strong
        ><small>connection categories</small>
      </div>
      <div class="status-segment">
        <span>Network endpoints</span><strong
          >{loading || loadError ? "—" : hostedConnections}</strong
        ><small>with a host configured</small>
      </div>
    </div>
  </PageHeader>

  <section class="inventory" aria-label="Connection inventory">
    <div class="inventory-heading">
      <div>
        <span class="panel-kicker">Resource inventory</span>
        <h2>Organization connections</h2>
        <p>Credential-backed endpoints available to pipelines.</p>
      </div>
      <span class="inventory-count">{loading || loadError ? "—" : connections.length}</span>
    </div>
    <div class="inventory-toolbar">
      <label class="search-bar">
        <span class="sr-only">Search connections</span>
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none"
          ><path
            d={icons.search.d}
            stroke="currentColor"
            stroke-width="1.8"
            stroke-linecap="round"
            stroke-linejoin="round"
          /></svg
        >
        <input
          type="search"
          bind:value={searchQuery}
          placeholder="Search ID, host, type, or description..."
        />
      </label>
      <div class="filter-controls">
        <label for="connection-type-filter">Type</label>
        <select id="connection-type-filter" bind:value={typeFilter}>
          <option value="">All types</option>
          {#each connTypes as ct}<option value={ct.type}>{ct.label}</option>{/each}
        </select>
        <span class="result-count">{filteredConnections.length} of {connections.length}</span>
      </div>
    </div>

    {#if loading}
      <div class="state-intro">
        <span class="state-pulse"></span>
        <div>
          <strong>Loading connection inventory</strong><small
            >Retrieving organization resources and endpoint metadata.</small
          >
        </div>
      </div>
      <div class="skeleton-rows" aria-label="Loading connections">
        {#each Array(4) as _}<Skeleton height="58px" width="100%" />{/each}
      </div>
    {:else if loadError}
      <div class="unavailable-state">
        <span class="state-icon">!</span><strong>Connection inventory unavailable</strong>
        <p>
          The organization resource service could not be reached. Existing connections have not been
          changed.
        </p>
        <button class="btn-secondary" on:click={retryLoad}>Try again</button>
      </div>
    {:else if connections.length === 0}
      <div class="empty-wrap">
        <EmptyState
          brandIcon="connections"
          title="No connections configured"
          description="Connections store credentials for databases, APIs, and other external services your pipelines use."
          ctaLabel="+ New Connection"
          on:click={openCreate}
        />
      </div>
    {:else if filteredConnections.length === 0}
      <div class="no-results">
        <strong>No matching connections</strong><span
          >Try a different search term or type filter.</span
        ><button
          on:click={() => {
            searchQuery = "";
            typeFilter = "";
          }}>Clear filters</button
        >
      </div>
    {:else}
      <div class="table-scroll">
        <div class="table">
          <div class="table-header">
            <span class="col-id">Connection</span><span class="col-type">Type</span><span
              class="col-host">Endpoint</span
            ><span class="col-desc">Description</span><span class="col-actions">Actions</span>
          </div>
          {#each paginatedConnections as conn}
            <div class="table-row">
              <span class="col-id"
                ><span class="resource-mark">{conn.conn_id.slice(0, 1).toUpperCase()}</span><span
                  class="identity"><code>{conn.conn_id}</code><small>Connection ID</small></span
                ></span
              >
              <span class="col-type"><span class="type-badge">{typeLabel(conn.type)}</span></span>
              <span class="col-host"
                ><strong class="mono"
                  >{conn.host || "Not set"}{conn.port ? `:${conn.port}` : ""}</strong
                ><small>{conn.schema || "Default schema"}</small></span
              >
              <span class="col-desc">{conn.description || "No description"}</span>
              <span class="col-actions">
                <button
                  class="btn-icon"
                  aria-label="Edit {conn.conn_id}"
                  title="Edit connection"
                  on:click={() => openEdit(conn)}
                  ><svg width="13" height="13" viewBox="0 0 24 24" fill="none"
                    ><path
                      d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7M18.5 2.5a2.12 2.12 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"
                      stroke="currentColor"
                      stroke-width="1.5"
                      stroke-linecap="round"
                      stroke-linejoin="round"
                    /></svg
                  ></button
                >
                <button
                  class="btn-icon danger"
                  aria-label="Delete {conn.conn_id}"
                  title="Delete connection"
                  on:click={() => {
                    deleteTarget = conn.conn_id;
                    confirmDelete = true;
                  }}
                  ><svg width="13" height="13" viewBox="0 0 24 24" fill="none"
                    ><path
                      d="M3 6h18M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2M10 11v6M14 11v6"
                      stroke="currentColor"
                      stroke-width="1.5"
                      stroke-linecap="round"
                      stroke-linejoin="round"
                    /></svg
                  ></button
                >
              </span>
            </div>
          {/each}
        </div>
      </div>
      <Pagination
        total={filteredConnections.length}
        page={connPage}
        pageSize={connPageSize}
        on:page={(e) => (connPage = e.detail)}
        on:pagesize={(e) => {
          connPageSize = e.detail;
          connPage = 1;
        }}
      />
    {/if}
  </section>

  {#if showModal}
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <div class="modal-overlay" on:click={() => (showModal = false)} on:keydown={() => {}}>
      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <div
        class="modal"
        class:wide={!editing && modalStep === "catalog"}
        role="dialog"
        tabindex="-1"
        aria-modal="true"
        aria-labelledby="connection-dialog-title"
        on:click|stopPropagation
        on:keydown={() => {}}
      >
        <header class="modal-header">
          <div class="modal-heading">
            <span class="modal-mark">{editing ? "↗" : "+"}</span>
            <div>
              <h2 id="connection-dialog-title">
                {editing
                  ? "Edit connection"
                  : modalStep === "catalog"
                    ? "New connection"
                    : `New ${typeMeta(form.type || "")?.label || "connection"}`}
              </h2>
              <p>
                {editing
                  ? "Update access details or verify connectivity."
                  : modalStep === "catalog"
                    ? "Pick the system you're connecting — databases, storage, APIs."
                    : "Add a reusable external resource for your pipelines."}
              </p>
            </div>
          </div>
          <button
            class="modal-close"
            aria-label="Close connection dialog"
            on:click={() => (showModal = false)}>×</button
          >
        </header>

        {#if !editing && modalStep === "catalog"}
          <div class="modal-body catalog-body">
            <input
              class="catalog-search"
              type="search"
              placeholder="Search types — postgres, bucket, api..."
              bind:value={catalogQuery}
              aria-label="Search connection types"
            />
            {#if connTypes.length === 0}
              <div class="catalog-empty">
                Connection types unavailable — check the server connection and retry.
              </div>
            {:else if catalogGroups.length === 0}
              <div class="catalog-empty">No connection type matches "{catalogQuery}".</div>
            {:else}
              {#each catalogGroups as group (group.category)}
                <section class="catalog-section">
                  <h3 class="catalog-kicker">{group.label}</h3>
                  <div class="catalog-grid">
                    {#each group.types as ct (ct.type)}
                      <CatalogCard
                        title={ct.label}
                        description={describe(ct)}
                        iconD={typeIconD(ct)}
                        monogram={monogramFor(ct.label)}
                        category={group.category}
                        on:click={() => chooseType(ct.type)}
                      />
                    {/each}
                  </div>
                </section>
              {/each}
            {/if}
          </div>
          <div class="modal-actions">
            <span class="spacer"></span>
            <button class="btn-secondary" on:click={() => (showModal = false)}>Cancel</button>
          </div>
        {:else}
          <div class="modal-body">
            {#if !editing}
              <div class="type-summary">
                <span class="type-tile cat-{typeMeta(form.type || '')?.category || 'other'}">
                  {#if typeIconD(typeMeta(form.type || ""))}
                    <svg width="18" height="18" viewBox="0 0 24 24" fill="none">
                      <path
                        d={typeIconD(typeMeta(form.type || ""))}
                        stroke="currentColor"
                        stroke-width="1.5"
                        stroke-linecap="round"
                        stroke-linejoin="round"
                      />
                    </svg>
                  {:else}
                    <span class="type-monogram">{monogramFor(typeLabel(form.type || ""))}</span>
                  {/if}
                </span>
                <div class="type-summary-copy">
                  <strong>{typeLabel(form.type || "")}</strong>
                  <small
                    >{describe(
                      typeMeta(form.type || "") || { type: "", label: "", fields: [] },
                    )}</small
                  >
                </div>
                <button class="btn-secondary back-type" on:click={backToCatalog}
                  >← Change type</button
                >
              </div>
            {/if}

            <div class="form-group">
              <label for="connection-id">Connection ID</label>
              <input
                id="connection-id"
                value={form.conn_id || ""}
                on:input={(e) =>
                  (form.conn_id = e.currentTarget.value.toLowerCase().replace(/[^a-z0-9_-]/g, ""))}
                placeholder="my_{form.type || 'connection'}"
                disabled={editing}
              />
              {#if !editing}<span class="field-hint">Lowercase, hyphens, underscores only</span
                >{/if}
            </div>

            {#if editing}
              <div class="form-group">
                <label for="connection-type">Type</label>
                <select id="connection-type" value={form.type || "postgres"} disabled>
                  {#each connTypes as ct}
                    <option value={ct.type}>{ct.label}</option>
                  {/each}
                </select>
              </div>
            {/if}

            <div class="form-group">
              <label for="connection-description">Description <span>Optional</span></label>
              <input
                id="connection-description"
                value={form.description || ""}
                on:input={(e) => (form.description = e.currentTarget.value)}
                placeholder="Production database"
              />
            </div>

            {#if typeFields(form.type || "").includes("host")}
              <div class="form-row">
                <div class="form-group flex-2">
                  <label for="connection-host">Host</label>
                  <input
                    id="connection-host"
                    value={form.host || ""}
                    on:input={(e) => (form.host = e.currentTarget.value)}
                    placeholder={typeHint("host", "localhost")}
                  />
                </div>
                {#if typeFields(form.type || "").includes("port")}
                  <div class="form-group flex-1">
                    <label for="connection-port">Port</label>
                    <input
                      id="connection-port"
                      type="number"
                      value={form.port || 0}
                      on:input={(e) => (form.port = Number(e.currentTarget.value))}
                    />
                  </div>
                {/if}
              </div>
            {/if}

            {#if typeFields(form.type || "").includes("schema")}
              <div class="form-group">
                <label for="connection-schema">Database / Schema</label>
                <input
                  id="connection-schema"
                  value={form.schema || ""}
                  on:input={(e) => (form.schema = e.currentTarget.value)}
                  placeholder={typeHint("schema", "mydb")}
                />
              </div>
            {/if}

            {#if typeFields(form.type || "").includes("login")}
              <div class="form-row">
                <div class="form-group flex-1">
                  <label for="connection-login">Login / Username</label>
                  <input
                    id="connection-login"
                    value={form.login || ""}
                    on:input={(e) => (form.login = e.currentTarget.value)}
                    placeholder={typeHint("login", "admin")}
                  />
                </div>
                {#if typeFields(form.type || "").includes("password")}
                  <div class="form-group flex-1">
                    <label for="connection-password">Password</label>
                    <input
                      id="connection-password"
                      type="password"
                      value={form.password || ""}
                      on:input={(e) => (form.password = e.currentTarget.value)}
                      placeholder={editing ? "Leave blank to keep" : "password"}
                    />
                  </div>
                {/if}
              </div>
            {/if}

            {#if typeFields(form.type || "").includes("extra")}
              <div class="form-group">
                <label for="connection-extra">Extra (JSON)</label>
                <textarea
                  id="connection-extra"
                  class="code-input"
                  rows="3"
                  value={form.extra || ""}
                  on:input={(e) => (form.extra = e.currentTarget.value)}
                  placeholder={typeHint("extra", "JSON extra fields")}
                ></textarea>
                <span class="field-hint"
                  >Type-specific fields: headers, tokens, bucket, region, etc.</span
                >
              </div>
            {/if}

            <div class="form-group">
              <label for="connection-max-concurrent">Max concurrent node executions</label>
              <input
                id="connection-max-concurrent"
                type="number"
                min="0"
                value={form.max_concurrent || 0}
                on:input={(e) => (form.max_concurrent = Number(e.currentTarget.value))}
              />
              <span class="field-hint"
                >0 = unlimited. Nodes over the budget wait (visible in the run log). Per engine
                instance in this release.</span
              >
            </div>

            {#if testResult}
              <div
                class="test-result"
                role="status"
                class:success={testResult.success}
                class:error={!testResult.success}
              >
                {testResult.success
                  ? "Connection successful"
                  : testResult.error || "Connection failed"}
              </div>
            {/if}
          </div>
          <div class="modal-actions">
            {#if editing}
              <button class="btn-secondary" on:click={testConnection} disabled={testing}>
                {testing ? "Testing..." : "Test Connection"}
              </button>
            {/if}
            <span class="spacer"></span>
            <button class="btn-secondary" on:click={() => (showModal = false)}>Cancel</button>
            <button class="btn-primary" on:click={saveConnection}>
              {editing ? "Update" : "Create"}
            </button>
          </div>
        {/if}
      </div>
    </div>
  {/if}
</div>

<ConfirmDialog
  bind:visible={confirmDelete}
  title="Delete Connection"
  message="Are you sure? Any pipelines referencing this connection will need to be updated."
  confirmLabel="Delete"
  destructive={true}
  on:confirm={() => deleteConnection(deleteTarget)}
/>

<style>
  .connections-page {
    width: 100%;
    min-width: 0;
  }
  .status-summary {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
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
  .status-segment small {
    color: var(--text-dim);
    font-size: 9px;
  }
  .inventory-heading {
    display: flex;
    min-height: 66px;
    align-items: center;
    justify-content: space-between;
    gap: 16px;
    padding: 11px 14px;
    border-bottom: 1px solid var(--border-subtle);
    background: linear-gradient(
      90deg,
      color-mix(in srgb, var(--bg-tertiary), transparent 35%),
      transparent
    );
  }
  .inventory-heading h2 {
    margin-top: 3px;
    color: var(--text-primary);
    font-size: 13px;
    font-weight: 650;
  }
  .inventory-heading p {
    margin-top: 2px;
    color: var(--text-muted);
    font-size: 9.5px;
  }
  .inventory-count {
    display: grid;
    min-width: 30px;
    height: 26px;
    place-items: center;
    border: 1px solid var(--border-subtle);
    border-radius: 5px;
    background: var(--bg-primary);
    color: var(--text-muted);
    font: 600 10px var(--font-mono);
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
    min-height: 380px;
    align-items: center;
    justify-content: center;
    flex-direction: column;
    gap: 7px;
    padding: 32px;
    text-align: center;
  }
  .unavailable-state .state-icon {
    display: grid;
    width: 38px;
    height: 38px;
    margin-bottom: 4px;
    place-items: center;
    border: 1px solid color-mix(in srgb, var(--failed), transparent 65%);
    border-radius: 50%;
    background: var(--failed-bg);
    color: var(--failed);
    font-weight: 700;
  }
  .unavailable-state strong {
    color: var(--text-primary);
    font-size: 13px;
  }
  .unavailable-state p {
    max-width: 430px;
    color: var(--text-muted);
    font-size: 10.5px;
    line-height: 1.5;
  }
  .unavailable-state button {
    margin-top: 8px;
  }
  .btn-primary,
  .btn-secondary {
    display: inline-flex;
    min-height: 34px;
    align-items: center;
    justify-content: center;
    gap: 7px;
    padding: 0 14px;
    border: 1px solid var(--accent);
    border-radius: 6px;
    background: var(--accent);
    color: var(--bk-action-primary-fg);
    font-size: 11px;
    font-weight: 550;
    transition: all 150ms ease;
  }
  .btn-primary:hover {
    background: var(--accent-hover);
  }
  .btn-secondary {
    border-color: var(--border);
    background: var(--bg-secondary);
    color: var(--text-secondary);
  }
  .btn-secondary:hover {
    border-color: var(--border-hover);
    background: var(--bg-tertiary);
    color: var(--text-primary);
  }

  .inventory {
    display: flex;
    min-height: 520px;
    margin-top: 18px;
    flex-direction: column;
    overflow: hidden;
    border: 1px solid var(--border-subtle);
    border-radius: 9px;
    background: var(--bg-secondary);
    box-shadow: var(--shadow-card);
  }
  .inventory-toolbar {
    display: grid;
    grid-template-columns: minmax(260px, 1fr) auto;
    align-items: center;
    gap: 12px;
    padding: 10px 12px;
    border-bottom: 1px solid var(--border-subtle);
  }
  .search-bar {
    display: flex;
    height: 34px;
    align-items: center;
    gap: 8px;
    padding: 0 10px;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--bg-primary);
    color: var(--text-muted);
  }
  .search-bar:focus-within {
    border-color: var(--accent);
    box-shadow: 0 0 0 2px var(--accent-glow);
  }
  .search-bar input {
    width: 100%;
    border: 0;
    outline: 0;
    background: transparent;
    color: var(--text-primary);
    font-size: 11px;
  }
  .filter-controls {
    display: flex;
    align-items: center;
    gap: 8px;
    color: var(--text-dim);
    font-size: 9px;
    text-transform: uppercase;
  }
  .filter-controls select {
    height: 32px;
    min-width: 120px;
    padding: 0 28px 0 10px;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--bg-secondary);
    color: var(--text-secondary);
    font: 11px var(--font-ui);
    text-transform: none;
  }
  .result-count {
    min-width: 52px;
    color: var(--text-dim);
    font: 9px var(--font-mono);
    text-align: right;
    text-transform: none;
  }
  .sr-only {
    position: absolute;
    width: 1px;
    height: 1px;
    padding: 0;
    overflow: hidden;
    clip: rect(0, 0, 0, 0);
    white-space: nowrap;
    border: 0;
  }

  .table-scroll {
    overflow-x: auto;
  }
  .table {
    min-width: 800px;
  }
  .table-header,
  .table-row {
    display: grid;
    grid-template-columns: 1.35fr 0.75fr 1.25fr 1.4fr 72px;
    align-items: center;
    gap: 14px;
    padding: 0 16px;
  }
  .table-header {
    min-height: 40px;
    border-bottom: 1px solid var(--border-subtle);
    color: var(--text-muted);
    font-size: 9px;
    font-weight: 600;
    letter-spacing: 0.07em;
    text-transform: uppercase;
  }
  .table-row {
    min-height: 62px;
    border-bottom: 1px solid var(--border-subtle);
    color: var(--text-secondary);
    transition: background 150ms ease;
  }
  .table-row:last-child {
    border-bottom: 0;
  }
  .table-row:hover {
    background: var(--bg-card-hover);
  }
  .col-id {
    display: flex;
    min-width: 0;
    align-items: center;
    gap: 10px;
  }
  .resource-mark {
    display: grid;
    width: 30px;
    height: 30px;
    flex: none;
    place-items: center;
    border: 1px solid color-mix(in srgb, var(--accent), transparent 68%);
    border-radius: 7px;
    background: var(--accent-glow);
    color: var(--accent);
    font: 650 11px var(--font-mono);
  }
  .identity,
  .col-host {
    display: flex;
    min-width: 0;
    flex-direction: column;
    gap: 3px;
  }
  .identity code {
    overflow: hidden;
    padding: 0;
    background: transparent;
    color: var(--text-primary);
    font: 650 11px var(--font-mono);
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .identity small,
  .col-host small {
    color: var(--text-dim);
    font-size: 8.5px;
  }
  .type-badge {
    display: inline-flex;
    width: fit-content;
    min-height: 24px;
    align-items: center;
    padding: 0 8px;
    border: 1px solid var(--border-subtle);
    border-radius: 5px;
    background: var(--bg-tertiary);
    color: var(--text-secondary);
    font-size: 10px;
    font-weight: 550;
  }
  .col-host strong {
    overflow: hidden;
    color: var(--text-secondary);
    font-size: 10px;
    font-weight: 500;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .col-desc {
    overflow: hidden;
    color: var(--text-muted);
    font-size: 10.5px;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .col-actions {
    display: flex;
    justify-content: flex-end;
    gap: 3px;
  }
  .mono {
    font-family: var(--font-mono);
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
  .btn-icon:hover {
    background: var(--bg-tertiary);
    color: var(--text-primary);
  }
  .btn-icon.danger:hover {
    background: var(--failed-bg);
    color: var(--failed);
  }
  .skeleton-rows {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 14px;
  }
  .empty-wrap {
    display: grid;
    min-height: 410px;
    place-items: center;
    padding: 30px;
  }
  .no-results {
    display: flex;
    min-height: 360px;
    align-items: center;
    justify-content: center;
    flex-direction: column;
    gap: 6px;
    color: var(--text-muted);
    font-size: 11px;
  }
  .no-results strong {
    color: var(--text-primary);
    font-size: 13px;
  }
  .no-results button {
    margin-top: 7px;
    color: var(--accent);
    font-size: 10px;
  }

  .modal-overlay {
    position: fixed;
    inset: 0;
    z-index: 100;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 16px;
    background: rgba(0, 4, 6, 0.78);
    backdrop-filter: blur(8px) saturate(0.8);
    animation: overlay-in 150ms ease;
  }
  .modal {
    width: min(610px, 100%);
    max-height: calc(100vh - 32px);
    overflow: hidden;
    border: 1px solid color-mix(in srgb, var(--border), white 8%);
    border-radius: 12px;
    background: var(--bg-secondary);
    box-shadow:
      0 32px 90px rgba(0, 0, 0, 0.62),
      inset 0 1px 0 rgba(255, 255, 255, 0.035);
    animation: modal-in 200ms cubic-bezier(0.16, 1, 0.3, 1);
  }
  .modal.wide {
    width: min(860px, 100%);
  }

  /* ── Type catalog (create step 1) ── */
  .catalog-body {
    display: flex;
    flex-direction: column;
    gap: 18px;
  }
  .catalog-search {
    width: 100%;
    padding: 9px 12px;
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius-md);
    background: var(--bg-input);
    color: var(--text-primary);
    font-size: 13px;
  }
  .catalog-search:focus {
    border-color: var(--accent);
    outline: none;
  }
  .catalog-section {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .catalog-kicker {
    margin: 0;
    color: var(--text-dim);
    font-family: var(--font-mono);
    font-size: 10px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.12em;
  }
  .catalog-grid {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 10px;
  }
  @media (max-width: 700px) {
    .catalog-grid {
      grid-template-columns: 1fr;
    }
  }
  .catalog-empty {
    padding: 24px;
    border: 1px dashed var(--border-subtle);
    border-radius: var(--radius-lg);
    color: var(--text-muted);
    font-size: 12px;
    text-align: center;
  }

  /* ── Chosen-type summary (create step 2) ── */
  .type-summary {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 10px 12px;
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius-lg);
    background: var(--bg-tertiary);
    margin-bottom: 4px;
  }
  .type-tile {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 34px;
    height: 34px;
    border-radius: var(--radius-md);
    flex-shrink: 0;
  }
  .type-tile.cat-database {
    background: var(--accent-glow);
    color: var(--accent);
  }
  .type-tile.cat-storage {
    background: var(--success-bg);
    color: var(--success);
  }
  .type-tile.cat-api {
    background: var(--warning-bg);
    color: var(--warning);
  }
  .type-tile.cat-other {
    background: var(--bg-secondary);
    color: var(--text-muted);
  }
  .type-monogram {
    font-family: var(--font-mono);
    font-size: 12px;
    font-weight: 700;
  }
  .type-summary-copy {
    display: flex;
    flex-direction: column;
    gap: 1px;
    min-width: 0;
    flex: 1;
  }
  .type-summary-copy strong {
    color: var(--text-primary);
    font-size: 13px;
  }
  .type-summary-copy small {
    color: var(--text-muted);
    font-size: 11px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .back-type {
    flex-shrink: 0;
    font-size: 11px;
    padding: 6px 10px;
  }
  .modal-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 18px 20px;
    border-bottom: 1px solid var(--border-subtle);
  }
  .modal-heading {
    display: flex;
    align-items: center;
    gap: 12px;
  }
  .modal-mark {
    display: grid;
    width: 38px;
    height: 38px;
    place-items: center;
    border: 1px solid color-mix(in srgb, var(--accent), transparent 65%);
    border-radius: 9px;
    background: var(--accent-glow);
    color: var(--accent);
    font-size: 20px;
  }
  .modal h2 {
    color: var(--text-primary);
    font-size: 15px;
    font-weight: 650;
    letter-spacing: -0.015em;
  }
  .modal-heading p {
    margin-top: 3px;
    color: var(--text-muted);
    font-size: 10.5px;
  }
  .modal-close {
    display: grid;
    width: 32px;
    height: 32px;
    place-items: center;
    border-radius: 6px;
    color: var(--text-muted);
    font-size: 20px;
  }
  .modal-close:hover {
    background: var(--bg-tertiary);
    color: var(--text-primary);
  }
  .modal-body {
    max-height: calc(100vh - 190px);
    padding: 20px;
    overflow-y: auto;
  }
  .form-group {
    margin-bottom: 14px;
  }
  .form-group:last-child {
    margin-bottom: 0;
  }
  .form-group label {
    display: flex;
    justify-content: space-between;
    margin-bottom: 7px;
    color: var(--text-secondary);
    font-size: 10px;
    font-weight: 550;
  }
  .form-group label span {
    color: var(--text-dim);
    font-size: 8.5px;
    font-weight: 400;
  }
  .form-group input,
  .form-group select,
  .form-group textarea {
    width: 100%;
    border-color: var(--border);
    border-radius: 6px;
    background: var(--bg-primary);
    font-size: 11px;
  }
  .form-group input,
  .form-group select {
    height: 38px;
    padding: 0 11px;
  }
  .form-group input:focus,
  .form-group select:focus,
  .form-group textarea:focus {
    border-color: var(--accent);
    outline: 0;
    box-shadow: 0 0 0 2px var(--accent-glow);
  }
  .form-row {
    display: flex;
    gap: 12px;
  }
  .flex-1 {
    flex: 1;
  }
  .flex-2 {
    flex: 2;
  }
  .field-hint {
    display: block;
    margin-top: 5px;
    color: var(--text-dim);
    font-size: 9px;
  }
  .code-input {
    min-height: 80px;
    padding: 9px 10px;
    resize: vertical;
    color: var(--text-primary);
    font-family: var(--font-mono);
  }
  .test-result {
    margin-top: 4px;
    padding: 9px 11px;
    border: 1px solid;
    border-radius: 6px;
    font-size: 10.5px;
    font-weight: 550;
  }
  .test-result.success {
    border-color: color-mix(in srgb, var(--success), transparent 75%);
    background: var(--success-bg);
    color: var(--success);
  }
  .test-result.error {
    border-color: color-mix(in srgb, var(--failed), transparent 75%);
    background: var(--failed-bg);
    color: var(--failed);
  }
  .modal-actions {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 14px 20px;
    border-top: 1px solid var(--border-subtle);
    background: color-mix(in srgb, var(--bg-primary), transparent 45%);
  }
  .spacer {
    flex: 1;
  }
  @keyframes overlay-in {
    from {
      opacity: 0;
    }
    to {
      opacity: 1;
    }
  }
  @keyframes modal-in {
    from {
      opacity: 0;
      transform: scale(0.96) translateY(8px);
    }
    to {
      opacity: 1;
      transform: scale(1) translateY(0);
    }
  }

  @media (max-width: 768px) {
    .inventory-toolbar {
      grid-template-columns: 1fr;
    }
    .filter-controls {
      justify-content: flex-end;
    }
    .table {
      min-width: 0;
    }
    .table-header {
      display: none;
    }
    .table-row {
      grid-template-columns: minmax(0, 1fr) auto;
      gap: 10px 14px;
      padding: 14px;
    }
    .col-id {
      grid-column: 1;
    }
    .col-actions {
      grid-column: 2;
      grid-row: 1;
    }
    .col-type {
      grid-column: 1;
      grid-row: 2;
    }
    .col-host {
      grid-column: 1;
      grid-row: 3;
    }
    .col-desc {
      grid-column: 2;
      grid-row: 2 / 4;
      max-width: 220px;
      align-self: center;
      white-space: normal;
    }
  }
  @media (max-width: 520px) {
    .status-summary {
      grid-template-columns: 1fr;
    }
    .status-segment {
      min-height: 58px;
      border-right: 0;
      border-bottom: 1px solid var(--border-subtle);
    }
    .status-segment:last-child {
      border-bottom: 0;
    }
    .inventory-heading p {
      display: none;
    }
    .filter-controls label,
    .result-count {
      display: none;
    }
    .filter-controls select {
      width: 100%;
    }
    .table-row {
      grid-template-columns: 1fr auto;
    }
    .col-desc {
      grid-column: 1 / -1;
      grid-row: 4;
      max-width: none;
    }
    .form-row {
      flex-direction: column;
      gap: 0;
    }
    .modal-overlay {
      align-items: flex-end;
      padding: 0;
    }
    .modal {
      width: 100%;
      max-height: 94vh;
      border-radius: 12px 12px 0 0;
    }
    .modal-heading p {
      display: none;
    }
    .modal-actions {
      flex-wrap: wrap;
    }
    .modal-actions .spacer {
      display: none;
    }
    .modal-actions button {
      flex: 1;
    }
    .modal-actions .btn-secondary:first-child {
      flex-basis: 100%;
    }
  }
</style>
