<script lang="ts">
  import { onMount } from "svelte";
  import { notify } from "../lib/toast";
  import { authHeaders } from "../lib/auth";
  import ConfirmDialog from "../components/ConfirmDialog.svelte";
  import Pagination from "../components/Pagination.svelte";

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
    created_at: string;
    updated_at: string;
  }

  interface ConnType {
    type: string;
    label: string;
    fields: string[];
  }

  let connections: Connection[] = [];
  let connPage = 1;
  let connPageSize = 25;
  let connTypes: ConnType[] = [];
  let loading = true;

  // Modal state
  let showModal = false;
  let editing = false;
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
    try {
      const res = await fetch("/api/connections", { headers: authHeaders() });
      connections = await res.json();
    } catch {
      notify.error("Failed to load connections");
    }
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
    form = { type: "postgres", port: 5432 };
    testResult = null;
    showModal = true;
  }

  function openEdit(c: Connection) {
    editing = true;
    form = { ...c };
    testResult = null;
    showModal = true;
  }

  async function saveConnection() {
    if (!form.conn_id?.trim()) { notify.warning("Connection ID is required"); return; }
    if (!form.type) { notify.warning("Type is required"); return; }

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
    return connTypes.find(t => t.type === type)?.label || type;
  }

  function typeFields(type: string): string[] {
    return connTypes.find(t => t.type === type)?.fields || [];
  }

  // Default ports per type
  function defaultPort(type: string): number {
    switch (type) {
      case "postgres": return 5432;
      case "mysql": return 3306;
      case "http": return 443;
      case "sftp": return 22;
      default: return 0;
    }
  }

  function onTypeChange(type: string) {
    form.type = type;
    form.port = defaultPort(type);
  }
</script>

