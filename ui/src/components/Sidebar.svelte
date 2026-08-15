<script lang="ts">
  import { onMount } from "svelte";
  import { icons } from "../lib/icons";
  import { theme, toggleTheme } from "../lib/theme";
  import { authUser, logout, userLabel } from "../lib/auth";
  import { wsConnected } from "../lib/sodp";
  import { sidebarCollapsed, sidebarGroups, toggleSidebar, toggleGroup } from "../lib/sidebar";
  export let currentPath: string = "/";

  // DRIFT-CHECK CONTRACT: every nav path below must stay a literal
  // `path: "/x"` on a single line — never computed, never a shared
  // constant. scripts/check-overlay-drift.sh in the enterprise repo greps
  // these literals to verify the overlay copy carries every core route.
  // Settings lives in the footer utility cluster, not this array; its
  // drift-check literal is this comment: path: "/settings"
  const navSections = [
    {
      id: "core",
      label: "",
      items: [
        { path: "/", label: "Dashboard", icon: icons.dashboard },
        { path: "/pipelines", label: "Pipelines", icon: icons.pipeline },
        { path: "/calendar", label: "Calendar", icon: icons.calendar },
        { path: "/lineage", label: "Lineage", icon: icons.lineage },
        { path: "/dependencies", label: "Dependencies", icon: icons.dependency },
      ],
    },
    {
      id: "data",
      label: "Data",
      items: [
        { path: "/variables", label: "Variables", icon: icons.variable },
        { path: "/connections", label: "Connections", icon: icons.connection },
        { path: "/plugins", label: "Plugins", icon: icons.plugin },
      ],
    },
  ];

  // Below 1025px the rail is forced (matching the old media-query behavior);
  // the stored preference only applies on wide viewports, and the toggle
  // control hides while the rail is forced.
  let viewportNarrow = false;
  onMount(() => {
    const mq = window.matchMedia("(max-width: 1024px)");
    viewportNarrow = mq.matches;
    const onChange = (e: MediaQueryListEvent) => (viewportNarrow = e.matches);
    mq.addEventListener("change", onChange);
    return () => mq.removeEventListener("change", onChange);
  });

  $: collapsed = $sidebarCollapsed || viewportNarrow;

  function isRouteActive(path: string) {
    return path === "/"
      ? currentPath === path
      : currentPath === path || currentPath.startsWith(`${path}/`);
  }

  function groupExpanded(id: string, groups: Record<string, boolean>) {
    return groups[id] !== false;
  }
</script>

