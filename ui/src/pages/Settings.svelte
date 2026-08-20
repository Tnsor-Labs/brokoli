<script lang="ts">
  import { onMount } from "svelte";
  import { notify } from "../lib/toast";
  import { authHeaders, authUser } from "../lib/auth";
  import { icons } from "../lib/icons";
  import PageHeader from "../components/PageHeader.svelte";
  import BrandIcon from "../components/BrandIcon.svelte";
  import Stepper from "../components/Stepper.svelte";

  interface UserInfo {
    id: string;
    username: string;
    display_name?: string;
    email?: string;
    role: string;
    created_at: string;
  }

  let generatedKey = "";
  let generating = false;
  let copied = false;
  let sysInfo: {
    version: string;
    db_size_mb: string;
    pipelines: number;
    active_runs: number;
    max_concurrent_runs: number;
  } | null = null;
  let sysInfoLoaded = false;
  let purging = false;
  let purgeDays = 30;
  let users: UserInfo[] = [];
  let usersLoaded = false;
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
    if (!resetNewPw || resetNewPw.length < 6) {
      notify.warning("Min 6 characters");
      return;
    }
    resettingPw = true;
    try {
      const res = await fetch("/api/auth/admin-reset-password", {
        method: "POST",
        headers: { "Content-Type": "application/json", ...authHeaders() },
        body: JSON.stringify({ user_id: resetUserId, new_password: resetNewPw }),
      });
      if (res.ok) {
        notify.success(`Password reset for ${resetUsername}`);
        showResetPw = false;
        resetNewPw = "";
      } else {
        const data = await res.json();
        notify.error(data.error || "Failed to reset password");
      }
    } catch {
      notify.error("Failed");
    }
    resettingPw = false;
  }

  // Password change
  let currentPw = "";
  let newPw = "";
  let confirmPw = "";
  let changingPw = false;

  async function changePassword() {
    if (!currentPw || !newPw) {
      notify.warning("Fill in all fields");
      return;
    }
    if (newPw.length < 6) {
      notify.warning("New password must be at least 6 characters");
      return;
    }
    if (newPw !== confirmPw) {
      notify.warning("Passwords don't match");
      return;
    }
    changingPw = true;
    try {
      const res = await fetch("/api/auth/change-password", {
        method: "POST",
        headers: { "Content-Type": "application/json", ...authHeaders() },
        body: JSON.stringify({ current_password: currentPw, new_password: newPw }),
      });
      if (res.ok) {
        notify.success("Password changed");
        currentPw = "";
        newPw = "";
        confirmPw = "";
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

  // brandIcon is the migrated bk-* sprite name; icon (legacy path data) is
  // only kept as a fallback for "notifications" -- no bell/notification
  // symbol exists in the v1.4/v1.5 icon set.
  const tabs: { id: Tab; label: string; description: string; brandIcon?: string; icon?: string }[] =
    [
      {
        id: "general",
        label: "General",
        description: "Runtime & maintenance",
        brandIcon: "settings",
      },
      { id: "users", label: "Users", description: "Access & credentials", brandIcon: "account" },
      {
        id: "notifications",
        label: "Alerts & SLA",
        description: "Delivery & monitoring",
        icon: icons.bell.d,
      },
      {
        id: "integrations",
        label: "Integrations",
        description: "Python & lineage",
        brandIcon: "lineage",
      },
      { id: "api", label: "API & CLI", description: "Endpoints & automation", brandIcon: "api" },
    ];

  onMount(async () => {
    try {
      const res = await fetch("/api/system/info", { headers: authHeaders() });
      sysInfo = await res.json();
    } catch {
      /* ignore */
    } finally {
      sysInfoLoaded = true;
    }
    loadUsers();
  });

  async function loadUsers() {
    try {
      const res = await fetch("/api/auth/users", { headers: authHeaders() });
      if (res.ok) users = await res.json();
    } catch {
      /* ignore */
    } finally {
      usersLoaded = true;
    }
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
      const hex = Array.from(arr)
        .map((b) => b.toString(16).padStart(2, "0"))
        .join("");
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
  let slackLoading = false;
  let slackLoadFailed = false;

  // Teams config
  let teamsWebhook = "";
  let teamsConfigured = false;
  let teamsMasked = "";
  let teamsSaving = false;

  async function loadSlackConfig() {
    slackLoading = true;
    slackLoadFailed = false;
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
      } else {
        slackLoadFailed = true;
      }
    } catch {
      slackLoadFailed = true;
    } finally {
      slackLoading = false;
    }
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
  <PageHeader
    brandIcon="settings"
    kicker="Organization control center"
    title="Brokoli workspace"
    description="Runtime, access, delivery, and automation in one operational view."
  >
    <dl slot="kpi-rail" class="identity-stats" aria-label="System summary">
      <div>
        <dt>Edition</dt>
        <dd>Community</dd>
      </div>
      <div>
        <dt>Version</dt>
        <dd>{sysInfoLoaded ? sysInfo?.version || "Unavailable" : "--"}</dd>
      </div>
      <div>
        <dt>Pipelines</dt>
        <dd>{sysInfoLoaded ? (sysInfo?.pipelines ?? "Unavailable") : "--"}</dd>
      </div>
      <div class:attention={(sysInfo?.active_runs ?? 0) > 0}>
        <dt>Active runs</dt>
        <dd>{sysInfoLoaded ? (sysInfo?.active_runs ?? "Unavailable") : "--"}</dd>
      </div>
    </dl>
  </PageHeader>

  <nav class="tab-bar" aria-label="Organization settings">
    {#each tabs as tab}
      <button
        type="button"
        class="tab-btn"
        class:active={activeTab === tab.id}
        aria-current={activeTab === tab.id ? "page" : undefined}
        on:click={() => (activeTab = tab.id)}
      >
        <span class="tab-icon" aria-hidden="true">
          {#if tab.brandIcon}
            <BrandIcon name={tab.brandIcon} size={15} />
          {:else}
            <svg width="15" height="15" viewBox="0 0 24 24" fill="none">
              <path
                d={tab.icon}
                stroke="currentColor"
                stroke-width="1.5"
                stroke-linecap="round"
                stroke-linejoin="round"
              />
            </svg>
          {/if}
        </span>
        <span class="tab-copy"><strong>{tab.label}</strong><small>{tab.description}</small></span>
        <span class="tab-arrow" aria-hidden="true">›</span>
      </button>
    {/each}
  </nav>

  <div class="tab-content">
    <!-- ═══════════════════ GENERAL TAB ═══════════════════ -->
    {#if activeTab === "general"}
      <section class="section">
        <h2 class="section-title">System Info</h2>
        <div class="info-card">
          <div class="info-row">
            <span class="info-label">Version</span>
            <span class="info-value mono"
              >{sysInfoLoaded ? sysInfo?.version || "Unavailable" : "Loading..."}</span
            >
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
            <span class="info-value mono"
              >{sysInfoLoaded ? sysInfo?.db_size_mb || "Unavailable" : "Loading..."}</span
            >
          </div>
          <div class="info-row">
            <span class="info-label">Pipelines</span>
            <span class="info-value mono"
              >{sysInfoLoaded ? (sysInfo?.pipelines ?? "Unavailable") : "Loading..."}</span
            >
          </div>
          <div class="info-row">
            <span class="info-label">Active Runs</span>
            <span class="info-value mono"
              >{sysInfoLoaded ? (sysInfo?.active_runs ?? "Unavailable") : "Loading..."}</span
            >
          </div>
          <div class="info-row">
            <span class="info-label">Max Concurrent</span>
            <span class="info-value mono"
              >{sysInfoLoaded
                ? (sysInfo?.max_concurrent_runs ?? "Unavailable")
                : "Loading..."}</span
            >
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
              <Stepper bind:value={purgeDays} min={1} max={365} />
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
          {#if !usersLoaded}
            <div class="empty-state" role="status">
              <span class="state-pulse" aria-hidden="true"></span>
              <div>
                <strong>Loading access inventory</strong><span
                  >Checking configured users and roles.</span
                >
              </div>
            </div>
          {:else if users.length === 0}
            <div class="auth-section">
              <p class="auth-desc">
                No users configured. The system is in <strong>open mode</strong> — anyone can access all
                features. Create a user to enable authentication.
              </p>
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
                    {user.display_name || user.username}
                    {#if user.display_name}
                      <span class="user-handle">{user.username}</span>
                    {/if}
                    {#if $authUser?.username === user.username}
                      <span class="you-badge">you</span>
                    {/if}
                  </span>
                  <span class="col-role">
                    <span class="role-badge role-{user.role}">{user.role}</span>
                  </span>
                  <span class="col-created mono"
                    >{new Date(user.created_at).toLocaleDateString()}</span
                  >
                  <span class="col-actions">
                    {#if $authUser?.role === "admin" && $authUser?.username !== user.username}
                      <button
                        class="btn-reset-pw"
                        on:click={() => {
                          resetUserId = user.id;
                          resetUsername = user.username;
                          showResetPw = true;
                        }}
                      >
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
              <input
                type="text"
                bind:value={newUsername}
                placeholder="Username"
                class="form-input"
              />
              <input
                type="password"
                bind:value={newPassword}
                placeholder="Password"
                class="form-input"
              />
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
                  <input
                    type="password"
                    bind:value={currentPw}
                    placeholder="Enter current password"
                  />
                </div>
                <div class="form-row-2">
                  <div class="form-group-inline">
                    <label>New Password</label>
                    <input type="password" bind:value={newPw} placeholder="Min 6 characters" />
                  </div>
                  <div class="form-group-inline">
                    <label>Confirm New Password</label>
                    <input
                      type="password"
                      bind:value={confirmPw}
                      placeholder="Repeat new password"
                      on:keydown={(e) => {
                        if (e.key === "Enter") changePassword();
                      }}
                    />
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
        <div class="modal-overlay" on:click={() => (showResetPw = false)} on:keydown={() => {}}>
          <!-- svelte-ignore a11y_no_static_element_interactions -->
          <div
            class="modal"
            role="dialog"
            aria-modal="true"
            aria-labelledby="reset-password-title"
            aria-describedby="reset-password-description"
            tabindex="-1"
            on:click|stopPropagation
            on:keydown={(event) => {
              if (event.key === "Escape") showResetPw = false;
            }}
          >
            <button
              class="modal-close"
              type="button"
              aria-label="Close reset password dialog"
              on:click={() => (showResetPw = false)}>×</button
            >
            <header class="modal-header">
              <div class="modal-symbol" aria-hidden="true">
                <svg viewBox="0 0 24 24"
                  ><path d="M7 11V8a5 5 0 0 1 10 0v3"></path><rect
                    x="5"
                    y="11"
                    width="14"
                    height="10"
                    rx="2"
                  ></rect></svg
                >
              </div>
              <span class="eyebrow">Administrative access</span>
              <h2 id="reset-password-title">Reset {resetUsername}'s password</h2>
              <p id="reset-password-description">
                Set a temporary replacement credential. The user can change it after signing in.
              </p>
            </header>
            <div class="modal-body">
              <div class="form-group-inline">
                <label for="reset-new-password">New Password</label>
                <input
                  id="reset-new-password"
                  type="password"
                  bind:value={resetNewPw}
                  placeholder="Min 6 characters"
                  on:keydown={(e) => {
                    if (e.key === "Enter") adminResetPassword();
                  }}
                />
                <span class="field-hint">Minimum 6 characters</span>
              </div>
            </div>
            <footer class="modal-actions">
              <button class="btn-secondary" on:click={() => (showResetPw = false)}>Cancel</button>
              <button class="btn-action" on:click={adminResetPassword} disabled={resettingPw}>
                {resettingPw ? "Resetting..." : "Reset Password"}
              </button>
            </footer>
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
              {#if slackLoading}
                <span class="status-inactive">Loading notification settings...</span>
              {:else if slackLoadFailed}
                <span class="status-inactive">Notification settings unavailable</span>
              {:else if slackConfigured}
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
                placeholder={slackConfigured
                  ? "Leave empty to keep current"
                  : "https://hooks.slack.com/services/T.../B.../xxx"}
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
                <button class="btn-action btn-clear" on:click={clearSlack}> Disconnect </button>
              {/if}
            </div>

            {#if slackTestResult}
              <div
                class="test-result"
                class:success={slackTestResult.ok}
                class:fail={!slackTestResult.ok}
              >
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
              {#if slackLoading}
                <span class="status-inactive">Loading notification settings...</span>
              {:else if slackLoadFailed}
                <span class="status-inactive">Notification settings unavailable</span>
              {:else if teamsConfigured}
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
                placeholder={teamsConfigured
                  ? "Leave empty to keep current"
                  : "https://your-org.webhook.office.com/webhookb2/..."}
              />
            </div>
            <div class="slack-actions">
              <button class="btn-action" on:click={saveSlackConfig} disabled={teamsSaving}>
                Save
              </button>
              {#if teamsConfigured}
                <button
                  class="btn-action btn-clear"
                  on:click={async () => {
                    await fetch("/api/settings/notifications", {
                      method: "PUT",
                      headers: { "Content-Type": "application/json", ...authHeaders() },
                      body: JSON.stringify({ teams_webhook: "__clear__" }),
                    });
                    // For now just reload
                    teamsWebhook = "";
                    await loadSlackConfig();
                    notify.success("Teams disconnected");
                  }}
                >
                  Disconnect
                </button>
              {/if}
            </div>
            <p class="auth-desc" style="margin-top: 12px; font-size: 11px; color: var(--text-dim)">
              Create a webhook in Teams: Channel Settings → Connectors → Incoming Webhook →
              Configure.
            </p>
          </div>
        </div>
      </section>

      <section class="section">
        <h2 class="section-title">SLA Monitoring</h2>
        <div class="info-card">
          <div class="auth-section">
            <p class="auth-desc">
              Set SLA deadlines per pipeline in the editor toolbar (click <strong>SLA</strong>). The
              checker runs every minute and alerts when a pipeline misses its deadline.
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
            <p class="auth-desc">
              Python Code nodes work with any <code>python3</code>. For faster processing:
            </p>
            <pre class="code-block">pip install pyarrow pandas</pre>
            <p class="auth-desc" style="margin-top: 8px">
              Recommended: use a virtualenv and set the path in Code node config:
            </p>
            <pre class="code-block">python3 -m venv ~/.brokoli-env
~/.brokoli-env/bin/pip install pyarrow pandas numpy requests</pre>
            <p class="auth-desc" style="margin-top: 8px; font-size: 11px; color: var(--text-dim)">
              Under 10K rows: JSON. Larger: CSV temp files (3-5x faster). With pyarrow: Arrow IPC
              (5-10x faster).
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
            <p class="auth-desc">
              Emit lineage events to DataHub, Marquez, or any OpenLineage-compatible endpoint:
            </p>
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
              Trigger pipeline runs via HTTP. Generate a webhook token in the pipeline editor (click <strong
                >Webhook</strong
              >), then:
            </p>
            <pre
              class="code-block">curl -X POST http://localhost:9900/api/pipelines/PIPELINE_ID/webhook?token=whk_...</pre>
            <p class="auth-desc" style="margin-top: 8px; font-size: 11px; color: var(--text-dim)">
              Useful for triggering on external events: git push, model deploy, dbt completion,
              Kafka consumer, etc.
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
  .settings-page {
    display: flex;
    min-width: 0;
    flex-direction: column;
    gap: 18px;
  }
  .identity-stats {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    margin: 0;
    background: color-mix(in srgb, var(--bg-primary), transparent 52%);
  }
  .identity-stats div {
    display: flex;
    min-width: 0;
    flex-direction: column;
    justify-content: center;
    gap: 5px;
    padding: 14px;
    border-right: 1px solid var(--border-subtle);
  }
  .identity-stats div:last-child {
    border-right: 0;
  }
  /* Used inside the reset-password modal header ("Administrative access") */
  .eyebrow {
    display: block;
    margin-bottom: 5px;
    color: var(--accent);
    font: 650 9px var(--font-mono);
    letter-spacing: 0.14em;
    text-transform: uppercase;
  }
  .identity-stats dt {
    color: var(--text-dim);
    font: 8px var(--font-mono);
    letter-spacing: 0.09em;
    text-transform: uppercase;
  }
  .identity-stats dd {
    overflow: hidden;
    margin: 0;
    color: var(--text-primary);
    font: 650 12px var(--font-mono);
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .identity-stats .attention dd {
    color: var(--accent);
  }

  /* ── Tab Bar ──
   * v1.5: "Tabs: text + 2px active underline. Do not turn tabs into a
   * row of green pills." This used to be a grid of individually
   * bordered cards with a gradient fill + inset shadow on the active
   * one -- replaced with a flat strip (one shared bottom border) and a
   * 2px underline + brand-signal text/icon color on the active tab.
   */
  .tab-bar {
    display: grid;
    grid-template-columns: repeat(5, minmax(0, 1fr));
    gap: 4px;
    border-bottom: 1px solid var(--border);
  }
  .tab-btn {
    display: grid;
    grid-template-columns: 34px minmax(0, 1fr) 12px;
    align-items: center;
    gap: 9px;
    min-width: 0;
    padding: 10px 11px;
    border: none;
    border-bottom: 2px solid transparent;
    background: transparent;
    color: inherit;
    text-align: left;
    transition:
      background 150ms ease,
      border-color 150ms ease;
  }
  .tab-btn:hover {
    background: var(--bg-tertiary);
  }
  .tab-btn.active {
    border-bottom-color: var(--bk-brand-signal);
  }
  .tab-icon {
    display: grid;
    width: 32px;
    height: 32px;
    place-items: center;
    border: 1px solid var(--border-subtle);
    border-radius: 7px;
    background: var(--bg-primary);
    color: var(--text-muted);
  }
  .tab-btn.active .tab-icon {
    border-color: color-mix(in srgb, var(--bk-brand-signal), transparent 58%);
    color: var(--bk-brand-signal);
  }
  .tab-copy {
    display: flex;
    min-width: 0;
    flex-direction: column;
    gap: 2px;
  }
  .tab-copy strong {
    overflow: hidden;
    color: var(--text-secondary);
    font-size: 11px;
    font-weight: 620;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .tab-btn.active .tab-copy strong {
    color: var(--bk-brand-signal);
  }
  .tab-copy small {
    overflow: hidden;
    color: var(--text-dim);
    font-size: 8.5px;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .tab-arrow {
    color: var(--text-dim);
    font-size: 17px;
  }
  .tab-btn.active .tab-arrow {
    color: var(--bk-brand-signal);
  }

  .tab-content {
    min-height: 400px;
  }

  /* ── Sections ── */
  .section {
    margin-bottom: 18px;
  }
  .section-title {
    margin: 0;
    padding: 15px 18px;
    border: 1px solid var(--border);
    border-bottom: 0;
    border-radius: 11px 11px 0 0;
    background: var(--bg-secondary);
    color: var(--text-secondary);
    font-size: 0.68rem;
    font-weight: 650;
    text-transform: uppercase;
    letter-spacing: 0.1em;
  }

  .info-card {
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    border-radius: 0 0 11px 11px;
    overflow: hidden;
    box-shadow: inset 0 1px 0 rgba(255, 255, 255, 0.02);
  }
  .info-row {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: var(--space-md) var(--space-lg);
    border-bottom: 1px solid var(--border);
  }
  .info-row:last-child {
    border-bottom: none;
  }
  .info-label {
    font-size: 0.8125rem;
    color: var(--text-secondary);
  }
  .info-value {
    font-size: 0.8125rem;
  }
  .mono {
    font-family: var(--font-mono);
  }
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
    font-size: 11px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    padding: 2px 8px;
    border-radius: 4px;
    background: var(--bg-tertiary);
    color: var(--text-muted);
  }
  .edition-badge.enterprise {
    background: var(--accent-glow);
    color: var(--accent);
  }
  .feature-tag {
    font-size: 10px;
    font-family: var(--font-mono);
    font-weight: 500;
    padding: 1px 6px;
    border-radius: 3px;
    background: var(--accent-glow);
    color: var(--accent);
    margin-right: 4px;
  }
  .code-block {
    font-family: var(--font-mono);
    font-size: 11px;
    line-height: 1.6;
    background: var(--bg-code);
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius-md);
    padding: 10px 14px;
    color: var(--text-secondary);
    white-space: pre;
    overflow-x: auto;
    margin: 0;
  }
  .auth-desc code {
    font-family: var(--font-mono);
    font-size: 12px;
    color: var(--accent);
    font-weight: 500;
  }
  .auth-desc {
    font-size: 0.8125rem;
    color: var(--text-secondary);
    margin-bottom: var(--space-md);
    line-height: 1.6;
  }

  .event-tags {
    display: flex;
    gap: 6px;
    margin-top: var(--space-sm);
  }
  .event-tag {
    font-family: var(--font-mono);
    font-size: 11px;
    font-weight: 500;
    padding: 2px 8px;
    border-radius: 4px;
    background: var(--bg-tertiary);
    color: var(--text-secondary);
  }
  .event-tag.alert {
    background: rgba(245, 158, 11, 0.1);
    color: var(--warning);
  }

  .btn-action {
    background: var(--accent);
    color: white;
    min-height: 34px;
    padding: 0 14px;
    border-radius: var(--radius-md);
    font-weight: 500;
    font-size: 0.8125rem;
    transition: background 150ms ease;
  }
  .btn-secondary {
    min-height: 34px;
    padding: 0 14px;
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    background: var(--bg-secondary);
    color: var(--text-secondary);
    font-size: 0.8125rem;
    font-weight: 500;
    transition: all 150ms ease;
  }
  .btn-secondary:hover {
    border-color: var(--border-hover);
    background: var(--bg-tertiary);
    color: var(--text-primary);
  }
  .btn-action:hover:not(:disabled) {
    background: var(--accent-hover);
  }
  .btn-action:disabled {
    opacity: 0.5;
  }

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
  .btn-copy:hover {
    color: var(--text-primary);
    background: var(--border);
  }

  .key-hint {
    font-size: 0.75rem;
    color: var(--text-muted);
  }
  .key-hint code {
    font-size: 0.6875rem;
  }

  .users-table {
    overflow: hidden;
  }
  .users-header,
  .users-row {
    display: grid;
    grid-template-columns: 1fr 100px 100px 80px;
    padding: var(--space-sm) var(--space-lg);
    align-items: center;
  }
  .col-actions {
    text-align: right;
  }
  .btn-reset-pw {
    padding: 3px 8px;
    border-radius: 4px;
    font-size: 10px;
    font-weight: 500;
    color: var(--text-dim);
    transition: all 150ms ease;
  }
  .btn-reset-pw:hover {
    color: var(--warning);
    background: rgba(245, 158, 11, 0.1);
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
  .user-handle {
    margin-left: 6px;
    color: var(--text-muted);
    font-family: var(--font-mono);
    font-size: 10px;
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
  .role-admin {
    color: var(--failed);
    background: var(--failed-bg);
  }
  .role-editor {
    color: var(--accent-text);
    background: var(--accent-glow);
  }
  .role-viewer {
    color: var(--text-muted);
    background: var(--pending-bg);
  }

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
  .form-select {
    flex: 0.7;
  }
  .role-help {
    font-size: 0.6875rem;
    color: var(--text-dim);
    margin-top: var(--space-sm);
    line-height: 1.6;
  }
  .empty-state {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 24px var(--space-lg);
    color: var(--text-muted);
  }
  .empty-state > div {
    display: flex;
    flex-direction: column;
    gap: 3px;
  }
  .empty-state strong {
    color: var(--text-secondary);
    font-size: 12px;
    font-weight: 600;
  }
  .empty-state span:not(.state-pulse) {
    font-size: 10px;
  }
  .state-pulse {
    width: 9px;
    height: 9px;
    flex: 0 0 auto;
    border-radius: 50%;
    background: var(--accent);
    box-shadow: 0 0 0 0 var(--accent-glow);
    animation: state-pulse 1.4s ease-out infinite;
  }
  @keyframes state-pulse {
    70%,
    100% {
      box-shadow: 0 0 0 8px transparent;
    }
  }

  .purge-controls {
    display: flex;
    align-items: center;
    gap: var(--space-sm);
  }
  .purge-label {
    font-size: 0.8125rem;
    color: var(--text-secondary);
  }
  /* ── Slack config ── */
  .status-active {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 12px;
    font-weight: 600;
    color: var(--success);
  }
  .status-dot-green {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--success);
    box-shadow: 0 0 6px var(--success-glow);
  }
  .status-inactive {
    font-size: 12px;
    color: var(--text-dim);
  }
  .slack-form {
    padding: var(--space-lg);
    border-top: 1px solid var(--border);
  }
  .form-group-inline {
    margin-bottom: 12px;
  }
  .form-group-inline label {
    display: block;
    font-size: 10px;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.08em;
    margin-bottom: 4px;
    font-weight: 600;
  }
  .form-group-inline input {
    width: 100%;
    padding: 9px 12px;
    background: var(--bg-primary);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    color: var(--text-primary);
    font-size: 13px;
    font-family: var(--font-ui);
  }
  .form-group-inline input:focus {
    border-color: var(--accent);
    outline: none;
  }
  .form-row-2 {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 12px;
  }
  .slack-actions {
    display: flex;
    gap: 8px;
    margin-top: 4px;
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
    border: 1px solid rgba(239, 68, 68, 0.2);
  }
  .btn-clear:hover {
    background: var(--failed-bg) !important;
  }
  .test-result {
    margin-top: 12px;
    padding: 10px 14px;
    border-radius: var(--radius-md);
    font-size: 12px;
    font-weight: 500;
  }
  .test-result.success {
    background: var(--success-bg);
    color: var(--success);
    border: 1px solid rgba(34, 197, 94, 0.2);
  }
  .test-result.fail {
    background: var(--failed-bg);
    color: var(--failed);
    border: 1px solid rgba(239, 68, 68, 0.2);
  }
  .slack-events {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 12px var(--space-lg);
    border-top: 1px solid var(--border);
  }
  .events-label {
    font-size: 11px;
    color: var(--text-dim);
  }

  .purge-input {
    width: 60px;
    font-family: var(--font-mono);
    font-size: 0.8125rem;
    padding: 4px 8px;
    text-align: center;
  }

  /* ── Roles ── */
  .tab-header-row {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: var(--space-md);
  }
  .roles-list {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .role-card {
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    overflow: hidden;
    transition: border-color 150ms ease;
  }
  .role-card.editing {
    border-color: var(--accent);
  }
  .role-card-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 14px 20px;
  }
  .role-card-info {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .role-card-name {
    font-size: 14px;
    font-weight: 600;
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .role-card-desc {
    font-size: 11px;
    color: var(--text-muted);
  }
  .system-badge {
    font-size: 9px;
    font-weight: 600;
    text-transform: uppercase;
    padding: 1px 6px;
    border-radius: 3px;
    background: var(--bg-tertiary);
    color: var(--text-dim);
    letter-spacing: 0.06em;
  }
  .role-card-meta {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .perm-count {
    font-size: 11px;
    color: var(--text-dim);
    font-family: var(--font-mono);
  }
  .btn-sm-action {
    padding: 5px 14px;
    border-radius: 5px;
    font-size: 11px;
    font-weight: 500;
    background: var(--bg-tertiary);
    border: 1px solid var(--border);
    color: var(--text-secondary);
    transition: all 150ms ease;
  }
  .btn-sm-action:hover {
    border-color: var(--accent);
    color: var(--accent-text);
  }
  .btn-sm-danger {
    padding: 5px 14px;
    border-radius: 5px;
    font-size: 11px;
    font-weight: 500;
    color: var(--text-dim);
    transition: all 150ms ease;
  }
  .btn-sm-danger:hover {
    color: var(--failed);
    background: var(--failed-bg);
  }
  .btn-secondary-sm {
    padding: 6px 14px;
    border-radius: var(--radius-md);
    font-size: 12px;
    background: var(--bg-tertiary);
    color: var(--text-secondary);
    border: 1px solid var(--border);
  }

  /* Permission editor */
  .perm-editor {
    padding: 20px 24px;
    border-top: 1px solid var(--border);
    background: var(--bg-primary);
  }
  .perm-group {
    margin-bottom: 20px;
    padding: 14px 16px;
    background: var(--bg-secondary);
    border: 1px solid var(--border-subtle);
    border-radius: 8px;
  }
  .perm-group:last-child {
    margin-bottom: 0;
  }
  .perm-group-title {
    display: block;
    font-size: 11px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--text-muted);
    margin-bottom: 10px;
    padding-bottom: 8px;
    border-bottom: 1px solid var(--border-subtle);
  }
  .perm-checkboxes {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
    gap: 6px;
  }
  .perm-checkbox {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 12px;
    color: var(--text-secondary);
    cursor: pointer;
    padding: 6px 10px;
    border-radius: 6px;
    transition: background 120ms ease;
  }
  .perm-checkbox:hover {
    background: var(--bg-tertiary);
  }

  /* Custom toggle switch */
  .perm-checkbox input[type="checkbox"] {
    appearance: none;
    -webkit-appearance: none;
    width: 32px;
    height: 18px;
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
    top: 2px;
    left: 2px;
    width: 12px;
    height: 12px;
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
    opacity: 0.4;
    cursor: default;
  }
  .perm-label {
    text-transform: capitalize;
    user-select: none;
  }
  .perm-actions {
    display: flex;
    gap: 8px;
    margin-top: 20px;
    padding-top: 14px;
    border-top: 1px solid var(--border-subtle);
  }

  .modal-overlay {
    position: fixed;
    inset: 0;
    background: rgba(0, 4, 6, 0.8);
    backdrop-filter: blur(10px) saturate(0.75);
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 16px;
    z-index: 2000;
    animation: modal-fade 180ms ease-out;
  }
  @keyframes modal-fade {
    from {
      opacity: 0;
    }
    to {
      opacity: 1;
    }
  }
  .modal {
    position: relative;
    border: 1px solid var(--border);
    border-radius: 13px;
    padding: 0;
    width: min(520px, 100%);
    max-width: none;
    max-height: calc(100dvh - 32px);
    overflow-y: auto;
    background:
      radial-gradient(circle at 4% 0%, var(--accent-glow), transparent 30%), var(--bg-secondary);
    box-shadow:
      0 36px 100px rgba(0, 0, 0, 0.65),
      inset 0 1px 0 rgba(255, 255, 255, 0.035);
  }
  .modal-close {
    position: absolute;
    top: 12px;
    right: 12px;
    display: grid;
    width: 30px;
    height: 30px;
    z-index: 2;
    place-items: center;
    border-radius: 6px;
    color: var(--text-muted);
    font-size: 19px;
  }
  .modal-close:hover {
    background: var(--bg-tertiary);
    color: var(--text-primary);
  }
  .modal-header {
    padding: 28px 30px 21px;
    border-bottom: 1px solid var(--border-subtle);
  }
  .modal-symbol {
    display: grid;
    width: 42px;
    height: 42px;
    place-items: center;
    margin-bottom: 22px;
    border: 1px solid color-mix(in srgb, var(--accent), transparent 58%);
    border-radius: 10px;
    background: var(--accent-glow);
    color: var(--accent);
  }
  .modal-symbol svg {
    width: 19px;
    fill: none;
    stroke: currentColor;
    stroke-linecap: round;
    stroke-linejoin: round;
    stroke-width: 1.6;
  }
  .modal-header h2 {
    margin: 6px 0;
    color: var(--text-primary);
    font-size: 21px;
    font-weight: 680;
    letter-spacing: -0.035em;
  }
  .modal-header p {
    max-width: 410px;
    margin: 0;
    color: var(--text-muted);
    font-size: 10px;
    line-height: 1.6;
  }
  .modal-body {
    padding: 21px 30px 9px;
  }
  .field-hint {
    display: block;
    margin-top: 5px;
    color: var(--text-dim);
    font-size: 9px;
  }
  .modal-actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    margin: 0;
    padding: 14px 30px;
    border-top: 1px solid var(--border-subtle);
    background: color-mix(in srgb, var(--bg-primary), transparent 48%);
  }
  .modal-role {
    width: 640px;
    max-width: 95vw;
    max-height: 85vh;
    display: flex;
    flex-direction: column;
    padding: 0;
    overflow: hidden;
  }
  .modal-role-header {
    padding: 24px 28px 16px;
    border-bottom: 1px solid var(--border);
    flex-shrink: 0;
  }
  .modal-role-header h2 {
    margin-bottom: 16px;
  }
  .modal-role-body {
    flex: 1;
    overflow-y: auto;
    min-height: 0;
  }
  .modal-role-body .perm-editor {
    padding: 16px 28px 20px;
  }
  .modal-role-footer {
    padding: 14px 28px;
    border-top: 1px solid var(--border);
    background: var(--bg-secondary);
    flex-shrink: 0;
    display: flex;
    justify-content: space-between;
    align-items: center;
  }
  .perm-selected-count {
    font-size: 11px;
    font-family: var(--font-mono);
    color: var(--text-dim);
  }

  @media (max-width: 1100px) {
    .tab-bar {
      grid-template-columns: repeat(3, minmax(0, 1fr));
    }
  }

  @media (max-width: 768px) {
    .tab-bar {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }
    .info-row {
      gap: 16px;
      padding: 11px 14px;
    }
    .info-value {
      min-width: 0;
      overflow-wrap: anywhere;
      text-align: right;
    }
    .users-header {
      display: none;
    }
    .users-row {
      grid-template-columns: minmax(0, 1fr) auto;
      gap: 8px 14px;
      padding: 13px 14px;
    }
    .users-row .col-user {
      grid-column: 1;
      grid-row: 1;
    }
    .users-row .col-actions {
      grid-column: 2;
      grid-row: 1;
    }
    .users-row .col-role {
      grid-column: 1;
      grid-row: 2;
    }
    .users-row .col-created {
      grid-column: 2;
      grid-row: 2;
      color: var(--text-muted);
      font-size: 11px;
      text-align: right;
    }
    .form-row {
      display: grid;
      grid-template-columns: 1fr 1fr;
    }
    .form-row .btn-action {
      width: 100%;
    }
    .code-block {
      max-width: 100%;
    }
  }

  @media (max-width: 520px) {
    .settings-page {
      gap: 12px;
    }
    .identity-stats {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }
    .identity-stats div {
      min-height: 58px;
      box-sizing: border-box;
      padding: 10px 14px;
      border-bottom: 1px solid var(--border-subtle);
    }
    .identity-stats div:nth-child(3),
    .identity-stats div:nth-child(4) {
      border-bottom: 0;
    }
    .tab-bar {
      grid-template-columns: 1fr;
      gap: 5px;
    }
    .tab-btn {
      min-height: 48px;
      padding: 7px 10px;
    }
    .section-title {
      padding: 13px 14px;
    }
    .auth-section,
    .slack-form {
      padding: 14px;
    }
    .form-row,
    .form-row-2 {
      grid-template-columns: 1fr;
    }
    .form-input,
    .form-select {
      width: 100%;
    }
    .purge-controls,
    .slack-actions,
    .slack-events {
      align-items: stretch;
      flex-wrap: wrap;
    }
    .purge-controls .btn-action {
      flex-basis: 100%;
    }
    .slack-actions .btn-action {
      flex: 1 1 auto;
    }
    .key-display {
      align-items: flex-start;
      flex-direction: column;
    }
    .key-display .btn-copy {
      align-self: flex-end;
    }
    .modal-overlay {
      align-items: flex-end;
      padding: 8px;
    }
    .modal {
      max-height: calc(100dvh - 16px);
    }
    .modal-header,
    .modal-body,
    .modal-actions {
      padding-right: 20px;
      padding-left: 20px;
    }
    .modal-symbol {
      margin-bottom: 16px;
    }
    .modal-actions button {
      flex: 1;
    }
  }
</style>
