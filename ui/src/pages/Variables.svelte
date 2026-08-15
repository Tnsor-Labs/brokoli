<script lang="ts">
  import { onMount } from "svelte";
  import { notify } from "../lib/toast";
  import { authHeaders } from "../lib/auth";
  import ConfirmDialog from "../components/ConfirmDialog.svelte";
  import Pagination from "../components/Pagination.svelte";
  import Skeleton from "../components/Skeleton.svelte";
  import EmptyState from "../components/EmptyState.svelte";
  import { icons } from "../lib/icons";

  interface Variable {
    key: string;
    value: string;
    type: string;
    description: string;
    created_at: string;
    updated_at: string;
  }

  let variables: Variable[] = [];
  let loading = true;
  let loadError = false;
  let searchQuery = "";
  let typeFilter = "";
  let varPage = 1;
  let varPageSize = 25;

  // Modal
  let showModal = false;
  let editing = false;
  let form: Partial<Variable> = {};

  // Delete
  let confirmDelete = false;
  let deleteTarget = "";

  onMount(async () => {
    await loadVariables();
    loading = false;
  });

  async function loadVariables() {
    loadError = false;
    try {
      const res = await fetch("/api/variables", { headers: authHeaders() });
      if (!res.ok) throw new Error("Variables unavailable");
      const data = await res.json();
      variables = Array.isArray(data) ? data : [];
    } catch {
      loadError = true;
      notify.error("Failed to load variables");
    }
  }

  async function retryLoad() {
    loading = true;
    await loadVariables();
    loading = false;
  }

  function openCreate() {
    editing = false;
    form = { type: "string" };
    showModal = true;
  }

  function openEdit(v: Variable) {
    editing = true;
    form = { ...v };
    showModal = true;
  }

  async function saveVariable() {
    if (!form.key?.trim()) {
      notify.warning("Key is required");
      return;
    }
    try {
      const method = editing ? "PUT" : "POST";
      const url = editing ? `/api/variables/${form.key}` : "/api/variables";
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
      notify.success(editing ? "Variable updated" : "Variable created");
      showModal = false;
      await loadVariables();
    } catch {
      notify.error("Failed to save variable");
    }
  }

  async function deleteVariable(key: string) {
    try {
      await fetch(`/api/variables/${key}`, { method: "DELETE", headers: authHeaders() });
      notify.success("Variable deleted");
      await loadVariables();
    } catch {
      notify.error("Failed to delete");
    }
  }

  function typeColor(type: string): string {
    switch (type) {
      case "string":
        return "var(--node-source-file)";
      case "number":
        return "var(--node-transform)";
      case "json":
        return "var(--node-source-api)";
      case "secret":
        return "var(--failed)";
      default:
        return "var(--text-muted)";
    }
  }

  $: filtered = variables.filter(
    (v) =>
      (!searchQuery ||
        v.key.toLowerCase().includes(searchQuery.toLowerCase()) ||
        v.description.toLowerCase().includes(searchQuery.toLowerCase())) &&
      (!typeFilter || v.type === typeFilter),
  );
  $: paginatedVars = filtered.slice((varPage - 1) * varPageSize, varPage * varPageSize);
  $: secretCount = variables.filter((variable) => variable.type === "secret").length;
  $: structuredCount = variables.filter((variable) => variable.type === "json").length;
  $: if (searchQuery || typeFilter) varPage = 1;
</script>