<aside class="sidebar" class:collapsed>
  <div class="brand-zone">
    <div class="logo">
      <div class="logo-mark" title="Brokoli · v0.1.0">
        <!-- Brokoli logo — broccoli floret as data node graph -->
        <svg width="22" height="22" viewBox="0 0 32 32" fill="none">
          <path d="M16 19v9" stroke="#4ade80" stroke-width="2.5" stroke-linecap="round" />
          <path
            d="M14 22l2-3 2 3"
            stroke="#4ade80"
            stroke-width="1.5"
            stroke-linecap="round"
            stroke-linejoin="round"
            opacity="0.5"
          />
          <circle cx="16" cy="11" r="4.5" fill="#0d9488" />
          <circle cx="9" cy="7" r="3.5" fill="#16a34a" />
          <circle cx="23" cy="7" r="3.5" fill="#16a34a" />
          <circle cx="6" cy="2" r="2.5" fill="#22c55e" />
          <circle cx="16" cy="2" r="3" fill="#22c55e" />
          <circle cx="26" cy="2" r="2.5" fill="#22c55e" />
          <line x1="9" y1="7" x2="16" y2="11" stroke="#0d9488" stroke-width="1" opacity="0.4" />
          <line x1="23" y1="7" x2="16" y2="11" stroke="#0d9488" stroke-width="1" opacity="0.4" />
          <line x1="6" y1="2" x2="9" y2="7" stroke="#16a34a" stroke-width="1" opacity="0.3" />
          <line x1="16" y1="2" x2="9" y2="7" stroke="#16a34a" stroke-width="1" opacity="0.3" />
          <line x1="16" y1="2" x2="23" y2="7" stroke="#16a34a" stroke-width="1" opacity="0.3" />
          <line x1="26" y1="2" x2="23" y2="7" stroke="#16a34a" stroke-width="1" opacity="0.3" />
        </svg>
      </div>
      <div class="logo-text">
        <span class="logo-name">Brokoli</span>
        <span class="logo-sub">orchestrator · v0.1.0</span>
      </div>
    </div>
    {#if !viewportNarrow}
      <button
        class="collapse-toggle"
        on:click={toggleSidebar}
        aria-expanded={!collapsed}
        aria-controls="sidebar-nav"
        aria-label={collapsed ? "Expand sidebar" : "Collapse sidebar"}
        title={collapsed ? "Expand sidebar" : "Collapse sidebar"}
      >
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" class:flipped={collapsed}>
          <path
            d="M15 6l-6 6 6 6"
            stroke="currentColor"
            stroke-width="1.5"
            stroke-linecap="round"
            stroke-linejoin="round"
          />
        </svg>
      </button>
    {/if}
  </div>

  <nav id="sidebar-nav">
    {#each navSections as section (section.id)}
      {#if section.label}
        {#if collapsed}
          <div class="group-divider" role="presentation"></div>
        {:else}
          <button
            class="group-header"
            on:click={() => toggleGroup(section.id)}
            aria-expanded={groupExpanded(section.id, $sidebarGroups)}
            aria-controls="group-{section.id}"
          >
            <span>{section.label}</span>
            <svg
              width="12"
              height="12"
              viewBox="0 0 24 24"
              fill="none"
              class="group-chevron"
              class:open={groupExpanded(section.id, $sidebarGroups)}
            >
              <path
                d="M9 6l6 6-6 6"
                stroke="currentColor"
                stroke-width="1.5"
                stroke-linecap="round"
                stroke-linejoin="round"
              />
            </svg>
          </button>
        {/if}
      {/if}
      <div id="group-{section.id}" class="group-items">
        {#if collapsed || !section.label || groupExpanded(section.id, $sidebarGroups)}
          {#each section.items as item (item.path)}
            <a
              href="#{item.path}"
              class="nav-item"
              class:active={isRouteActive(item.path)}
              title={item.label}
              aria-label={item.label}
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
              <span class="nav-label">{item.label}</span>
            </a>
          {/each}
        {/if}
      </div>
    {/each}
  </nav>

  <div class="sidebar-footer">
    <a
      href="#/settings"
      class="nav-item utility-link"
      class:active={isRouteActive("/settings")}
      title="Settings"
      aria-label="Settings"
    >
      <svg class="nav-icon" width="18" height="18" viewBox="0 0 24 24" fill="none">
        <path
          d={icons.settings.d}
          stroke="currentColor"
          stroke-width="1.5"
          stroke-linecap="round"
          stroke-linejoin="round"
        />
      </svg>
      <span class="nav-label">Settings</span>
    </a>

    {#if $authUser}
      <div class="user-info">
        <div class="user-avatar">
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none">
            <path
              d={icons.user.d}
              stroke="currentColor"
              stroke-width="1.5"
              stroke-linecap="round"
              stroke-linejoin="round"
            />
          </svg>
        </div>
        <div class="user-details">
          <span class="user-name">{userLabel($authUser)}</span>
          <span class="user-role">{$authUser.role}</span>
        </div>
        <button class="logout-btn" on:click={logout} title="Sign out" aria-label="Sign out">
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none">
            <path
              d={icons.logout.d}
              stroke="currentColor"
              stroke-width="1.5"
              stroke-linecap="round"
              stroke-linejoin="round"
            />
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
      <div class="server-status" title={$wsConnected ? "Connected" : "Reconnecting..."}>
        <span class="status-dot" class:disconnected={!$wsConnected}></span>
        <span>{$wsConnected ? "Connected" : "Reconnecting..."}</span>
      </div>
      <button
        class="theme-toggle"
        on:click={toggleTheme}
        title="Toggle theme"
        aria-label="Toggle theme"
      >
        {#if $theme === "dark"}
          <svg width="15" height="15" viewBox="0 0 24 24" fill="none">
            <circle cx="12" cy="12" r="5" stroke="currentColor" stroke-width="1.5" />
            <path
              d="M12 1v2M12 21v2M4.22 4.22l1.42 1.42M18.36 18.36l1.42 1.42M1 12h2M21 12h2M4.22 19.78l1.42-1.42M18.36 5.64l1.42-1.42"
              stroke="currentColor"
              stroke-width="1.5"
              stroke-linecap="round"
            />
          </svg>
        {:else}
          <svg width="15" height="15" viewBox="0 0 24 24" fill="none">
            <path
              d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"
              stroke="currentColor"
              stroke-width="1.5"
              stroke-linecap="round"
              stroke-linejoin="round"
            />
          </svg>
        {/if}
      </button>
    </div>
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
    overflow: hidden;
    transition:
      width 200ms ease,
      background 200ms ease,
      border-color 200ms ease;
  }
  @media (prefers-reduced-motion: reduce) {
    .sidebar {
      transition: none;
    }
  }

  .brand-zone {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 16px 12px 14px 20px;
    border-bottom: 1px solid var(--border-sidebar);
  }
  .logo {
    display: flex;
    align-items: center;
    gap: 10px;
    min-width: 0;
  }
  .logo-mark {
    width: 32px;
    height: 32px;
    display: flex;
    align-items: center;
    justify-content: center;
    background: var(--accent-glow);
    border-radius: 8px;
    flex-shrink: 0;
  }
  .logo-text {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  .logo-name {
    font-size: 15px;
    font-weight: 700;
    color: var(--text-primary);
    letter-spacing: -0.02em;
    line-height: 1;
  }
  .logo-sub {
    font-size: 9px;
    color: var(--text-dim);
    text-transform: uppercase;
    letter-spacing: 0.12em;
    margin-top: 2px;
    white-space: nowrap;
  }

  .collapse-toggle {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 24px;
    height: 24px;
    border-radius: 5px;
    color: var(--text-dim);
    flex-shrink: 0;
    transition: all 150ms ease;
  }
  .collapse-toggle:hover {
    color: var(--text-primary);
    background: var(--bg-tertiary);
  }
  .collapse-toggle:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: 1px;
  }
  .collapse-toggle svg {
    transition: transform 200ms ease;
  }
  .collapse-toggle svg.flipped {
    transform: rotate(180deg);
  }

  nav {
    flex: 1;
    padding: 12px 10px;
    display: flex;
    flex-direction: column;
    gap: 1px;
    overflow-y: auto;
  }

  .group-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    width: 100%;
    padding: 10px 12px 4px;
    color: var(--text-dim);
    font-size: 10px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.1em;
    transition: color 150ms ease;
  }
  .group-header:hover {
    color: var(--text-secondary);
  }
  .group-header:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: 1px;
    border-radius: 4px;
  }
  .group-chevron {
    transition: transform 150ms ease;
  }
  .group-chevron.open {
    transform: rotate(90deg);
  }
  .group-divider {
    height: 1px;
    margin: 8px 10px;
    background: var(--border-sidebar);
  }
  .group-items {
    display: flex;
    flex-direction: column;
    gap: 1px;
  }

  .nav-item {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 9px 12px;
    border-radius: 6px;
    color: var(--text-muted);
    font-size: 13px;
    font-weight: 500;
    transition: all 150ms ease;
    text-decoration: none;
  }
  .nav-item:hover {
    color: var(--text-secondary);
    background: var(--bg-tertiary);
  }
  .nav-item:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: -2px;
  }
  .nav-item.active {
    color: var(--text-primary);
    background: var(--accent-glow);
  }
  .nav-item.active .nav-icon {
    color: var(--accent);
  }
  .nav-icon {
    flex-shrink: 0;
  }
  .nav-label {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .sidebar-footer {
    padding: 10px 12px 14px;
    border-top: 1px solid var(--border-sidebar);
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .utility-link {
    margin-bottom: 2px;
  }

  .user-info {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 6px 8px 6px 12px;
  }
  .user-avatar {
    width: 28px;
    height: 28px;
    display: flex;
    align-items: center;
    justify-content: center;
    background: var(--accent-glow);
    border-radius: 6px;
    color: var(--accent);
    flex-shrink: 0;
  }
  .user-details {
    display: flex;
    flex-direction: column;
    flex: 1;
    min-width: 0;
  }
  .user-name {
    font-size: 12px;
    font-weight: 600;
    color: var(--text-primary);
    line-height: 1.2;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
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
    display: flex;
    align-items: center;
    justify-content: center;
    width: 28px;
    height: 28px;
    border-radius: 6px;
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
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 0 8px 0 12px;
  }
  .server-status {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 11px;
    color: var(--text-dim);
  }
  .status-dot {
    width: 5px;
    height: 5px;
    border-radius: 50%;
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
    0%,
    100% {
      opacity: 1;
    }
    50% {
      opacity: 0.4;
    }
  }
  .status-dot.open {
    background: var(--accent);
    box-shadow: 0 0 6px var(--accent-glow);
  }

  .theme-toggle {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 28px;
    height: 28px;
    border-radius: 6px;
    color: var(--text-muted);
    transition: all 150ms ease;
  }
  .theme-toggle:hover {
    color: var(--text-primary);
    background: var(--bg-tertiary);
  }

  /* ── Collapsed rail: one class-driven ruleset (user toggle OR narrow viewport) ── */
  .sidebar.collapsed .brand-zone {
    flex-direction: column;
    gap: 8px;
    padding: 14px 8px 12px;
  }
  .sidebar.collapsed .logo {
    justify-content: center;
  }
  .sidebar.collapsed .logo-text {
    display: none;
  }
  .sidebar.collapsed .group-header {
    display: none;
  }
  .sidebar.collapsed .nav-item {
    justify-content: center;
    padding: 10px;
  }
  .sidebar.collapsed .nav-label {
    display: none;
  }
  .sidebar.collapsed .user-details,
  .sidebar.collapsed .logout-btn {
    display: none;
  }
  .sidebar.collapsed .user-info {
    justify-content: center;
    padding: 6px 0;
  }
  .sidebar.collapsed .footer-row {
    flex-direction: column;
    gap: 8px;
    padding: 0;
  }
  .sidebar.collapsed .server-status span {
    display: none;
  }
  .sidebar.collapsed .open-label {
    display: none;
  }

  /* ── Mobile: hide sidebar completely (Layout renders the mobile nav) ── */
  @media (max-width: 768px) {
    .sidebar {
      display: none;
    }
  }
</style>
