<script lang="ts">
  import { onMount } from "svelte";
  import { notify } from "../lib/toast";
  import { authHeaders, authUser } from "../lib/auth";

  interface UserInfo { id: string; username: string; role: string; created_at: string; }

  let generatedKey = "";
  let generating = false;
  let copied = false;
  let sysInfo: { version: string; db_size_mb: string; pipelines: number } | null = null;
  let purging = false;
  let purgeDays = 30;
  let users: UserInfo[] = [];
  let newUsername = "";
  let newPassword = "";
  let newRole = "editor";
  let creatingUser = false;

  onMount(async () => {
    try {
      const res = await fetch("/api/system/info", { headers: authHeaders() });
      sysInfo = await res.json();
    } catch { /* ignore */ }
    loadUsers();
  });

  async function loadUsers() {
    try {
      const res = await fetch("/api/auth/users", { headers: authHeaders() });
      if (res.ok) users = await res.json();
    } catch { /* ignore */ }
  }

  async function createUser() {
    if (!newUsername.trim() || !newPassword.trim()) {
      notify.warning("Username and password required");
      return;
    }
    creatingUser = true;
    try {
      const res = await fetch("/api/auth/users", {
        method: "POST",
        headers: { "Content-Type": "application/json", ...authHeaders() },
        body: JSON.stringify({ username: newUsername, password: newPassword, role: newRole }),
      });
      if (!res.ok) {
        const data = await res.json();
        notify.error(data.error || "Failed to create user");
      } else {
        notify.success(`User '${newUsername}' created`);
        newUsername = "";
        newPassword = "";
        await loadUsers();
      }
    } catch {
      notify.error("Failed to create user");
    } finally {
      creatingUser = false;
    }
  }

  async function purgeRuns() {
    purging = true;
    try {
      const res = await fetch("/api/system/purge", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ days: purgeDays }),
      });
      const data = await res.json();
      notify.success(`Purged ${data.deleted} old runs`);
      // Refresh info
      const infoRes = await fetch("/api/system/info");
      sysInfo = await infoRes.json();
    } catch {
      notify.error("Purge failed");
    } finally {
      purging = false;
    }
  }

  async function generateKey() {
    generating = true;
    try {
      // This generates a key client-side for display purposes
      // In production, this would call a backend endpoint
      const arr = new Uint8Array(24);
      crypto.getRandomValues(arr);
      const hex = Array.from(arr).map(b => b.toString(16).padStart(2, "0")).join("");
      generatedKey = "brk_" + hex;
    } finally {
      generating = false;
    }
  }

  async function copyKey() {
    if (!generatedKey) return;
    await navigator.clipboard.writeText(generatedKey);
    copied = true;
    setTimeout(() => (copied = false), 2000);
  }
</script>

