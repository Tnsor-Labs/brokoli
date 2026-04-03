<script lang="ts">
  import { onMount } from "svelte";
  import { notify } from "../lib/toast";
  import { authHeaders, authUser } from "../lib/auth";
  import { icons } from "../lib/icons";

  interface UserInfo { id: string; username: string; role: string; created_at: string; }

  let generatedKey = "";
  let generating = false;
  let copied = false;
  let sysInfo: { version: string; db_size_mb: string; pipelines: number; active_runs: number; max_concurrent_runs: number } | null = null;
  let purging = false;
  let purgeDays = 30;
  let users: UserInfo[] = [];
  let newUsername = "";
  let newPassword = "";
  let newRole = "editor";
  let creatingUser = false;

  // Admin password reset
  let showResetPw = false;
  let resetUserId = "";
  let resetUsername = "";
  let resetNewPw = "";
  let resettingPw = false;

  async function adminResetPassword() {
    if (!resetNewPw || resetNewPw.length < 6) { notify.warning("Min 6 characters"); return; }
    resettingPw = true;
    try {
      const res = await fetch("/api/auth/admin-reset-password", {
        method: "POST",
        headers: { "Content-Type": "application/json", ...authHeaders() },
        body: JSON.stringify({ user_id: resetUserId, new_password: resetNewPw }),
      });
      if (res.ok) {
        notify.success(`Password reset for ${resetUsername}`);
        showResetPw = false; resetNewPw = "";
      } else {
        const data = await res.json();
        notify.error(data.error || "Failed to reset password");
      }
    } catch { notify.error("Failed"); }
    resettingPw = false;
  }

  // Password change
  let currentPw = "";
  let newPw = "";
  let confirmPw = "";
  let changingPw = false;

  async function changePassword() {
    if (!currentPw || !newPw) { notify.warning("Fill in all fields"); return; }
    if (newPw.length < 6) { notify.warning("New password must be at least 6 characters"); return; }
    if (newPw !== confirmPw) { notify.warning("Passwords don't match"); return; }
    changingPw = true;
    try {
      const res = await fetch("/api/auth/change-password", {
        method: "POST",
        headers: { "Content-Type": "application/json", ...authHeaders() },
        body: JSON.stringify({ current_password: currentPw, new_password: newPw }),
      });
      if (res.ok) {
        notify.success("Password changed");
        currentPw = ""; newPw = ""; confirmPw = "";
      } else {
        const data = await res.json();
        notify.error(data.error || "Failed to change password");
      }
    } catch {
      notify.error("Failed to change password");
    }
    changingPw = false;
  }

  // Tabs
  type Tab = "general" | "users" | "notifications" | "integrations" | "api";
  let activeTab: Tab = "general";

  const tabs: { id: Tab; label: string; icon: string }[] = [
    { id: "general", label: "General", icon: icons.settings.d },
    { id: "users", label: "Users", icon: icons.user.d },
    { id: "notifications", label: "Alerts & SLA", icon: icons.bell.d },
    { id: "integrations", label: "Integrations", icon: icons.connection.d },
    { id: "api", label: "API & CLI", icon: icons.code.d },
  ];

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
        headers: { "Content-Type": "application/json", ...authHeaders() },
        body: JSON.stringify({ days: purgeDays }),
      });
      const data = await res.json();
      notify.success(`Purged ${data.deleted} old runs`);
      const infoRes = await fetch("/api/system/info", { headers: authHeaders() });
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

  // ── Slack config ──
  let slackWebhook = "";
  let slackChannel = "";
  let slackUsername = "Brokoli";
  let slackConfigured = false;
  let slackMasked = "";
  let slackSaving = false;
  let slackTesting = false;
  let slackTestResult: { ok: boolean; msg: string } | null = null;
  let slackLoaded = false;

  // Teams config
  let teamsWebhook = "";
  let teamsConfigured = false;
  let teamsMasked = "";
  let teamsSaving = false;

  async function loadSlackConfig() {
    try {
      const res = await fetch("/api/settings/notifications", { headers: authHeaders() });
      if (res.ok) {
        const data = await res.json();
        slackConfigured = data.webhook_configured;
        slackMasked = data.webhook_masked || "";
        slackChannel = data.channel || "";
        slackUsername = data.username || "Brokoli";
        teamsConfigured = data.teams_configured || false;
        teamsMasked = data.teams_webhook_masked || "";
        slackLoaded = true;
      }
    } catch {}
  }

  async function saveSlackConfig() {
    slackSaving = true;
    slackTestResult = null;
    try {
      const body: Record<string, string> = { channel: slackChannel, username: slackUsername };
      if (slackWebhook) body.webhook = slackWebhook;
      if (teamsWebhook) body.teams_webhook = teamsWebhook;
      const res = await fetch("/api/settings/notifications", {
        method: "PUT",
        headers: { "Content-Type": "application/json", ...authHeaders() },
        body: JSON.stringify(body),
      });
      if (res.ok) {
        notify.success("Slack config saved");
        slackWebhook = "";
        await loadSlackConfig();
      } else {
        notify.error("Failed to save");
      }
    } catch {
      notify.error("Failed to save");
    }
    slackSaving = false;
  }

  async function testSlack() {
    slackTesting = true;
    slackTestResult = null;
    try {
      const res = await fetch("/api/settings/notifications/test", {
        method: "POST",
        headers: authHeaders(),
      });
      const data = await res.json();
      if (res.ok) {
        slackTestResult = { ok: true, msg: "Test message sent successfully!" };
      } else {
        slackTestResult = { ok: false, msg: data.error || "Test failed" };
      }
    } catch {
      slackTestResult = { ok: false, msg: "Request failed" };
    }
    slackTesting = false;
  }

  async function clearSlack() {
    await fetch("/api/settings/notifications", { method: "DELETE", headers: authHeaders() });
    notify.success("Slack config cleared");
    slackWebhook = "";
    slackChannel = "";
    slackUsername = "Brokoli";
    await loadSlackConfig();
  }

  // Load slack config when switching to notifications tab
  $: if (activeTab === "notifications" && !slackLoaded) loadSlackConfig();
