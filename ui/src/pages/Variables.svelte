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
  let searchQuery = "";
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
    try {
      const res = await fetch("/api/variables", { headers: authHeaders() });
      variables = await res.json();
    } catch {
      notify.error("Failed to load variables");
    }
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
    if (!form.key?.trim()) { notify.warning("Key is required"); return; }
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
      case "string": return "var(--node-source-file)";
      case "number": return "var(--node-transform)";
      case "json": return "var(--node-source-api)";
      case "secret": return "var(--failed)";
      default: return "var(--text-muted)";
    }
  }

  $: filtered = variables.filter(v =>
    !searchQuery ||
    v.key.toLowerCase().includes(searchQuery.toLowerCase()) ||
    v.description.toLowerCase().includes(searchQuery.toLowerCase())
  );
  $: paginatedVars = filtered.slice((varPage - 1) * varPageSize, varPage * varPageSize);
  $: if (searchQuery) varPage = 1;
</script>

<div class="variables-page animate-in">
  <header class="page-header">
    <div class="header-left">
      <h1>Variables</h1>
      <span class="meta">{variables.length} variables</span>
    </div>
    <button class="btn-primary" on:click={openCreate}>+ New Variable</button>
  </header>

  <div class="usage-hint">
    Use <code>{"${var.key_name}"}</code> in any node config field to reference a variable.
    Secret variables are encrypted at rest and masked in the UI.
  </div>

  <div class="search-bar">
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none">
      <path d="M11 3a8 8 0 1 0 0 16 8 8 0 0 0 0-16zM21 21l-4.35-4.35" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
    </svg>
    <input type="text" class="search-input" bind:value={searchQuery} placeholder="Search variables..." />
  </div>

  {#if loading}
    <div class="skeleton-rows">
      {#each Array(4) as _}
        <Skeleton height="48px" width="100%" />
      {/each}
    </div>
  {:else if variables.length === 0}
    <EmptyState
      icon={icons.variable.d}
      title="No variables configured"
      description="Variables let you store reusable values like file paths, API endpoints, or secrets that pipelines can reference."
      ctaLabel="+ New Variable"
      on:click={() => (showCreateModal = true)}
    />
  {:else}
    <div class="table">
      <div class="table-header">
        <span class="col-key">Key</span>
        <span class="col-type">Type</span>
        <span class="col-value">Value</span>
        <span class="col-desc">Description</span>
        <span class="col-actions">Actions</span>
      </div>
      {#each paginatedVars as v}
        <div class="table-row">
          <span class="col-key"><code class="key-badge">{v.key}</code></span>
          <span class="col-type">
            <span class="type-dot" style="background: {typeColor(v.type)}"></span>
            {v.type}
          </span>
          <span class="col-value">
            {#if v.type === "secret"}
              <span class="secret-mask">********</span>
            {:else if v.type === "json"}
              <code class="json-val">{v.value.length > 40 ? v.value.slice(0, 40) + "…" : v.value}</code>
            {:else}
              <span class="mono">{v.value.length > 50 ? v.value.slice(0, 50) + "…" : v.value}</span>
            {/if}
          </span>
          <span class="col-desc">{v.description || "—"}</span>
          <span class="col-actions">
            <button class="btn-icon" title="Edit" on:click={() => openEdit(v)}>
              <svg width="12" height="12" viewBox="0 0 24 24" fill="none"><path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7M18.5 2.5a2.12 2.12 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" /></svg>
            </button>
            <button class="btn-icon danger" title="Delete" on:click={() => { deleteTarget = v.key; confirmDelete = true; }}>
              <svg width="12" height="12" viewBox="0 0 24 24" fill="none"><path d="M3 6h18M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2M10 11v6M14 11v6" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" /></svg>
            </button>
          </span>
        </div>
      {/each}
    </div>
    <Pagination total={filtered.length} page={varPage} pageSize={varPageSize}
      on:page={(e) => varPage = e.detail} on:pagesize={(e) => { varPageSize = e.detail; varPage = 1; }} />
  {/if}

  {#if showModal}
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <div class="modal-overlay" on:click={() => showModal = false} on:keydown={() => {}}>
      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <div class="modal" on:click|stopPropagation on:keydown={() => {}}>
        <h2>{editing ? "Edit Variable" : "New Variable"}</h2>

        <div class="form-group">
          <label>Key</label>
          <input value={form.key || ""} on:input={(e) => form.key = e.currentTarget.value.replace(/[^a-zA-Z0-9._-]/g, "")}
            placeholder="my_variable" disabled={editing} />
          {#if !editing}<span class="field-hint">Used as <code>{"${var." + (form.key || "key") + "}"}</code></span>{/if}
        </div>

        <div class="form-group">
          <label>Type</label>
          <select value={form.type || "string"} on:change={(e) => form.type = e.currentTarget.value}>
            <option value="string">String</option>
            <option value="number">Number</option>
            <option value="json">JSON</option>
            <option value="secret">Secret (encrypted)</option>
          </select>
        </div>

        <div class="form-group">
          <label>Value</label>
          {#if form.type === "secret"}
            <input type="password" value={form.value || ""} on:input={(e) => form.value = e.currentTarget.value}
              placeholder={editing ? "Leave blank to keep existing" : "Secret value"} />
          {:else if form.type === "json"}
            <textarea class="code-input" rows="4" value={form.value || ""} on:input={(e) => form.value = e.currentTarget.value}
              placeholder="JSON value"></textarea>
          {:else}
            <input value={form.value || ""} on:input={(e) => form.value = e.currentTarget.value}
              placeholder="Variable value" />
          {/if}
        </div>

        <div class="form-group">
          <label>Description</label>
          <input value={form.description || ""} on:input={(e) => form.description = e.currentTarget.value} placeholder="What this variable is used for" />
        </div>

        <div class="modal-actions">
          <button class="btn-secondary" on:click={() => showModal = false}>Cancel</button>
          <button class="btn-primary" on:click={saveVariable}>{editing ? "Update" : "Create"}</button>
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
  .page-header {
    display: flex; justify-content: space-between; align-items: center;
    margin-bottom: var(--space-md);
  }
  .header-left { display: flex; align-items: baseline; gap: 12px; }
  .page-header h1 { font-size: 1.5rem; font-weight: 600; letter-spacing: -0.02em; }
  .meta { font-size: 0.8125rem; color: var(--text-muted); font-family: var(--font-mono); }

  .usage-hint {
    font-size: 12px; color: var(--text-muted); padding: 10px 14px;
    background: var(--accent-glow); border: 1px solid rgba(99,102,241,0.15);
    border-radius: var(--radius-md); margin-bottom: var(--space-md);
  }
  .usage-hint code {
    font-family: var(--font-mono); font-size: 11px;
    background: transparent; padding: 1px 2px;
    color: var(--accent-text); font-weight: 600;
  }

  .empty-state {
    background: var(--bg-secondary); border: 1px solid var(--border);
    border-radius: var(--radius-lg); padding: var(--space-xl);
    text-align: center; color: var(--text-secondary);
  }
  .hint { color: var(--text-muted); font-size: 0.875rem; margin-top: var(--space-xs); }

  .search-bar {
    display: flex; align-items: center; gap: var(--space-sm);
    padding: var(--space-sm) var(--space-md);
    background: var(--bg-secondary); border: 1px solid var(--border);
    border-radius: var(--radius-lg); margin-bottom: var(--space-md);
    color: var(--text-muted);
  }
  .search-input {
    flex: 1; border: none; background: transparent; font-size: 0.875rem;
    color: var(--text-primary); outline: none;
  }

  .skeleton-rows { display: flex; flex-direction: column; gap: 8px; }
  .table { border: 1px solid var(--border-subtle); border-radius: var(--radius-xl, 14px); overflow: hidden; box-shadow: var(--shadow-card); }
  .table-header, .table-row {
    display: flex; align-items: center; padding: 10px 16px; gap: 12px;
  }
  .table-header {
    background: transparent;
    font-size: 11px; font-weight: 600;
    color: var(--text-muted); text-transform: uppercase; letter-spacing: 0.06em;
    border-bottom: 2px solid var(--border-subtle);
  }
  .table-row { border-bottom: 1px solid var(--border-subtle); transition: background 150ms ease; }
  .table-row:last-child { border-bottom: none; }
  .table-row:hover { background: rgba(255, 255, 255, 0.02); }

  .col-key { flex: 1.5; }
  .col-type { flex: 0.8; display: flex; align-items: center; gap: 6px; font-size: 12px; color: var(--text-secondary); }
  .col-value { flex: 2; font-size: 12px; }
  .col-desc { flex: 1.5; color: var(--text-muted); font-size: 12px; }
  .col-actions { flex: 0.5; display: flex; gap: 4px; justify-content: flex-end; }

  .key-badge {
    font-family: var(--font-mono); font-size: 12px; font-weight: 600;
    color: var(--accent); background: none;
    padding: 0;
  }
  .type-dot { width: 6px; height: 6px; border-radius: 50%; flex-shrink: 0; }
  .mono { font-family: var(--font-mono); font-size: 11px; }
  .secret-mask { font-family: var(--font-mono); font-size: 11px; color: var(--text-dim); }
  .json-val { font-family: var(--font-mono); font-size: 10px; color: var(--text-secondary); }

  .btn-primary {
    padding: 8px 16px; border-radius: 6px; font-size: 13px; font-weight: 500;
    background: var(--accent); border: 1px solid var(--accent); color: white;
    transition: all 150ms ease;
  }
  .btn-primary:hover { background: var(--accent-hover); }
  .btn-secondary {
    padding: 8px 16px; border-radius: 6px; font-size: 13px; font-weight: 500;
    background: var(--bg-secondary); border: 1px solid var(--border); color: var(--text-secondary);
    transition: all 150ms ease;
  }
  .btn-secondary:hover { background: var(--bg-tertiary); color: var(--text-primary); }

  .btn-icon {
    width: 28px; height: 28px; display: flex; align-items: center; justify-content: center;
    border-radius: 4px; color: var(--text-muted); transition: all 150ms ease;
  }
  .btn-icon:hover { color: var(--text-primary); background: var(--bg-tertiary); }
  .btn-icon.danger:hover { color: var(--failed); background: var(--failed-bg); }

  .modal-overlay {
    position: fixed; inset: 0;
    background: rgba(0, 0, 0, 0.7); backdrop-filter: blur(4px);
    display: flex; align-items: center; justify-content: center; z-index: 100;
    animation: overlay-in 150ms ease;
  }
  @keyframes overlay-in { from { opacity: 0; } to { opacity: 1; } }
  .modal {
    background: var(--bg-secondary); border: 1px solid var(--border);
    border-radius: var(--radius-xl, 14px); padding: 28px 32px;
    width: 500px; max-width: 90vw;
    box-shadow: 0 16px 48px rgba(0, 0, 0, 0.4);
    animation: modal-in 200ms cubic-bezier(0.16, 1, 0.3, 1);
  }
  @keyframes modal-in {
    from { opacity: 0; transform: scale(0.96) translateY(8px); }
    to { opacity: 1; transform: scale(1) translateY(0); }
  }
  .modal h2 { font-size: 1.2rem; font-weight: 600; margin-bottom: 20px; letter-spacing: -0.01em; }

  .form-group { margin-bottom: 16px; }
  .form-group label {
    display: block; font-size: 11px; color: var(--text-muted);
    text-transform: uppercase; letter-spacing: 0.06em; margin-bottom: 6px; font-weight: 500;
  }
  .form-group input, .form-group select, .form-group textarea { width: 100%; }
  .form-group select {
    padding: 9px var(--space-md); background: var(--bg-secondary);
    color: var(--text-primary); border: 1px solid var(--border);
    border-radius: var(--radius-md); font-family: var(--font-ui); font-size: 0.875rem;
  }
  .field-hint { font-size: 10.5px; color: var(--text-dim); margin-top: 4px; display: block; }
  .field-hint code {
    font-family: var(--font-mono); font-size: 10px;
    background: var(--bg-code); padding: 1px 4px; border-radius: 3px; color: var(--accent-text);
  }

  .code-input {
    font-family: var(--font-mono); font-size: 11px;
    background: var(--bg-code); color: var(--text-primary);
    border: 1px solid var(--border); border-radius: var(--radius-md);
    padding: 8px 10px; resize: vertical;
  }

  .modal-actions {
    display: flex; justify-content: flex-end; gap: var(--space-sm); margin-top: var(--space-lg);
  }
</style>