<div class="variables-page animate-in">
  <header class="identity-hero">
    <div class="hero-main">
      <div class="hero-mark" aria-hidden="true">{"{ }"}</div>
      <div class="header-copy">
        <span class="eyebrow">Organization control center / Runtime configuration</span>
        <h1>Variables</h1>
        <p>Centralize reusable values and protected secrets for every pipeline.</p>
      </div>
      <button class="btn-primary" on:click={openCreate}
        ><svg width="14" height="14" viewBox="0 0 24 24" fill="none"
          ><path
            d={icons.plus.d}
            stroke="currentColor"
            stroke-width="2"
            stroke-linecap="round"
          /></svg
        >New Variable</button
      >
    </div>
    <div class="status-summary" aria-label="Variable summary">
      <div class="status-segment accent">
        <span>Total variables</span><strong>{loading || loadError ? "—" : variables.length}</strong
        ><small>available references</small>
      </div>
      <div class="status-segment secret">
        <span>Protected secrets</span><strong>{loading || loadError ? "—" : secretCount}</strong
        ><small>encrypted and masked</small>
      </div>
      <div class="status-segment">
        <span>JSON values</span><strong>{loading || loadError ? "—" : structuredCount}</strong
        ><small>structured configuration</small>
      </div>
    </div>
  </header>

  <section class="inventory" aria-label="Variable inventory">
    <div class="inventory-heading">
      <div>
        <span class="panel-kicker">Configuration inventory</span>
        <h2>Organization variables</h2>
        <p>Values exposed to pipeline node configuration.</p>
      </div>
      <span class="inventory-count">{loading || loadError ? "—" : variables.length}</span>
    </div>
    <div class="inventory-toolbar">
      <label class="search-bar"
        ><span class="sr-only">Search variables</span><svg
          width="16"
          height="16"
          viewBox="0 0 24 24"
          fill="none"
          ><path
            d={icons.search.d}
            stroke="currentColor"
            stroke-width="1.8"
            stroke-linecap="round"
            stroke-linejoin="round"
          /></svg
        ><input
          type="search"
          bind:value={searchQuery}
          placeholder="Search key or description..."
        /></label
      >
      <div class="filter-controls">
        <label for="variable-type-filter">Type</label>
        <select id="variable-type-filter" bind:value={typeFilter}
          ><option value="">All types</option><option value="string">String</option><option
            value="number">Number</option
          ><option value="json">JSON</option><option value="secret">Secret</option></select
        >
        <span class="result-count">{filtered.length} of {variables.length}</span>
      </div>
    </div>
    <div class="usage-hint">
      <span class="hint-mark">{"{ }"}</span><span
        >Reference any value with <code>{"${var.key_name}"}</code> in a node config field. Secrets stay
        encrypted at rest and masked here.</span
      >
    </div>

    {#if loading}
      <div class="state-intro">
        <span class="state-pulse"></span>
        <div>
          <strong>Loading variable inventory</strong><small
            >Retrieving organization configuration and protected references.</small
          >
        </div>
      </div>
      <div class="skeleton-rows" aria-label="Loading variables">
        {#each Array(4) as _}<Skeleton height="58px" width="100%" />{/each}
      </div>
    {:else if loadError}
      <div class="unavailable-state">
        <span class="state-icon">!</span><strong>Variable inventory unavailable</strong>
        <p>
          The organization configuration service could not be reached. Existing values have not been
          changed.
        </p>
        <button class="btn-secondary" on:click={retryLoad}>Try again</button>
      </div>
    {:else if variables.length === 0}
      <div class="empty-wrap">
        <EmptyState
          icon={icons.variable.d}
          title="No variables configured"
          description="Variables let you store reusable values like file paths, API endpoints, or secrets that pipelines can reference."
          ctaLabel="+ New Variable"
          on:click={openCreate}
        />
      </div>
    {:else if filtered.length === 0}
      <div class="no-results">
        <strong>No matching variables</strong><span>Try a different key, description, or type.</span
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
            <span class="col-key">Variable</span><span class="col-type">Type</span><span
              class="col-value">Stored value</span
            ><span class="col-desc">Description</span><span class="col-actions">Actions</span>
          </div>
          {#each paginatedVars as v}
            <div class="table-row">
              <span class="col-key"
                ><span class="variable-mark" class:secret={v.type === "secret"}
                  >{v.type === "secret" ? "•" : v.key.slice(0, 1).toUpperCase()}</span
                ><span class="identity"
                  ><code>{v.key}</code><small>{"${var." + v.key + "}"}</small></span
                ></span
              >
              <span class="col-type"
                ><span class="type-dot" style="background: {typeColor(v.type)}"
                ></span>{v.type}</span
              >
              <span class="col-value">
                {#if v.type === "secret"}<span class="secret-mask" aria-label="Secret value masked"
                    >••••••••</span
                  >
                {:else if v.type === "json"}<code class="json-val"
                    >{v.value.length > 40 ? v.value.slice(0, 40) + "…" : v.value}</code
                  >
                {:else}<span class="mono"
                    >{v.value.length > 50 ? v.value.slice(0, 50) + "…" : v.value}</span
                  >{/if}
              </span>
              <span class="col-desc">{v.description || "No description"}</span>
              <span class="col-actions">
                <button
                  class="btn-icon"
                  aria-label="Edit {v.key}"
                  title="Edit variable"
                  on:click={() => openEdit(v)}
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
                  aria-label="Delete {v.key}"
                  title="Delete variable"
                  on:click={() => {
                    deleteTarget = v.key;
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
        total={filtered.length}
        page={varPage}
        pageSize={varPageSize}
        on:page={(e) => (varPage = e.detail)}
        on:pagesize={(e) => {
          varPageSize = e.detail;
          varPage = 1;
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
        role="dialog"
        tabindex="-1"
        aria-modal="true"
        aria-labelledby="variable-dialog-title"
        on:click|stopPropagation
        on:keydown={() => {}}
      >
        <header class="modal-header">
          <div class="modal-heading">
            <span class="modal-mark">{editing ? "↗" : "+"}</span>
            <div>
              <h2 id="variable-dialog-title">{editing ? "Edit variable" : "New variable"}</h2>
              <p>
                {editing
                  ? "Update this runtime value and its metadata."
                  : "Create a reusable value for pipeline configuration."}
              </p>
            </div>
          </div>
          <button
            class="modal-close"
            aria-label="Close variable dialog"
            on:click={() => (showModal = false)}>×</button
          >
        </header>
        <div class="modal-body">
          <div class="form-group">
            <label for="variable-key">Key</label>
            <input
              id="variable-key"
              value={form.key || ""}
              on:input={(e) => (form.key = e.currentTarget.value.replace(/[^a-zA-Z0-9._-]/g, ""))}
              placeholder="my_variable"
              disabled={editing}
            />
            {#if !editing}<span class="field-hint"
                >Used as <code>{"${var." + (form.key || "key") + "}"}</code></span
              >{/if}
          </div>

          <div class="form-group">
            <label for="variable-type">Type</label>
            <select
              id="variable-type"
              value={form.type || "string"}
              on:change={(e) => (form.type = e.currentTarget.value)}
            >
              <option value="string">String</option>
              <option value="number">Number</option>
              <option value="json">JSON</option>
              <option value="secret">Secret (encrypted)</option>
            </select>
          </div>

          <div class="form-group">
            <label for="variable-value">Value</label>
            {#if form.type === "secret"}
              <input
                id="variable-value"
                type="password"
                value={form.value || ""}
                on:input={(e) => (form.value = e.currentTarget.value)}
                placeholder={editing ? "Leave blank to keep existing" : "Secret value"}
              />
            {:else if form.type === "json"}
              <textarea
                id="variable-value"
                class="code-input"
                rows="4"
                value={form.value || ""}
                on:input={(e) => (form.value = e.currentTarget.value)}
                placeholder="JSON value"
              ></textarea>
            {:else}
              <input
                id="variable-value"
                value={form.value || ""}
                on:input={(e) => (form.value = e.currentTarget.value)}
                placeholder="Variable value"
              />
            {/if}
          </div>

          <div class="form-group">
            <label for="variable-description">Description <span>Optional</span></label>
            <input
              id="variable-description"
              value={form.description || ""}
              on:input={(e) => (form.description = e.currentTarget.value)}
              placeholder="What this variable is used for"
            />
          </div>
        </div>
        <div class="modal-actions">
          <button class="btn-secondary" on:click={() => (showModal = false)}>Cancel</button>
          <button class="btn-primary" on:click={saveVariable}
            >{editing ? "Update" : "Create"}</button
          >
        </div>
      </div>
    </div>
  {/if}
</div>

<ConfirmDialog
  bind:visible={confirmDelete}
  title="Delete Variable"
  message="Are you sure? Any pipelines using this variable will get empty values."
  confirmLabel="Delete"
  destructive={true}
  on:confirm={() => deleteVariable(deleteTarget)}
/>

<style>
  .variables-page {
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
    font: 700 12px var(--font-mono);
  }
  .hero-main .header-copy {
    flex: 1;
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
  .status-segment.secret::before {
    background: var(--failed);
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
    min-height: 360px;
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
  .usage-hint {
    font-size: 12px;
    color: var(--text-muted);
    padding: 10px 14px;
    background: var(--accent-glow);
    border: 1px solid rgba(99, 102, 241, 0.15);
    border-radius: var(--radius-md);
    margin-bottom: var(--space-md);
  }
  .usage-hint code {
    font-family: var(--font-mono);
    font-size: 11px;
    background: transparent;
    padding: 1px 2px;
    color: var(--accent-text);
    font-weight: 600;
  }

  .search-bar {
    display: flex;
    align-items: center;
    gap: var(--space-sm);
    padding: var(--space-sm) var(--space-md);
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    margin-bottom: var(--space-md);
    color: var(--text-muted);
  }

  .skeleton-rows {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .table {
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius-xl, 14px);
    overflow: hidden;
    box-shadow: var(--shadow-card);
  }
  .table-header,
  .table-row {
    display: flex;
    align-items: center;
    padding: 10px 16px;
    gap: 12px;
  }
  .table-header {
    background: transparent;
    font-size: 11px;
    font-weight: 600;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.06em;
    border-bottom: 2px solid var(--border-subtle);
  }
  .table-row {
    border-bottom: 1px solid var(--border-subtle);
    transition: background 150ms ease;
  }
  .table-row:last-child {
    border-bottom: none;
  }
  .table-row:hover {
    background: rgba(255, 255, 255, 0.02);
  }

  .col-key {
    flex: 1.5;
  }
  .col-type {
    flex: 0.8;
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 12px;
    color: var(--text-secondary);
  }
  .col-value {
    flex: 2;
    font-size: 12px;
  }
  .col-desc {
    flex: 1.5;
    color: var(--text-muted);
    font-size: 12px;
  }
  .col-actions {
    flex: 0.5;
    display: flex;
    gap: 4px;
    justify-content: flex-end;
  }

  .type-dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    flex-shrink: 0;
  }
  .mono {
    font-family: var(--font-mono);
    font-size: 11px;
  }
  .secret-mask {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--text-dim);
  }
  .json-val {
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--text-secondary);
  }

  .btn-primary {
    padding: 8px 16px;
    border-radius: 6px;
    font-size: 13px;
    font-weight: 500;
    background: var(--accent);
    border: 1px solid var(--accent);
    color: white;
    transition: all 150ms ease;
  }
  .btn-primary:hover {
    background: var(--accent-hover);
  }
  .btn-secondary {
    padding: 8px 16px;
    border-radius: 6px;
    font-size: 13px;
    font-weight: 500;
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    color: var(--text-secondary);
    transition: all 150ms ease;
  }
  .btn-secondary:hover {
    background: var(--bg-tertiary);
    color: var(--text-primary);
  }

  .btn-icon {
    width: 28px;
    height: 28px;
    display: flex;
    align-items: center;
    justify-content: center;
    border-radius: 4px;
    color: var(--text-muted);
    transition: all 150ms ease;
  }
  .btn-icon:hover {
    color: var(--text-primary);
    background: var(--bg-tertiary);
  }
  .btn-icon.danger:hover {
    color: var(--failed);
    background: var(--failed-bg);
  }

  .modal-overlay {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.7);
    backdrop-filter: blur(4px);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 100;
    animation: overlay-in 150ms ease;
  }
  @keyframes overlay-in {
    from {
      opacity: 0;
    }
    to {
      opacity: 1;
    }
  }
  .modal {
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    border-radius: var(--radius-xl, 14px);
    padding: 28px 32px;
    width: 500px;
    max-width: 90vw;
    box-shadow: 0 16px 48px rgba(0, 0, 0, 0.4);
    animation: modal-in 200ms cubic-bezier(0.16, 1, 0.3, 1);
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
  .modal h2 {
    font-size: 1.2rem;
    font-weight: 600;
    margin-bottom: 20px;
    letter-spacing: -0.01em;
  }

  .form-group {
    margin-bottom: 16px;
  }
  .form-group label {
    display: block;
    font-size: 11px;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.06em;
    margin-bottom: 6px;
    font-weight: 500;
  }
  .form-group input,
  .form-group select,
  .form-group textarea {
    width: 100%;
  }
  .form-group select {
    padding: 9px var(--space-md);
    background: var(--bg-secondary);
    color: var(--text-primary);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    font-family: var(--font-ui);
    font-size: 0.875rem;
  }
  .field-hint {
    font-size: 10.5px;
    color: var(--text-dim);
    margin-top: 4px;
    display: block;
  }
  .field-hint code {
    font-family: var(--font-mono);
    font-size: 10px;
    background: var(--bg-code);
    padding: 1px 4px;
    border-radius: 3px;
    color: var(--accent-text);
  }

  .code-input {
    font-family: var(--font-mono);
    font-size: 11px;
    background: var(--bg-code);
    color: var(--text-primary);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    padding: 8px 10px;
    resize: vertical;
  }

  .modal-actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--space-sm);
    margin-top: var(--space-lg);
  }

  /* Refined configuration inventory */
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
    color: var(--text-primary);
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
    gap: 7px;
    padding: 0 14px;
    border: 1px solid var(--accent);
    border-radius: 6px;
    background: var(--accent);
    color: white;
    font-size: 11px;
    font-weight: 550;
    transition: all 150ms ease;
  }
  .btn-secondary {
    border-color: var(--border);
    background: var(--bg-secondary);
    color: var(--text-secondary);
  }

  .inventory {
    display: flex;
    min-height: 520px;
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
    margin: 0;
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
  .usage-hint {
    display: flex;
    align-items: center;
    gap: 10px;
    margin: 0;
    padding: 9px 12px;
    border: 0;
    border-bottom: 1px solid var(--border-subtle);
    border-radius: 0;
    background: color-mix(in srgb, var(--accent-glow), transparent 35%);
    color: var(--text-muted);
    font-size: 10px;
  }
  .hint-mark {
    display: grid;
    width: 26px;
    height: 26px;
    flex: none;
    place-items: center;
    border: 1px solid color-mix(in srgb, var(--accent), transparent 70%);
    border-radius: 6px;
    color: var(--accent);
    font: 650 9px var(--font-mono);
  }
  .usage-hint code {
    color: var(--accent-text);
    font-size: 9.5px;
  }

  .table-scroll {
    overflow-x: auto;
  }
  .table {
    min-width: 800px;
    overflow: visible;
    border: 0;
    border-radius: 0;
    box-shadow: none;
  }
  .table-header,
  .table-row {
    display: grid;
    grid-template-columns: 1.35fr 0.65fr 1.35fr 1.4fr 72px;
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
  }
  .table-row:hover {
    background: var(--bg-card-hover);
  }
  .col-key {
    display: flex;
    min-width: 0;
    align-items: center;
    gap: 10px;
  }
  .variable-mark {
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
  .variable-mark.secret {
    border-color: color-mix(in srgb, var(--failed), transparent 72%);
    background: var(--failed-bg);
    color: var(--failed);
  }
  .identity {
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
  .identity small {
    overflow: hidden;
    color: var(--text-dim);
    font: 8.5px var(--font-mono);
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .col-type {
    font-size: 10px;
    text-transform: capitalize;
  }
  .type-dot {
    width: 6px;
    height: 6px;
  }
  .col-value {
    min-width: 0;
    overflow: hidden;
    font-size: 10.5px;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .col-value .mono,
  .json-val {
    padding: 0;
    background: transparent;
    color: var(--text-secondary);
    font: 10px var(--font-mono);
  }
  .secret-mask {
    color: var(--text-dim);
    font: 11px var(--font-mono);
    letter-spacing: 0.14em;
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
  .btn-icon {
    display: grid;
    width: 29px;
    height: 29px;
    place-items: center;
    border-radius: 5px;
  }
  .skeleton-rows {
    padding: 14px;
  }
  .empty-wrap {
    display: grid;
    min-height: 390px;
    place-items: center;
    padding: 30px;
  }
  .no-results {
    display: flex;
    min-height: 340px;
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
    padding: 16px;
    background: rgba(0, 4, 6, 0.78);
    backdrop-filter: blur(8px) saturate(0.8);
  }
  .modal {
    width: min(540px, 100%);
    max-width: none;
    max-height: calc(100vh - 32px);
    padding: 0;
    overflow: hidden;
    border-color: color-mix(in srgb, var(--border), white 8%);
    border-radius: 12px;
    box-shadow:
      0 32px 90px rgba(0, 0, 0, 0.62),
      inset 0 1px 0 rgba(255, 255, 255, 0.035);
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
  .modal .modal-heading h2 {
    margin: 0;
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
  .modal .form-group {
    margin-bottom: 14px;
  }
  .modal .form-group:last-child {
    margin-bottom: 0;
  }
  .modal .form-group label {
    display: flex;
    justify-content: space-between;
    margin-bottom: 7px;
    color: var(--text-secondary);
    font-size: 10px;
    font-weight: 550;
    letter-spacing: 0;
    text-transform: none;
  }
  .modal .form-group label span {
    color: var(--text-dim);
    font-size: 8.5px;
    font-weight: 400;
  }
  .modal .form-group input,
  .modal .form-group select,
  .modal .form-group textarea {
    width: 100%;
    border-color: var(--border);
    border-radius: 6px;
    background: var(--bg-primary);
    font-size: 11px;
  }
  .modal .form-group input,
  .modal .form-group select {
    height: 38px;
    padding: 0 11px;
  }
  .modal .form-group input:focus,
  .modal .form-group select:focus,
  .modal .form-group textarea:focus {
    border-color: var(--accent);
    outline: 0;
    box-shadow: 0 0 0 2px var(--accent-glow);
  }
  .modal .code-input {
    min-height: 90px;
    padding: 9px 10px;
    resize: vertical;
    color: var(--text-primary);
    font-family: var(--font-mono);
  }
  .modal .modal-actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    margin: 0;
    padding: 14px 20px;
    border-top: 1px solid var(--border-subtle);
    background: color-mix(in srgb, var(--bg-primary), transparent 45%);
  }

  @media (max-width: 768px) {
    .hero-main {
      align-items: flex-start;
    }
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
    .col-key {
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
    .col-value {
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
    .usage-hint {
      align-items: flex-start;
    }
    .table-row {
      grid-template-columns: 1fr auto;
    }
    .col-desc {
      grid-column: 1 / -1;
      grid-row: 4;
      max-width: none;
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
    .modal-actions button {
      flex: 1;
    }
  }
</style>
