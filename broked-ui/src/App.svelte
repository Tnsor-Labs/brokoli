<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import Router from "svelte-spa-router";
  import Layout from "./components/Layout.svelte";
  import ToastContainer from "./components/ToastContainer.svelte";
  import Dashboard from "./pages/Dashboard.svelte";
  import Pipelines from "./pages/Pipelines.svelte";
  import PipelineEditor from "./pages/PipelineEditor.svelte";
  import PipelineRuns from "./pages/PipelineRuns.svelte";
  import Settings from "./pages/Settings.svelte";
  import Login from "./pages/Login.svelte";
  import Lineage from "./pages/Lineage.svelte";
  import Connections from "./pages/Connections.svelte";
  import Variables from "./pages/Variables.svelte";
  import Calendar from "./pages/Calendar.svelte";
  import { createWebSocket } from "./lib/ws";
  import { addEvent } from "./lib/stores";
  import { notify } from "./lib/toast";
  import { initTheme } from "./lib/theme";
  import { initAuth, authReady, authUser, needsSetup, loadPermissions } from "./lib/auth";
  import GlobalSearch from "./components/GlobalSearch.svelte";

  initTheme();

  const routes = {
    "/": Dashboard,
    "/pipelines": Pipelines,
    "/calendar": Calendar,
    "/pipelines/:id/edit": PipelineEditor,
    "/pipelines/:id/runs": PipelineRuns,
    "/pipelines/:id": PipelineRuns,
    "/lineage": Lineage,
    "/variables": Variables,
    "/connections": Connections,
    "/settings": Settings,
    "/login": Login,
  };

  let ws: { close: () => void } | null = null;

  onMount(async () => {
    await initAuth();
    await loadPermissions();
    ws = createWebSocket((event) => {
      addEvent(event);
      // Global notifications for run events
      if (event.type === "run.failed") {
        notify.error(`Pipeline run failed${event.error ? ": " + event.error : ""}`);
      }
      if (event.type === "run.completed") {
        notify.success("Pipeline run completed");
      }
    });
  });

  onDestroy(() => {
    ws?.close();
  });

  let showShortcuts = false;

  function handleGlobalKey(e: KeyboardEvent) {
    const tag = (e.target as HTMLElement)?.tagName;
    if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT") return;
    if (e.key === "?" || (e.key === "/" && e.shiftKey)) {
      e.preventDefault();
      showShortcuts = !showShortcuts;
    }
    if (e.key === "Escape" && showShortcuts) {
      showShortcuts = false;
    }
  }

  // Track current hash reactively
  let currentHash = window.location.hash;
  function onHashChange() { currentHash = window.location.hash; }

  $: isLoginRoute = currentHash === "#/login" || currentHash === "";
  $: requiresAuth = $authReady && !$authUser && !$needsSetup;
</script>

<svelte:window on:keydown={handleGlobalKey} on:hashchange={onHashChange} />

{#if showShortcuts}
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="shortcut-overlay" on:click={() => showShortcuts = false} on:keydown={() => {}}>
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <div class="shortcut-modal" on:click|stopPropagation on:keydown={() => {}}>
      <h2>Keyboard Shortcuts</h2>
      <div class="shortcut-grid">
        <div class="shortcut-section">
          <h3>Global</h3>
          <div class="shortcut-row"><kbd>?</kbd><span>Show this help</span></div>
          <div class="shortcut-row"><kbd>Esc</kbd><span>Close modal / deselect</span></div>
        </div>
        <div class="shortcut-section">
          <h3>Pipeline Editor</h3>
          <div class="shortcut-row"><kbd>Ctrl+S</kbd><span>Save pipeline</span></div>
          <div class="shortcut-row"><kbd>Ctrl+Z</kbd><span>Undo</span></div>
          <div class="shortcut-row"><kbd>Ctrl+Shift+Z</kbd><span>Redo</span></div>
          <div class="shortcut-row"><kbd>Delete</kbd><span>Delete selected node</span></div>
          <div class="shortcut-row"><kbd>D</kbd><span>Duplicate selected node</span></div>
          <div class="shortcut-row"><kbd>Drag bg</kbd><span>Pan canvas</span></div>
          <div class="shortcut-row"><kbd>Scroll</kbd><span>Zoom in/out</span></div>
        </div>
        <div class="shortcut-section">
          <h3>Code Editor</h3>
          <div class="shortcut-row"><kbd>Ctrl+S</kbd><span>Save script</span></div>
          <div class="shortcut-row"><kbd>Tab</kbd><span>Indent (4 spaces)</span></div>
          <div class="shortcut-row"><kbd>Esc</kbd><span>Close editor</span></div>
        </div>
      </div>
    </div>
  </div>
{/if}

{#if !$authReady}
  <div class="loading-screen">
    <div class="loading-spinner"></div>
  </div>
{:else if requiresAuth && !isLoginRoute}
  <Login />
{:else if $needsSetup && !$authUser}
  <Login />
{:else if isLoginRoute}
  <Router {routes} />
{:else}
  <Layout>
    <Router {routes} />
  </Layout>
{/if}
<ToastContainer />
<GlobalSearch />

<style>
  .loading-screen {
    height: 100vh;
    display: flex;
    align-items: center;
    justify-content: center;
    background: var(--bg-primary);
  }
  .loading-spinner {
    width: 32px;
    height: 32px;
    border: 3px solid var(--border);
    border-top-color: var(--accent);
    border-radius: 50%;
    animation: spin 0.8s linear infinite;
  }
  @keyframes spin {
    to { transform: rotate(360deg); }
  }

  .shortcut-overlay {
    position: fixed; inset: 0; background: rgba(0,0,0,0.6);
    display: flex; align-items: center; justify-content: center;
    z-index: 9999;
  }
  .shortcut-modal {
    background: var(--bg-secondary); border: 1px solid var(--border);
    border-radius: 12px; padding: 24px 32px;
    max-width: 560px; width: 90vw;
  }
  .shortcut-modal h2 {
    font-size: 16px; font-weight: 600; margin-bottom: 16px;
    color: var(--text-primary);
  }
  .shortcut-grid {
    display: grid; grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
    gap: 20px;
  }
  .shortcut-section h3 {
    font-size: 10px; text-transform: uppercase; letter-spacing: 0.08em;
    color: var(--text-muted); font-weight: 600; margin-bottom: 8px;
  }
  .shortcut-row {
    display: flex; align-items: center; justify-content: space-between;
    padding: 4px 0; font-size: 12px; color: var(--text-secondary);
  }
  .shortcut-row kbd {
    font-family: var(--font-mono); font-size: 10px; font-weight: 600;
    background: var(--bg-tertiary); border: 1px solid var(--border);
    padding: 2px 6px; border-radius: 4px; color: var(--text-primary);
    min-width: 24px; text-align: center;
  }
</style>
