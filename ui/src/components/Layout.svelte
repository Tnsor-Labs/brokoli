<script lang="ts">
  import Sidebar from "./Sidebar.svelte";
  import RunIndicator from "./RunIndicator.svelte";
  import { icons } from "../lib/icons";

  let currentPath = window.location.hash.replace("#", "") || "/";

  function handleHashChange() {
    currentPath = window.location.hash.replace("#", "") || "/";
  }

  const mobileNav = [
    { path: "/", label: "Home", icon: icons.dashboard.d },
    { path: "/pipelines", label: "Pipelines", icon: icons.pipeline.d },
    { path: "/connections", label: "Connect", icon: icons.connection.d },
    { path: "/variables", label: "Variables", icon: icons.variable.d },
    { path: "/settings", label: "Settings", icon: icons.settings.d },
  ];
</script>

<svelte:window on:hashchange={handleHashChange} />

<div class="layout">
  <Sidebar {currentPath} />
  <div class="main-area">
    <main class="content">
      <slot />
    </main>
  </div>
</div>
<RunIndicator />

<!-- Mobile bottom nav -->
<nav class="mobile-nav">
  {#each mobileNav as item}
    <a href="#{item.path}" class="mobile-nav-item" class:active={currentPath === item.path}>
      <svg width="20" height="20" viewBox="0 0 24 24" fill="none">
        <path d={item.icon} stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
      </svg>
      <span>{item.label}</span>
    </a>
  {/each}
</nav>

<style>
  .layout {
    display: flex;
    height: 100vh;
    overflow: hidden;
  }
  .main-area {
    flex: 1;
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }
  .content {
    flex: 1;
    overflow-y: auto;
    padding: var(--space-xl);
  }

  /* ── Mobile bottom nav — hidden on desktop ── */
  .mobile-nav {
    display: none;
  }

  @media (max-width: 768px) {
    .content {
      padding: var(--space-md);
      padding-bottom: 70px;
    }
    .mobile-nav {
      display: flex;
      position: fixed;
      bottom: 0;
      left: 0;
      right: 0;
      height: 56px;
      background: var(--bg-sidebar);
      border-top: 1px solid var(--border-sidebar);
      z-index: 100;
      justify-content: space-around;
      align-items: center;
      padding: 0 4px;
    }
    .mobile-nav-item {
      display: flex;
      flex-direction: column;
      align-items: center;
      gap: 2px;
      padding: 6px 8px;
      border-radius: 8px;
      color: var(--text-muted);
      text-decoration: none;
      font-size: 9px;
      font-weight: 500;
      transition: color 150ms ease;
      min-width: 48px;
    }
    .mobile-nav-item.active {
      color: var(--accent);
    }
    .mobile-nav-item:hover {
      color: var(--text-primary);
    }
  }

  /* Tablet: narrower content area */
  @media (max-width: 1024px) and (min-width: 769px) {
    .content {
      padding: var(--space-lg);
    }
  }
</style>
