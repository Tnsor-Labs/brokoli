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
  import { initTheme } from "./lib/theme";
  import { initAuth, authReady, authUser, needsSetup } from "./lib/auth";

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
    ws = createWebSocket((event) => {
      addEvent(event);
    });
  });

  onDestroy(() => {
    ws?.close();
  });

  // Determine if we need the login page
  $: isLoginRoute = window.location.hash === "#/login" || window.location.hash === "";
  $: requiresAuth = $authReady && !$authUser && !$needsSetup;
</script>

{#if !$authReady}
  <div class="loading-screen">
    <div class="loading-spinner"></div>
  </div>
{:else if requiresAuth && !isLoginRoute}
  <Login />
{:else if $needsSetup && !$authUser}
  <Login />
{:else if window.location.hash === "#/login"}
  <Router {routes} />
{:else}
  <Layout>
    <Router {routes} />
  </Layout>
{/if}
<ToastContainer />

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
</style>