</script>

<div class="settings-page animate-in">
  <header class="page-header">
    <h1>Settings</h1>
  </header>

  <!-- Tab bar -->
  <div class="tab-bar">
    {#each tabs as tab}
      <button
        class="tab-btn"
        class:active={activeTab === tab.id}
        on:click={() => activeTab = tab.id}
      >
        <svg width="15" height="15" viewBox="0 0 24 24" fill="none">
          <path d={tab.icon} stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
        </svg>
        {tab.label}
      </button>
    {/each}
  </div>

  <div class="tab-content">

    <!-- ═══════════════════ GENERAL TAB ═══════════════════ -->
    {#if activeTab === "general"}
      <section class="section">
        <h2 class="section-title">System Info</h2>
        <div class="info-card">
          <div class="info-row">
            <span class="info-label">Version</span>
            <span class="info-value mono">{sysInfo?.version || "0.1.0-dev"}</span>
          </div>
          <div class="info-row">
            <span class="info-label">Edition</span>
            <span class="info-value">
              <span class="edition-badge">community</span>
            </span>
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
          <div class="info-row">
            <span class="info-label">Active Runs</span>
            <span class="info-value mono">{sysInfo?.active_runs ?? 0}</span>
          </div>
          <div class="info-row">
            <span class="info-label">Max Concurrent</span>
            <span class="info-value mono">{sysInfo?.max_concurrent_runs ?? "..."}</span>
          </div>
        </div>
      </section>

      <section class="section">
        <h2 class="section-title">Maintenance</h2>
        <div class="info-card">
          <div class="auth-section">
            <p class="auth-desc">Purge old pipeline runs to free disk space.</p>
            <div class="purge-controls">
              <span class="purge-label">Delete runs older than</span>
              <input type="number" class="purge-input" bind:value={purgeDays} min="1" max="365" />
              <span class="purge-label">days</span>
              <button class="btn-action" on:click={purgeRuns} disabled={purging}>
                {purging ? "Purging..." : "Purge"}
              </button>
            </div>
          </div>
        </div>
      </section>

    <!-- ═══════════════════ USERS TAB ═══════════════════ -->
    {:else if activeTab === "users"}
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
                <span class="col-actions">Actions</span>
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
                  <span class="col-actions">
                    {#if $authUser?.role === "admin" && $authUser?.username !== user.username}
                      <button class="btn-reset-pw" on:click={() => { resetUserId = user.id; resetUsername = user.username; showResetPw = true; }}>
                        Reset PW
                      </button>
                    {/if}
                  </span>
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
              <button class="btn-action" on:click={createUser} disabled={creatingUser}>
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

      {#if $authUser}
        <section class="section">
          <h2 class="section-title">Change Password</h2>
          <div class="info-card">
            <div class="auth-section">
              <div class="pw-form">
                <div class="form-group-inline">
                  <label>Current Password</label>
                  <input type="password" bind:value={currentPw} placeholder="Enter current password" />
                </div>
                <div class="form-row-2">
                  <div class="form-group-inline">
                    <label>New Password</label>
                    <input type="password" bind:value={newPw} placeholder="Min 6 characters" />
                  </div>
                  <div class="form-group-inline">
                    <label>Confirm New Password</label>
                    <input type="password" bind:value={confirmPw} placeholder="Repeat new password"
                      on:keydown={(e) => { if (e.key === "Enter") changePassword(); }} />
                  </div>
                </div>
                <button class="btn-action" on:click={changePassword} disabled={changingPw}>
                  {changingPw ? "Changing..." : "Change Password"}
                </button>
              </div>
            </div>
          </div>
        </section>
      {/if}

      {#if showResetPw}
        <!-- svelte-ignore a11y_no_static_element_interactions -->
        <div class="modal-overlay" on:click={() => showResetPw = false} on:keydown={() => {}}>
          <!-- svelte-ignore a11y_no_static_element_interactions -->
          <div class="modal" on:click|stopPropagation on:keydown={() => {}}>
            <h2>Reset Password: {resetUsername}</h2>
            <div class="form-group-inline">
              <label>New Password</label>
              <input type="password" bind:value={resetNewPw} placeholder="Min 6 characters"
                on:keydown={(e) => { if (e.key === "Enter") adminResetPassword(); }} />
            </div>
            <div class="modal-actions">
              <button class="btn-secondary" on:click={() => showResetPw = false}>Cancel</button>
              <button class="btn-action" on:click={adminResetPassword} disabled={resettingPw}>
                {resettingPw ? "Resetting..." : "Reset Password"}
              </button>
            </div>
          </div>
        </div>
      {/if}

      <section class="section">
        <h2 class="section-title">API Key Authentication</h2>
        <div class="info-card">
          <div class="auth-section">
            <p class="auth-desc">
              Generate an API key and pass it via <code>--api-key</code> when starting the server.
            </p>
            <div class="key-actions">
              <button class="btn-action" on:click={generateKey} disabled={generating}>
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

    <!-- ═══════════════════ ALERTS & SLA TAB ═══════════════════ -->
    {:else if activeTab === "notifications"}
      <section class="section">
        <h2 class="section-title">Slack Notifications</h2>
        <div class="info-card">
          <!-- Status -->
          <div class="info-row">
            <span class="info-label">Status</span>
            <span class="info-value">
              {#if slackConfigured}
                <span class="status-active">
                  <span class="status-dot-green"></span>
                  Active
                </span>
              {:else}
                <span class="status-inactive">Not configured</span>
              {/if}
            </span>
          </div>
          {#if slackConfigured}
            <div class="info-row">
              <span class="info-label">Webhook</span>
              <span class="info-value mono">{slackMasked}</span>
            </div>
            <div class="info-row">
              <span class="info-label">Channel</span>
              <span class="info-value mono">{slackChannel || "default"}</span>
            </div>
            <div class="info-row">
              <span class="info-label">Bot Name</span>
              <span class="info-value">{slackUsername}</span>
            </div>
          {/if}

          <!-- Config form -->
          <div class="slack-form">
            <div class="form-group-inline">
              <label>Webhook URL</label>
              <input
                type="password"
                bind:value={slackWebhook}
                placeholder={slackConfigured ? "Leave empty to keep current" : "https://hooks.slack.com/services/T.../B.../xxx"}
              />
            </div>
            <div class="form-row-2">
              <div class="form-group-inline">
                <label>Channel</label>
                <input bind:value={slackChannel} placeholder="#data-alerts" />
              </div>
              <div class="form-group-inline">
                <label>Bot Name</label>
                <input bind:value={slackUsername} placeholder="Brokoli" />
              </div>
            </div>

            <div class="slack-actions">
              <button class="btn-action" on:click={saveSlackConfig} disabled={slackSaving}>
                {slackSaving ? "Saving..." : "Save Configuration"}
              </button>
              {#if slackConfigured}
                <button class="btn-action btn-test" on:click={testSlack} disabled={slackTesting}>
                  {slackTesting ? "Sending..." : "Send Test Message"}
                </button>
                <button class="btn-action btn-clear" on:click={clearSlack}>
                  Disconnect
                </button>
              {/if}
            </div>

            {#if slackTestResult}
              <div class="test-result" class:success={slackTestResult.ok} class:fail={!slackTestResult.ok}>
                {slackTestResult.msg}
              </div>
            {/if}
          </div>

          <!-- Events info -->
          <div class="slack-events">
            <span class="events-label">Alert events:</span>
            <span class="event-tag">run.completed</span>
            <span class="event-tag">run.failed</span>
            <span class="event-tag alert">sla.breach</span>
          </div>
        </div>
      </section>

      <section class="section">
        <h2 class="section-title">Microsoft Teams</h2>
        <div class="info-card">
          <div class="info-row">
            <span class="info-label">Status</span>
            <span class="info-value">
              {#if teamsConfigured}
                <span class="status-active">
                  <span class="status-dot-green"></span>
                  Active
                </span>
              {:else}
                <span class="status-inactive">Not configured</span>
              {/if}
            </span>
          </div>
          {#if teamsConfigured}
            <div class="info-row">
              <span class="info-label">Webhook</span>
              <span class="info-value mono">{teamsMasked}</span>
            </div>
          {/if}
          <div class="slack-form">
            <div class="form-group-inline">
              <label>Teams Webhook URL</label>
              <input
                type="password"
                bind:value={teamsWebhook}
                placeholder={teamsConfigured ? "Leave empty to keep current" : "https://your-org.webhook.office.com/webhookb2/..."}
              />
            </div>
            <div class="slack-actions">
              <button class="btn-action" on:click={saveSlackConfig} disabled={teamsSaving}>
                Save
              </button>
              {#if teamsConfigured}
                <button class="btn-action btn-clear" on:click={async () => {
                  await fetch("/api/settings/notifications", { method: "PUT", headers: { "Content-Type": "application/json", ...authHeaders() }, body: JSON.stringify({ teams_webhook: "__clear__" }) });
                  // For now just reload
                  teamsWebhook = "";
                  await loadSlackConfig();
                  notify.success("Teams disconnected");
                }}>
                  Disconnect
                </button>
              {/if}
            </div>
            <p class="auth-desc" style="margin-top: 12px; font-size: 11px; color: var(--text-dim)">
              Create a webhook in Teams: Channel Settings → Connectors → Incoming Webhook → Configure.
            </p>
          </div>
        </div>
      </section>

      <section class="section">
        <h2 class="section-title">SLA Monitoring</h2>
        <div class="info-card">
          <div class="auth-section">
            <p class="auth-desc">
              Set SLA deadlines per pipeline in the editor toolbar (click <strong>SLA</strong>).
              The checker runs every minute and alerts when a pipeline misses its deadline.
            </p>
          </div>
          <div class="info-row">
            <span class="info-label">Check interval</span>
            <span class="info-value mono">1 minute</span>
          </div>
          <div class="info-row">
            <span class="info-label">Alert window</span>
            <span class="info-value mono">1 hour after deadline</span>
          </div>
          <div class="info-row">
            <span class="info-label">Alert channel</span>
            <span class="info-value mono">Slack (if configured)</span>
          </div>
        </div>
      </section>

    <!-- ═══════════════════ INTEGRATIONS TAB ═══════════════════ -->
    {:else if activeTab === "integrations"}
      <section class="section">
        <h2 class="section-title">Python Integration</h2>
        <div class="info-card">
          <div class="auth-section">
            <p class="auth-desc">Python Code nodes work with any <code>python3</code>. For faster processing:</p>
            <pre class="code-block">pip install pyarrow pandas</pre>
            <p class="auth-desc" style="margin-top: 8px">Recommended: use a virtualenv and set the path in Code node config:</p>
            <pre class="code-block">python3 -m venv ~/.brokoli-env
~/.brokoli-env/bin/pip install pyarrow pandas numpy requests</pre>
            <p class="auth-desc" style="margin-top: 8px; font-size: 11px; color: var(--text-dim)">
              Under 10K rows: JSON. Larger: CSV temp files (3-5x faster). With pyarrow: Arrow IPC (5-10x faster).
            </p>
          </div>
        </div>
      </section>

      <section class="section">
        <h2 class="section-title">OpenLineage</h2>
        <div class="info-card">
          <div class="info-row">
            <span class="info-label">Status</span>
            <span class="info-value">
              <span class="edition-badge">enterprise</span>
            </span>
          </div>
          <div class="auth-section">
            <p class="auth-desc">Emit lineage events to DataHub, Marquez, or any OpenLineage-compatible endpoint:</p>
            <pre class="code-block">BROKOLI_OPENLINEAGE_URL=http://marquez:5000/api/v1/lineage
BROKOLI_OPENLINEAGE_NAMESPACE=brokoli-prod
BROKOLI_OPENLINEAGE_API_KEY=...</pre>
          </div>
        </div>
      </section>

      <section class="section">
        <h2 class="section-title">Webhook Triggers</h2>
        <div class="info-card">
          <div class="auth-section">
            <p class="auth-desc">
              Trigger pipeline runs via HTTP. Generate a webhook token in the pipeline editor (click <strong>Webhook</strong>), then:
            </p>
            <pre class="code-block">curl -X POST http://localhost:9900/api/pipelines/PIPELINE_ID/webhook?token=whk_...</pre>
            <p class="auth-desc" style="margin-top: 8px; font-size: 11px; color: var(--text-dim)">
              Useful for triggering on external events: git push, model deploy, dbt completion, Kafka consumer, etc.
            </p>
          </div>
        </div>
      </section>

    <!-- ═══════════════════ API & CLI TAB ═══════════════════ -->
    {:else if activeTab === "api"}
      <section class="section">
        <h2 class="section-title">API Reference</h2>
        <div class="info-card">
          <div class="info-row">
            <span class="info-label">Base URL</span>
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
          <div class="info-row">
            <span class="info-label">Webhook Trigger</span>
            <span class="info-value mono">POST /api/pipelines/:id/webhook</span>
          </div>
          <div class="info-row">
            <span class="info-label">Node Profile</span>
            <span class="info-value mono">GET /api/runs/:id/nodes/:nid/profile</span>
          </div>
          <div class="info-row">
            <span class="info-label">Dependencies</span>
            <span class="info-value mono">GET /api/pipelines/:id/deps</span>
          </div>
          <div class="info-row">
            <span class="info-label">Impact Analysis</span>
            <span class="info-value mono">GET /api/pipelines/:id/impact</span>
          </div>
        </div>
      </section>

      <section class="section">
        <h2 class="section-title">CLI Commands</h2>
        <div class="info-card">
          <div class="auth-section">
            <p class="auth-desc">Run and test pipelines from the command line or CI/CD:</p>
            <pre class="code-block"># Trigger a pipeline run and wait for completion
brokoli run PIPELINE_ID --server http://localhost:9900

# Run with assertions (CI/CD testing)
brokoli assert PIPELINE_ID -a assertions.yaml --server http://localhost:9900

# Start the server
brokoli serve --port 9900 --db ./brokoli.db</pre>
          </div>
        </div>
      </section>

      <section class="section">
        <h2 class="section-title">Assertion File Format</h2>
        <div class="info-card">
          <div class="auth-section">
            <pre class="code-block"># assertions.yaml
assertions:
  - name: "Has data"
    type: min_rows
    value: "1"
  - name: "ID is unique"
    type: unique
    column: id
  - name: "Email not null"
    type: no_nulls
    column: email
  - name: "Amount is numeric"
    type: column_type
    column: amount
    value: number
  - name: "Row count check"
    type: row_count
    operator: ">"
    value: "100"</pre>
          </div>
        </div>
      </section>
    {/if}
  </div>
</div>

<style>
  .page-header {
    margin-bottom: var(--space-lg);
  }
  .page-header h1 {
    font-size: 1.5rem;
    font-weight: 600;
    letter-spacing: -0.02em;
  }

  /* ── Tab Bar ── */
  .tab-bar {
    display: flex;
    gap: 2px;
    border-bottom: 1px solid var(--border);
    margin-bottom: var(--space-xl);
  }
  .tab-btn {
    display: flex;
    align-items: center;
    gap: 7px;
    padding: 10px 16px;
    font-size: 13px;
    font-weight: 500;
    color: var(--text-muted);
    border-bottom: 2px solid transparent;
    margin-bottom: -1px;
    transition: all 150ms ease;
    background: none;
    border-radius: 0;
  }
  .tab-btn:hover {
    color: var(--text-primary);
    background: var(--bg-tertiary);
  }
  .tab-btn.active {
    color: var(--accent-text);
    border-bottom-color: var(--accent);
  }
  .tab-btn svg {
    opacity: 0.6;
  }
  .tab-btn.active svg {
    opacity: 1;
  }

  .tab-content {
    min-height: 400px;
  }

  /* ── Sections ── */
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
  .edition-badge {
    font-size: 11px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.04em;
    padding: 2px 8px; border-radius: 4px;
    background: var(--bg-tertiary); color: var(--text-muted);
  }
  .edition-badge.enterprise {
    background: var(--accent-glow); color: var(--accent);
  }
  .feature-tag {
    font-size: 10px; font-family: var(--font-mono); font-weight: 500;
    padding: 1px 6px; border-radius: 3px;
    background: var(--accent-glow); color: var(--accent);
    margin-right: 4px;
  }
  .code-block {
    font-family: var(--font-mono); font-size: 11px; line-height: 1.6;
    background: var(--bg-code); border: 1px solid var(--border-subtle);
    border-radius: var(--radius-md); padding: 10px 14px;
    color: var(--text-secondary); white-space: pre; overflow-x: auto;
    margin: 0;
  }
  .auth-desc code {
    font-family: var(--font-mono); font-size: 12px;
    color: var(--accent); font-weight: 500;
  }
  .auth-desc {
    font-size: 0.8125rem;
    color: var(--text-secondary);
    margin-bottom: var(--space-md);
    line-height: 1.6;
  }

  .event-tags {
    display: flex; gap: 6px; margin-top: var(--space-sm);
  }
  .event-tag {
    font-family: var(--font-mono); font-size: 11px; font-weight: 500;
    padding: 2px 8px; border-radius: 4px;
    background: var(--bg-tertiary); color: var(--text-secondary);
  }
  .event-tag.alert {
    background: rgba(245, 158, 11, 0.1); color: var(--warning);
  }

  .btn-action {
    background: var(--accent);
    color: white;
    padding: 6px 14px;
    border-radius: var(--radius-md);
    font-weight: 500;
    font-size: 0.8125rem;
    transition: background 150ms ease;
  }
  .btn-action:hover:not(:disabled) { background: var(--accent-hover); }
  .btn-action:disabled { opacity: 0.5; }

  .key-actions {
    margin-bottom: var(--space-md);
  }
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
    grid-template-columns: 1fr 100px 100px 80px;
    padding: var(--space-sm) var(--space-lg);
    align-items: center;
  }
  .col-actions { text-align: right; }
  .btn-reset-pw {
    padding: 3px 8px; border-radius: 4px; font-size: 10px; font-weight: 500;
    color: var(--text-dim); transition: all 150ms ease;
  }
  .btn-reset-pw:hover { color: var(--warning); background: rgba(245,158,11,0.1); }
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

  .purge-controls {
    display: flex;
    align-items: center;
    gap: var(--space-sm);
  }
  .purge-label { font-size: 0.8125rem; color: var(--text-secondary); }
  /* ── Slack config ── */
  .status-active {
    display: flex; align-items: center; gap: 6px;
    font-size: 12px; font-weight: 600; color: #22c55e;
  }
  .status-dot-green {
    width: 7px; height: 7px; border-radius: 50%;
    background: #22c55e; box-shadow: 0 0 6px rgba(34,197,94,0.5);
  }
  .status-inactive {
    font-size: 12px; color: var(--text-dim);
  }
  .slack-form {
    padding: var(--space-lg);
    border-top: 1px solid var(--border);
  }
  .form-group-inline {
    margin-bottom: 12px;
  }
  .form-group-inline label {
    display: block; font-size: 10px; color: var(--text-muted);
    text-transform: uppercase; letter-spacing: 0.08em;
    margin-bottom: 4px; font-weight: 600;
  }
  .form-group-inline input {
    width: 100%; padding: 9px 12px;
    background: var(--bg-primary); border: 1px solid var(--border);
    border-radius: var(--radius-md); color: var(--text-primary);
    font-size: 13px; font-family: var(--font-ui);
  }
  .form-group-inline input:focus { border-color: var(--accent); outline: none; }
  .form-row-2 {
    display: grid; grid-template-columns: 1fr 1fr; gap: 12px;
  }
  .slack-actions {
    display: flex; gap: 8px; margin-top: 4px;
  }
  .btn-test {
    background: var(--bg-tertiary) !important;
    color: var(--text-secondary) !important;
    border: 1px solid var(--border);
  }
  .btn-test:hover:not(:disabled) {
    background: var(--border-subtle) !important;
    color: var(--text-primary) !important;
  }
  .btn-clear {
    background: transparent !important;
    color: var(--failed) !important;
    border: 1px solid rgba(239,68,68,0.2);
  }
  .btn-clear:hover { background: var(--failed-bg) !important; }
  .test-result {
    margin-top: 12px; padding: 10px 14px; border-radius: var(--radius-md);
    font-size: 12px; font-weight: 500;
  }
  .test-result.success {
    background: var(--success-bg); color: #22c55e;
    border: 1px solid rgba(34,197,94,0.2);
  }
  .test-result.fail {
    background: var(--failed-bg); color: var(--failed);
    border: 1px solid rgba(239,68,68,0.2);
  }
  .slack-events {
    display: flex; align-items: center; gap: 6px;
    padding: 12px var(--space-lg);
    border-top: 1px solid var(--border);
  }
  .events-label { font-size: 11px; color: var(--text-dim); }

  .purge-input {
    width: 60px;
    font-family: var(--font-mono);
    font-size: 0.8125rem;
    padding: 4px 8px;
    text-align: center;
  }

  /* ── Roles ── */
  .tab-header-row {
    display: flex; justify-content: space-between; align-items: center;
    margin-bottom: var(--space-md);
  }
  .roles-list { display: flex; flex-direction: column; gap: 8px; }
  .role-card {
    background: var(--bg-secondary); border: 1px solid var(--border);
    border-radius: var(--radius-lg); overflow: hidden;
    transition: border-color 150ms ease;
  }
  .role-card.editing { border-color: var(--accent); }
  .role-card-header {
    display: flex; justify-content: space-between; align-items: center;
    padding: 14px 20px;
  }
  .role-card-info { display: flex; flex-direction: column; gap: 2px; }
  .role-card-name { font-size: 14px; font-weight: 600; display: flex; align-items: center; gap: 8px; }
  .role-card-desc { font-size: 11px; color: var(--text-muted); }
  .system-badge {
    font-size: 9px; font-weight: 600; text-transform: uppercase;
    padding: 1px 6px; border-radius: 3px;
    background: var(--bg-tertiary); color: var(--text-dim);
    letter-spacing: 0.06em;
  }
  .role-card-meta { display: flex; align-items: center; gap: 8px; }
  .perm-count {
    font-size: 11px; color: var(--text-dim); font-family: var(--font-mono);
  }
  .btn-sm-action {
    padding: 5px 14px; border-radius: 5px; font-size: 11px; font-weight: 500;
    background: var(--bg-tertiary); border: 1px solid var(--border);
    color: var(--text-secondary); transition: all 150ms ease;
  }
  .btn-sm-action:hover { border-color: var(--accent); color: var(--accent-text); }
  .btn-sm-danger {
    padding: 5px 14px; border-radius: 5px; font-size: 11px; font-weight: 500;
    color: var(--text-dim); transition: all 150ms ease;
  }
  .btn-sm-danger:hover { color: var(--failed); background: var(--failed-bg); }
  .btn-secondary-sm {
    padding: 6px 14px; border-radius: var(--radius-md); font-size: 12px;
    background: var(--bg-tertiary); color: var(--text-secondary);
    border: 1px solid var(--border);
  }

  /* Permission editor */
  .perm-editor {
    padding: 20px 24px; border-top: 1px solid var(--border);
    background: var(--bg-primary);
  }
  .perm-group {
    margin-bottom: 20px;
    padding: 14px 16px;
    background: var(--bg-secondary);
    border: 1px solid var(--border-subtle);
    border-radius: 8px;
  }
  .perm-group:last-child { margin-bottom: 0; }
  .perm-group-title {
    display: block; font-size: 11px; font-weight: 600;
    text-transform: uppercase; letter-spacing: 0.08em;
    color: var(--text-muted); margin-bottom: 10px;
    padding-bottom: 8px; border-bottom: 1px solid var(--border-subtle);
  }
  .perm-checkboxes {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
    gap: 6px;
  }
  .perm-checkbox {
    display: flex; align-items: center; gap: 8px;
    font-size: 12px; color: var(--text-secondary);
    cursor: pointer; padding: 6px 10px;
    border-radius: 6px;
    transition: background 120ms ease;
  }
  .perm-checkbox:hover { background: var(--bg-tertiary); }

  /* Custom toggle switch */
  .perm-checkbox input[type="checkbox"] {
    appearance: none; -webkit-appearance: none;
    width: 32px; height: 18px;
    background: var(--bg-tertiary);
    border: 1px solid var(--border);
    border-radius: 9px;
    position: relative;
    cursor: pointer;
    transition: all 200ms ease;
    flex-shrink: 0;
  }
  .perm-checkbox input[type="checkbox"]::after {
    content: "";
    position: absolute;
    top: 2px; left: 2px;
    width: 12px; height: 12px;
    border-radius: 50%;
    background: var(--text-ghost);
    transition: all 200ms ease;
  }
  .perm-checkbox input[type="checkbox"]:checked {
    background: var(--accent);
    border-color: var(--accent);
  }
  .perm-checkbox input[type="checkbox"]:checked::after {
    background: white;
    transform: translateX(14px);
  }
  .perm-checkbox input:disabled {
    opacity: 0.4; cursor: default;
  }
  .perm-label { text-transform: capitalize; user-select: none; }
  .perm-actions {
    display: flex; gap: 8px; margin-top: 20px;
    padding-top: 14px; border-top: 1px solid var(--border-subtle);
  }

  .modal-overlay {
    position: fixed; inset: 0;
    background: rgba(0,0,0,0.6);
    display: flex; align-items: center; justify-content: center;
    z-index: 200;
  }
  .modal {
    background: var(--bg-secondary); border: 1px solid var(--border);
    border-radius: 12px; padding: 28px;
    max-width: 90vw;
  }
  .modal h2 { font-size: 17px; font-weight: 600; margin-bottom: 16px; }
  .modal-actions { display: flex; justify-content: flex-end; gap: 8px; margin-top: 20px; }
  .modal-role {
    width: 640px; max-width: 95vw; max-height: 85vh;
    display: flex; flex-direction: column;
    padding: 0; overflow: hidden;
  }
  .modal-role-header {
    padding: 24px 28px 16px;
    border-bottom: 1px solid var(--border);
    flex-shrink: 0;
  }
  .modal-role-header h2 { margin-bottom: 16px; }
  .modal-role-body {
    flex: 1; overflow-y: auto;
    min-height: 0;
  }
  .modal-role-body .perm-editor { padding: 16px 28px 20px; }
  .modal-role-footer {
    padding: 14px 28px;
    border-top: 1px solid var(--border);
    background: var(--bg-secondary);
    flex-shrink: 0;
    display: flex; justify-content: space-between; align-items: center;
  }
  .perm-selected-count {
    font-size: 11px; font-family: var(--font-mono);
    color: var(--text-dim);
  }
</style>
