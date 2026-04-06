<script lang="ts">
  import { icons } from "../lib/icons";
  import { theme, toggleTheme } from "../lib/theme";
  import { authUser, logout } from "../lib/auth";
  import { wsConnected } from "../lib/ws";
  export let currentPath: string = "/";

  const nav = [
    { path: "/", label: "Dashboard", icon: icons.dashboard },
    { path: "/pipelines", label: "Pipelines", icon: icons.pipeline },
    { path: "/calendar", label: "Calendar", icon: icons.calendar },
    { path: "/lineage", label: "Lineage", icon: icons.lineage },
    { path: "/variables", label: "Variables", icon: icons.variable },
    { path: "/connections", label: "Connections", icon: icons.connection },
    { path: "/settings", label: "Settings", icon: icons.settings },
  ];

  function renderIcon(icon: typeof icons.dashboard) {
    return icon;
  }
</script>

<aside class="sidebar">
  <div class="logo">
    <div class="logo-mark">
      <!-- Brokoli logo — broccoli floret as data node graph -->
      <svg width="22" height="22" viewBox="0 0 32 32" fill="none">
        <!-- Stem -->
        <path d="M16 19v9" stroke="#4ade80" stroke-width="2.5" stroke-linecap="round"/>
        <path d="M14 22l2-3 2 3" stroke="#4ade80" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" opacity="0.5"/>
        <!-- Crown nodes (3 connected florets = pipeline nodes) -->
        <circle cx="16" cy="11" r="4.5" fill="#0d9488"/>
        <circle cx="9" cy="7" r="3.5" fill="#16a34a"/>
        <circle cx="23" cy="7" r="3.5" fill="#16a34a"/>
        <circle cx="6" cy="2" r="2.5" fill="#22c55e"/>
        <circle cx="16" cy="2" r="3" fill="#22c55e"/>
        <circle cx="26" cy="2" r="2.5" fill="#22c55e"/>
        <!-- Connection lines (data flow between nodes) -->
        <line x1="9" y1="7" x2="16" y2="11" stroke="#0d9488" stroke-width="1" opacity="0.4"/>
        <line x1="23" y1="7" x2="16" y2="11" stroke="#0d9488" stroke-width="1" opacity="0.4"/>
        <line x1="6" y1="2" x2="9" y2="7" stroke="#16a34a" stroke-width="1" opacity="0.3"/>
        <line x1="16" y1="2" x2="9" y2="7" stroke="#16a34a" stroke-width="1" opacity="0.3"/>
        <line x1="16" y1="2" x2="23" y2="7" stroke="#16a34a" stroke-width="1" opacity="0.3"/>
        <line x1="26" y1="2" x2="23" y2="7" stroke="#16a34a" stroke-width="1" opacity="0.3"/>
      </svg>
    </div>
    <div class="logo-text">
      <span class="logo-name">Brokoli</span>
      <span class="logo-sub">orchestrator</span>
    </div>
  </div>

  <nav>
    {#each nav as item}
      <a
        href="#{item.path}"
        class="nav-item"
        class:active={currentPath === item.path}
      >
        <svg class="nav-icon" width="18" height="18" viewBox="0 0 24 24" fill="none">
          <path
            d={item.icon.d}
            stroke="currentColor"
            stroke-width="1.5"
            stroke-linecap="round"
            stroke-linejoin="round"
          />
        </svg>
        <span>{item.label}</span>
      </a>
    {/each}
  </nav>

  <div class="sidebar-footer">
    <!-- User info -->
    {#if $authUser}
      <div class="user-info">
        <div class="user-avatar">
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none">
            <path d={icons.user.d} stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
          </svg>
        </div>
        <div class="user-details">
          <span class="user-name">{$authUser.username}</span>
          <span class="user-role">{$authUser.role}</span>
        </div>
        <button class="logout-btn" on:click={logout} title="Sign out">
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none">
            <path d={icons.logout.d} stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
          </svg>
        </button>
      </div>
    {:else}
      <div class="user-info open-mode">
        <span class="status-dot open"></span>
        <span class="open-label">Open Mode</span>
      </div>
    {/if}

    <div class="footer-row">
      <div class="server-status">
        <span class="status-dot" class:disconnected={!$wsConnected}></span>
        <span>{$wsConnected ? "Connected" : "Reconnecting..."}</span>
      </div>
      <button class="theme-toggle" on:click={toggleTheme} title="Toggle theme">
        {#if $theme === "dark"}
          <svg width="15" height="15" viewBox="0 0 24 24" fill="none">
            <circle cx="12" cy="12" r="5" stroke="currentColor" stroke-width="1.5" />
            <path d="M12 1v2M12 21v2M4.22 4.22l1.42 1.42M18.36 18.36l1.42 1.42M1 12h2M21 12h2M4.22 19.78l1.42-1.42M18.36 5.64l1.42-1.42" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" />
          </svg>
        {:else}
          <svg width="15" height="15" viewBox="0 0 24 24" fill="none">
            <path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
          </svg>
        {/if}
      </button>
    </div>
    <span class="version">v0.1.0</span>
  </div>
</aside>

<style>
  .sidebar {
    width: var(--sidebar-width);
    height: 100vh;
    background: var(--bg-sidebar);
    border-right: 1px solid var(--border-sidebar);
    display: flex;
    flex-direction: column;
    flex-shrink: 0;
    transition: background 200ms ease, border-color 200ms ease;
  }

  .logo {
    padding: 20px 20px 18px;
    border-bottom: 1px solid var(--border-sidebar);
    display: flex;
    align-items: center;
    gap: 10px;
  }
  .logo-mark {
    width: 32px; height: 32px;
    display: flex; align-items: center; justify-content: center;
    background: var(--accent-glow);
    border-radius: 8px;
  }
  .logo-text { display: flex; flex-direction: column; }
  .logo-name {
    font-size: 15px; font-weight: 700;
    color: var(--text-primary);
    letter-spacing: -0.02em; line-height: 1;
  }
  .logo-sub {
    font-size: 9px; color: var(--text-dim);
    text-transform: uppercase; letter-spacing: 0.12em; margin-top: 2px;
  }

  .ws-switcher {
    padding: 0 12px 8px;
  }
  .ws-select {
    width: 100%; padding: 6px 10px;
    background: var(--bg-tertiary); border: 1px solid var(--border-subtle);
    border-radius: 6px; color: var(--text-primary);
    font-size: 12px; font-weight: 500;
    font-family: var(--font-ui);
    cursor: pointer;
    transition: border-color 150ms ease;
  }
  .ws-select:hover { border-color: var(--border-hover); }
  .ws-select:focus { border-color: var(--accent); outline: none; }

  nav {
    flex: 1; padding: 12px 10px;
    display: flex; flex-direction: column; gap: 1px;
  }

  .nav-item {
    display: flex; align-items: center; gap: 10px;
    padding: 9px 12px; border-radius: 6px;
    color: var(--text-muted); font-size: 13px; font-weight: 500;
    transition: all 150ms ease; text-decoration: none;
  }
  .nav-item:hover {
    color: var(--text-secondary);
    background: var(--bg-tertiary);
  }
  .nav-item.active {
    color: var(--text-primary);
    background: var(--accent-glow);
  }
  .nav-item.active .nav-icon { color: var(--accent); }
  .nav-icon { flex-shrink: 0; }

  .sidebar-footer {
    padding: 10px 20px 14px;
    border-top: 1px solid var(--border-sidebar);
    display: flex; flex-direction: column; gap: 6px;
  }

  /* User info section */
  .user-info {
    display: flex; align-items: center; gap: 8px;
    padding: 6px 0;
  }
  .user-avatar {
    width: 28px; height: 28px;
    display: flex; align-items: center; justify-content: center;
    background: var(--accent-glow);
    border-radius: 6px;
    color: var(--accent);
    flex-shrink: 0;
  }
  .user-details {
    display: flex; flex-direction: column;
    flex: 1; min-width: 0;
  }
  .user-name {
    font-size: 12px; font-weight: 600;
    color: var(--text-primary);
    line-height: 1.2;
    overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  }
  .user-role {
    font-family: var(--font-mono);
    font-size: 9px;
    color: var(--text-dim);
    text-transform: uppercase;
    letter-spacing: 0.06em;
    line-height: 1.2;
  }
  .logout-btn {
    display: flex; align-items: center; justify-content: center;
    width: 28px; height: 28px; border-radius: 6px;
    color: var(--text-muted);
    transition: all 150ms ease;
    flex-shrink: 0;
  }
  .logout-btn:hover {
    color: var(--failed);
    background: var(--failed-bg);
  }

  .user-info.open-mode {
    gap: 6px;
    font-size: 11px;
    color: var(--text-dim);
  }
  .open-label {
    font-size: 11px;
    color: var(--text-dim);
  }

  .footer-row {
    display: flex; justify-content: space-between; align-items: center;
  }
  .server-status {
    display: flex; align-items: center; gap: 6px;
    font-size: 11px; color: var(--text-dim);
  }
  .status-dot {
    width: 5px; height: 5px; border-radius: 50%;
    background: var(--success);
    box-shadow: 0 0 6px var(--success-glow);
    transition: background 300ms ease;
  }
  .status-dot.disconnected {
    background: var(--warning);
    box-shadow: 0 0 6px rgba(245, 158, 11, 0.4);
    animation: pulse-warn 1.5s ease-in-out infinite;
  }
  @keyframes pulse-warn {
    0%, 100% { opacity: 1; }
    50% { opacity: 0.4; }
  }
  .status-dot.open {
    background: var(--accent);
    box-shadow: 0 0 6px var(--accent-glow);
  }
  .version {
    font-family: var(--font-mono); font-size: 10px; color: var(--text-ghost);
  }

  .theme-toggle {
    display: flex; align-items: center; justify-content: center;
    width: 28px; height: 28px; border-radius: 6px;
    color: var(--text-muted);
    transition: all 150ms ease;
  }
  .theme-toggle:hover {
    color: var(--text-primary);
    background: var(--bg-tertiary);
  }

  /* ── Tablet: icon-only sidebar ── */
  @media (max-width: 1024px) {
    .sidebar { width: 60px; overflow: hidden; }
    .logo-text, .logo-sub { display: none; }
    .logo { padding: 16px 12px; justify-content: center; }
    .nav-item span { display: none; }
    .nav-item { justify-content: center; padding: 10px; }
    .ws-switcher { display: none; }
    .user-details { display: none; }
    .user-info { justify-content: center; }
    .logout-btn { display: none; }
    .footer-row { justify-content: center; }
    .server-status span { display: none; }
    .version { display: none; }
    .open-label { display: none; }
  }

  /* ── Mobile: hide sidebar completely ── */
  @media (max-width: 768px) {
    .sidebar { display: none; }
  }
</style>
