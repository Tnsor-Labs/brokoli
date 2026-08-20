<script lang="ts">
  import { onMount } from "svelte";
  import BrandIcon from "./BrandIcon.svelte";
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
        { path: "/", label: "Dashboard" },
        { path: "/pipelines", label: "Pipelines" },
        { path: "/calendar", label: "Calendar" },
        { path: "/lineage", label: "Lineage" },
        { path: "/dependencies", label: "Dependencies" },
      ],
    },
    {
      id: "data",
      label: "Data",
      items: [
        { path: "/variables", label: "Variables" },
        { path: "/connections", label: "Connections" },
        { path: "/plugins", label: "Plugins" },
      ],
    },
    {
      id: "platform",
      label: "Platform",
      items: [
        { path: "/workspaces", label: "Workspaces", disabled: true },
        { path: "/workers", label: "Workers", disabled: true },
        { path: "/audit-log", label: "Audit Log", badge: "PRO", disabled: true },
        { path: "/git-sync", label: "Git Sync", badge: "PRO", disabled: true },
        { path: "/organization", label: "Organization", disabled: true },
        { path: "/api", label: "API", disabled: true },
        { path: "/support", label: "Support", disabled: true },
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

  function navIconName(path: string) {
    const names: Record<string, string> = {
      "/": "dashboard",
      "/pipelines": "pipelines",
      "/calendar": "calendar",
      "/lineage": "lineage",
      "/dependencies": "dependencies",
      "/variables": "variables",
      "/connections": "connections",
      "/plugins": "plugins",
      "/workspaces": "workspaces",
      "/workers": "workers",
      "/audit-log": "audit-log",
      "/git-sync": "git-sync",
      "/organization": "organization",
      "/api": "api",
      "/support": "support",
    };
    return names[path] || "dashboard";
  }
</script>

<aside class="sidebar" class:collapsed>
  <div class="brand-zone">
    <div class="logo">
      <div class="logo-mark" title="Brokoli · v0.1.0">
        <img src="/brand/icons/brokoli-symbol-micro.svg" alt="" width="18" height="24" />
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
              href={item.disabled ? undefined : `#${item.path}`}
              class="nav-item"
              class:disabled={item.disabled}
              class:active={isRouteActive(item.path)}
              title={item.label}
              aria-label={item.label}
              aria-disabled={item.disabled ? "true" : undefined}
              on:click={(event) => item.disabled && event.preventDefault()}
            >
              <BrandIcon name={navIconName(item.path)} size={18} className="nav-icon" />
              <span class="nav-label">{item.label}</span>
              {#if item.badge}<span class="item-badge">{item.badge}</span>{/if}
            </a>
          {/each}
        {/if}
      </div>
    {/each}
  </nav>

  <div class="sidebar-footer">
    <a href="#/" class="nav-item utility-link" title="Get started" aria-label="Get started">
      <BrandIcon name="get-started" size={18} className="nav-icon" />
      <span class="nav-label">Get started</span>
    </a>
    <a
      href="#/settings"
      class="nav-item utility-link"
      class:active={isRouteActive("/settings")}
      title="Settings"
      aria-label="Settings"
    >
      <BrandIcon name="settings" size={18} className="nav-icon" />
      <span class="nav-label">Settings</span>
    </a>

    {#if $authUser}
      <div class="user-info">
        <div class="user-avatar">
          <BrandIcon name="account" size={14} />
        </div>
        <div class="user-details">
          <span class="user-name">{userLabel($authUser)}</span>
          <span class="user-role">{$authUser.role}</span>
        </div>
        <button class="logout-btn" on:click={logout} title="Sign out" aria-label="Sign out">
          <BrandIcon name="sign-out" size={14} />
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
    background: var(--brand-tile);
    border: 1px solid var(--bk-brand-tile-border);
    border-radius: 8px;
    flex-shrink: 0;
  }
  .logo-mark img {
    display: block;
    width: 18px;
    height: 24px;
    object-fit: contain;
  }
  .logo-text {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  .logo-name {
    font-size: 15px;
    font-weight: 700;
    color: #ffffff;
    letter-spacing: -0.02em;
    line-height: 1;
  }
  :global(:root[data-theme="light"]) .logo-name {
    color: var(--text-primary);
  }
  .logo-sub {
    font-size: 9px;
    color: #566168;
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
    color: #566168;
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
    color: #768188;
    font-size: 13px;
    font-weight: 500;
    transition: all 150ms ease;
    text-decoration: none;
  }
  .nav-item:hover {
    color: #b6c0bc;
    background: var(--nav-hover-bg);
  }
  .nav-item:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: -2px;
  }
  .nav-item.active {
    position: relative;
    color: var(--nav-active-text);
    background: var(--nav-hover-bg);
    font-weight: 700;
  }
  .nav-item.active::before {
    content: "";
    position: absolute;
    left: 0;
    top: 6px;
    bottom: 6px;
    width: 2px;
    border-radius: 2px;
    background: var(--bk-brand-signal);
  }
  .nav-item.active :global(.nav-icon) {
    color: var(--nav-active-icon);
  }
  .nav-item.disabled {
    color: #566168;
    opacity: 0.58;
    cursor: default;
  }
  .nav-item.disabled:hover {
    color: #566168;
    background: transparent;
  }
  :global(.nav-icon) {
    flex-shrink: 0;
  }
  .nav-label {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .item-badge {
    margin-left: auto;
    padding: 2px 4px;
    border-radius: 4px;
    background: #22282c;
    color: #7f898e;
    font-size: 7px;
    letter-spacing: 0.08em;
    text-transform: uppercase;
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
    padding: 6px 7px;
    border-radius: 7px;
    background: var(--nav-hover-bg);
  }
  .user-avatar {
    width: 28px;
    height: 28px;
    display: flex;
    align-items: center;
    justify-content: center;
    background: #08382d;
    border-radius: 7px;
    color: #16ce7a;
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
