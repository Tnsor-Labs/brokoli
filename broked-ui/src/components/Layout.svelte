<script lang="ts">
  import Sidebar from "./Sidebar.svelte";

  // Simple hash-based location tracking
  let currentPath = window.location.hash.replace("#", "") || "/";

  function handleHashChange() {
    currentPath = window.location.hash.replace("#", "") || "/";
  }
</script>

<svelte:window on:hashchange={handleHashChange} />

<div class="layout">
  <Sidebar {currentPath} />
  <main class="content">
    <slot />
  </main>
</div>

<style>
  .layout {
    display: flex;
    height: 100vh;
    overflow: hidden;
  }
  .content {
    flex: 1;
    overflow-y: auto;
    padding: var(--space-xl);
  }
</style>