<div class="settings-page animate-in">
  <header class="page-header">
    <h1>Settings</h1>
  </header>

  <section class="section">
    <h2 class="section-title">System</h2>
    <div class="info-card">
      <div class="info-row">
        <span class="info-label">Version</span>
        <span class="info-value mono">{sysInfo?.version || "0.1.0-dev"}</span>
      </div>
      <div class="info-row">
        <span class="info-label">Engine</span>
        <span class="info-value mono">BrokoliSQL-Go</span>
      </div>
      <div class="info-row">
        <span class="info-label">Database</span>
        <span class="info-value mono">SQLite (embedded)</span>
      </div>
      <div class="info-row">
        <span class="info-label">DB Size</span>
        <span class="info-value mono">{sysInfo?.db_size_mb || "..."}</span>
      </div>
      <div class="info-row">
        <span class="info-label">Pipelines</span>
        <span class="info-value mono">{sysInfo?.pipelines ?? "..."}</span>
      </div>
    </div>
  </section>

  <section class="section">
    <h2 class="section-title">Maintenance</h2>
    <div class="info-card">
      <div class="maintenance-section">
        <p class="auth-desc">Purge old pipeline runs to free disk space.</p>
        <div class="purge-controls">
          <span class="purge-label">Delete runs older than</span>
          <input type="number" class="purge-input" bind:value={purgeDays} min="1" max="365" />
          <span class="purge-label">days</span>
          <button class="btn-generate" on:click={purgeRuns} disabled={purging}>
            {purging ? "Purging..." : "Purge"}
          </button>
        </div>
      </div>
    </div>
  </section>

  <section class="section">
    <h2 class="section-title">Users & Access Control</h2>
    <div class="info-card">
      {#if users.length === 0}
        <div class="auth-section">
          <p class="auth-desc">No users configured. The system is in <strong>open mode</strong> — anyone can access all features. Create a user to enable authentication.</p>
        </div>
      {:else}
        <div class="users-table">
          <div class="users-header">
            <span class="col-user">Username</span>
            <span class="col-role">Role</span>
            <span class="col-created">Created</span>
          </div>
          {#each users as user}
            <div class="users-row">
              <span class="col-user">
                {user.username}
                {#if $authUser?.username === user.username}
                  <span class="you-badge">you</span>
                {/if}
              </span>
              <span class="col-role">
                <span class="role-badge role-{user.role}">{user.role}</span>
              </span>
              <span class="col-created mono">{new Date(user.created_at).toLocaleDateString()}</span>
            </div>
          {/each}
        </div>
      {/if}
      <div class="add-user-form">
        <span class="form-title">Add User</span>
        <div class="form-row">
          <input type="text" bind:value={newUsername} placeholder="Username" class="form-input" />
          <input type="password" bind:value={newPassword} placeholder="Password" class="form-input" />
          <select bind:value={newRole} class="form-input form-select">
            <option value="admin">Admin</option>
            <option value="editor">Editor</option>
            <option value="viewer">Viewer</option>
          </select>
          <button class="btn-generate" on:click={createUser} disabled={creatingUser}>
            {creatingUser ? "Creating..." : "Add"}
          </button>
        </div>
        <div class="role-help">
          <strong>Admin:</strong> full access &nbsp;|&nbsp;
          <strong>Editor:</strong> create/edit/run pipelines &nbsp;|&nbsp;
          <strong>Viewer:</strong> read-only
        </div>
      </div>
    </div>
  </section>

  <section class="section">
    <h2 class="section-title">Authentication</h2>
    <div class="info-card">
      <div class="auth-section">
        <p class="auth-desc">
          Generate an API key and pass it via <code>--api-key</code> when starting the server to enable authentication.
        </p>
        <div class="key-actions">
          <button class="btn-generate" on:click={generateKey} disabled={generating}>
            {generating ? "Generating..." : "Generate API Key"}
          </button>
        </div>
        {#if generatedKey}
          <div class="key-display">
            <code class="key-value">{generatedKey}</code>
            <button class="btn-copy" on:click={copyKey}>
              {copied ? "Copied" : "Copy"}
            </button>
          </div>
          <p class="key-hint">
            Start the server with: <code>brokoli serve --api-key {generatedKey}</code>
          </p>
        {/if}
      </div>
    </div>
  </section>

  <section class="section">
    <h2 class="section-title">Usage</h2>
    <div class="info-card">
      <div class="info-row">
        <span class="info-label">API Endpoint</span>
        <span class="info-value mono">/api/</span>
      </div>
      <div class="info-row">
        <span class="info-label">WebSocket</span>
        <span class="info-value mono">/api/ws</span>
      </div>
      <div class="info-row">
        <span class="info-label">Auth Header</span>
        <span class="info-value mono">Authorization: Bearer brk_...</span>
      </div>
      <div class="info-row">
        <span class="info-label">Import Pipeline</span>
        <span class="info-value mono">POST /api/pipelines/import</span>
      </div>
      <div class="info-row">
        <span class="info-label">Export Pipeline</span>
        <span class="info-value mono">GET /api/pipelines/:id/export</span>
      </div>
    </div>
  </section>
</div>

<style>
  .page-header {
    margin-bottom: var(--space-xl);
  }
  .page-header h1 {
    font-size: 1.5rem;
    font-weight: 600;
    letter-spacing: -0.02em;
  }

  .section {
    margin-bottom: var(--space-xl);
  }
  .section-title {
    font-size: 0.75rem;
    font-weight: 600;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.08em;
    margin-bottom: var(--space-md);
  }

  .info-card {
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    overflow: hidden;
  }
  .info-row {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: var(--space-md) var(--space-lg);
    border-bottom: 1px solid var(--border);
  }
  .info-row:last-child { border-bottom: none; }
  .info-label {
    font-size: 0.8125rem;
    color: var(--text-secondary);
  }
  .info-value {
    font-size: 0.8125rem;
  }
  .mono { font-family: var(--font-mono); }
  code {
    background: var(--bg-tertiary);
    padding: 1px 5px;
    border-radius: 3px;
    font-size: 0.8125rem;
  }

  .auth-section {
    padding: var(--space-lg);
  }
  .auth-desc {
    font-size: 0.8125rem;
    color: var(--text-secondary);
    margin-bottom: var(--space-md);
    line-height: 1.6;
  }
  .key-actions {
    margin-bottom: var(--space-md);
  }
  .btn-generate {
    background: var(--accent);
    color: white;
    padding: 6px 14px;
    border-radius: var(--radius-md);
    font-weight: 500;
    font-size: 0.8125rem;
    transition: background 150ms ease;
  }
  .btn-generate:hover { background: var(--accent-hover); }

  .key-display {
    display: flex;
    align-items: center;
    gap: var(--space-sm);
    background: var(--bg-primary);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    padding: var(--space-sm) var(--space-md);
    margin-bottom: var(--space-sm);
  }
  .key-value {
    flex: 1;
    font-family: var(--font-mono);
    font-size: 0.8125rem;
    color: var(--accent);
    background: none;
    padding: 0;
    word-break: break-all;
  }
  .btn-copy {
    padding: 4px 10px;
    border-radius: var(--radius-sm);
    font-size: 0.75rem;
    font-weight: 500;
    background: var(--bg-tertiary);
    color: var(--text-secondary);
    transition: all 150ms ease;
    flex-shrink: 0;
  }
  .btn-copy:hover { color: var(--text-primary); background: var(--border); }

  .key-hint {
    font-size: 0.75rem;
    color: var(--text-muted);
  }
  .key-hint code {
    font-size: 0.6875rem;
  }

  .users-table { overflow: hidden; }
  .users-header, .users-row {
    display: grid;
    grid-template-columns: 1fr 100px 120px;
    padding: var(--space-sm) var(--space-lg);
    align-items: center;
  }
  .users-header {
    background: var(--bg-tertiary);
    font-size: 0.7rem;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.08em;
    font-weight: 600;
  }
  .users-row {
    border-top: 1px solid var(--border);
    font-size: 0.8125rem;
  }
  .you-badge {
    font-size: 0.625rem;
    color: var(--accent-text);
    background: var(--accent-glow);
    padding: 1px 6px;
    border-radius: 3px;
    margin-left: 4px;
  }
  .role-badge {
    font-size: 0.6875rem;
    font-weight: 600;
    padding: 2px 8px;
    border-radius: 3px;
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
  .role-admin { color: var(--failed); background: var(--failed-bg); }
  .role-editor { color: var(--accent-text); background: var(--accent-glow); }
  .role-viewer { color: var(--text-muted); background: var(--pending-bg); }

  .add-user-form {
    padding: var(--space-md) var(--space-lg);
    border-top: 1px solid var(--border);
  }
  .form-title {
    font-size: 0.6875rem;
    font-weight: 600;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.08em;
    display: block;
    margin-bottom: var(--space-sm);
  }
  .form-row {
    display: flex;
    gap: var(--space-sm);
    align-items: center;
  }
  .form-input {
    font-size: 0.8125rem;
    padding: 6px 10px;
    flex: 1;
  }
  .form-select { flex: 0.7; }
  .role-help {
    font-size: 0.6875rem;
    color: var(--text-dim);
    margin-top: var(--space-sm);
    line-height: 1.6;
  }

  .maintenance-section { padding: var(--space-lg); }
  .purge-controls {
    display: flex;
    align-items: center;
    gap: var(--space-sm);
    margin-top: var(--space-sm);
  }
  .purge-label { font-size: 0.8125rem; color: var(--text-secondary); }
  .purge-input {
    width: 60px;
    font-family: var(--font-mono);
    font-size: 0.8125rem;
    padding: 4px 8px;
    text-align: center;
  }
</style>