<div class="connections-page animate-in">
  <header class="page-header">
    <h1>Connections</h1>
    <button class="btn-primary" on:click={openCreate}>+ New Connection</button>
  </header>

  {#if loading}
    <div class="empty-state">Loading...</div>
  {:else if connections.length === 0}
    <div class="empty-state">
      <p>No connections configured.</p>
      <p class="hint">Connections store credentials for databases, APIs, and other external services.</p>
    </div>
  {:else}
    <div class="table">
      <div class="table-header">
        <span class="col-id">Connection ID</span>
        <span class="col-type">Type</span>
        <span class="col-host">Host</span>
        <span class="col-desc">Description</span>
        <span class="col-actions">Actions</span>
      </div>
      {#each connections.slice((connPage - 1) * connPageSize, connPage * connPageSize) as conn}
        <div class="table-row">
          <span class="col-id"><code class="conn-id-badge">{conn.conn_id}</code></span>
          <span class="col-type"><span class="type-badge">{typeLabel(conn.type)}</span></span>
          <span class="col-host mono">{conn.host || "—"}{conn.port ? `:${conn.port}` : ""}</span>
          <span class="col-desc">{conn.description || "—"}</span>
          <span class="col-actions">
            <button class="btn-icon" title="Edit" on:click={() => openEdit(conn)}>
              <svg width="12" height="12" viewBox="0 0 24 24" fill="none"><path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7M18.5 2.5a2.12 2.12 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" /></svg>
            </button>
            <button class="btn-icon danger" title="Delete" on:click={() => { deleteTarget = conn.conn_id; confirmDelete = true; }}>
              <svg width="12" height="12" viewBox="0 0 24 24" fill="none"><path d="M3 6h18M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2M10 11v6M14 11v6" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" /></svg>
            </button>
          </span>
        </div>
      {/each}
    </div>
    <Pagination total={connections.length} page={connPage} pageSize={connPageSize}
      on:page={(e) => connPage = e.detail} on:pagesize={(e) => { connPageSize = e.detail; connPage = 1; }} />
  {/if}

  {#if showModal}
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <div class="modal-overlay" on:click={() => showModal = false} on:keydown={() => {}}>
      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <div class="modal" on:click|stopPropagation on:keydown={() => {}}>
        <h2>{editing ? "Edit Connection" : "New Connection"}</h2>

        <div class="form-group">
          <label>Connection ID</label>
          <input value={form.conn_id || ""} on:input={(e) => form.conn_id = e.currentTarget.value.toLowerCase().replace(/[^a-z0-9_-]/g, "")}
            placeholder="my_postgres" disabled={editing} />
          {#if !editing}<span class="field-hint">Lowercase, hyphens, underscores only</span>{/if}
        </div>

        <div class="form-group">
          <label>Type</label>
          <select value={form.type || "postgres"} on:change={(e) => onTypeChange(e.currentTarget.value)} disabled={editing}>
            {#each connTypes as ct}
              <option value={ct.type}>{ct.label}</option>
            {/each}
          </select>
        </div>

        <div class="form-group">
          <label>Description</label>
          <input value={form.description || ""} on:input={(e) => form.description = e.currentTarget.value} placeholder="Production database" />
        </div>

        {#if typeFields(form.type || "").includes("host")}
          <div class="form-row">
            <div class="form-group flex-2">
              <label>Host</label>
              <input value={form.host || ""} on:input={(e) => form.host = e.currentTarget.value} placeholder="localhost" />
            </div>
            {#if typeFields(form.type || "").includes("port")}
              <div class="form-group flex-1">
                <label>Port</label>
                <input type="number" value={form.port || 0} on:input={(e) => form.port = Number(e.currentTarget.value)} />
              </div>
            {/if}
          </div>
        {/if}

        {#if typeFields(form.type || "").includes("schema")}
          <div class="form-group">
            <label>Database / Schema</label>
            <input value={form.schema || ""} on:input={(e) => form.schema = e.currentTarget.value} placeholder="mydb" />
          </div>
        {/if}

        {#if typeFields(form.type || "").includes("login")}
          <div class="form-row">
            <div class="form-group flex-1">
              <label>Login / Username</label>
              <input value={form.login || ""} on:input={(e) => form.login = e.currentTarget.value} placeholder="admin" />
            </div>
            {#if typeFields(form.type || "").includes("password")}
              <div class="form-group flex-1">
                <label>Password</label>
                <input type="password" value={form.password || ""} on:input={(e) => form.password = e.currentTarget.value} placeholder={editing ? "Leave blank to keep" : "password"} />
              </div>
            {/if}
          </div>
        {/if}

        {#if typeFields(form.type || "").includes("extra")}
          <div class="form-group">
            <label>Extra (JSON)</label>
            <textarea class="code-input" rows="3" value={form.extra || ""} on:input={(e) => form.extra = e.currentTarget.value}
              placeholder="JSON extra fields"></textarea>
            <span class="field-hint">Type-specific fields: headers, tokens, bucket, region, etc.</span>
          </div>
        {/if}

        {#if testResult}
          <div class="test-result" class:success={testResult.success} class:error={!testResult.success}>
            {testResult.success ? "Connection successful" : testResult.error || "Connection failed"}
          </div>
        {/if}

        <div class="modal-actions">
          {#if editing}
            <button class="btn-secondary" on:click={testConnection} disabled={testing}>
              {testing ? "Testing..." : "Test Connection"}
            </button>
          {/if}
          <span class="spacer"></span>
          <button class="btn-secondary" on:click={() => showModal = false}>Cancel</button>
          <button class="btn-primary" on:click={saveConnection}>
            {editing ? "Update" : "Create"}
          </button>
        </div>
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
  .page-header {
    display: flex; justify-content: space-between; align-items: center;
    margin-bottom: var(--space-xl);
  }
  .page-header h1 { font-size: 1.5rem; font-weight: 600; letter-spacing: -0.02em; }

  .empty-state {
    background: var(--bg-secondary); border: 1px solid var(--border);
    border-radius: var(--radius-lg); padding: var(--space-xl);
    text-align: center; color: var(--text-secondary);
  }
  .hint { color: var(--text-muted); font-size: 0.875rem; margin-top: var(--space-xs); }

  .table { border: 1px solid var(--border); border-radius: var(--radius-lg); overflow: hidden; }
  .table-header, .table-row {
    display: flex; align-items: center; padding: 10px 16px; gap: 12px;
  }
  .table-header {
    background: var(--bg-secondary); font-size: 10px; font-weight: 600;
    color: var(--text-muted); text-transform: uppercase; letter-spacing: 0.08em;
    border-bottom: 1px solid var(--border);
  }
  .table-row { border-bottom: 1px solid var(--border-subtle); transition: background 150ms ease; }
  .table-row:last-child { border-bottom: none; }
  .table-row:hover { background: var(--bg-secondary); }

  .col-id { flex: 1.5; }
  .col-type { flex: 1; }
  .col-host { flex: 1.5; font-size: 12px; }
  .col-desc { flex: 2; color: var(--text-muted); font-size: 13px; }
  .col-actions { flex: 0.5; display: flex; gap: 4px; justify-content: flex-end; }

  .conn-id-badge {
    font-family: var(--font-mono); font-size: 12px; font-weight: 600;
    color: var(--accent-text); background: var(--accent-glow);
    padding: 2px 8px; border-radius: 4px;
  }
  .type-badge {
    font-size: 11px; font-weight: 500; color: var(--text-secondary);
    background: var(--bg-tertiary); padding: 2px 8px; border-radius: 4px;
  }
  .mono { font-family: var(--font-mono); }

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

  /* Modal */
  .modal-overlay {
    position: fixed; inset: 0; background: var(--bg-overlay);
    display: flex; align-items: center; justify-content: center; z-index: 100;
  }
  .modal {
    background: var(--bg-secondary); border: 1px solid var(--border);
    border-radius: var(--radius-lg); padding: var(--space-xl);
    width: 520px; max-width: 90vw; max-height: 85vh; overflow-y: auto;
  }
  .modal h2 { font-size: 1.125rem; margin-bottom: var(--space-lg); }

  .form-group { margin-bottom: var(--space-md); }
  .form-group label {
    display: block; font-size: 0.6875rem; color: var(--text-muted);
    text-transform: uppercase; letter-spacing: 0.08em; margin-bottom: var(--space-xs);
  }
  .form-group input, .form-group select, .form-group textarea { width: 100%; }
  .form-group select {
    padding: var(--space-sm) var(--space-md); background: var(--bg-input);
    color: var(--text-primary); border: 1px solid var(--border);
    border-radius: var(--radius-md); font-family: var(--font-ui); font-size: 0.875rem;
  }
  .form-row { display: flex; gap: var(--space-sm); }
  .flex-1 { flex: 1; }
  .flex-2 { flex: 2; }
  .field-hint { font-size: 10px; color: var(--text-dim); margin-top: 2px; display: block; }

  .code-input {
    font-family: var(--font-mono); font-size: 11px;
    background: var(--bg-code); color: var(--text-primary);
    border: 1px solid var(--border); border-radius: var(--radius-md);
    padding: 8px 10px; resize: vertical;
  }

  .test-result {
    padding: 8px 12px; border-radius: 6px; font-size: 12px; font-weight: 500; margin-bottom: 12px;
  }
  .test-result.success { background: var(--success-bg); color: var(--success); border: 1px solid rgba(34,197,94,0.2); }
  .test-result.error { background: var(--failed-bg); color: var(--failed); border: 1px solid rgba(239,68,68,0.2); }

  .modal-actions {
    display: flex; gap: var(--space-sm); margin-top: var(--space-lg); align-items: center;
  }
  .spacer { flex: 1; }
</style>
