<script lang="ts">
  export let width = "100%";
  export let height = "16px";
  export let rounded = false;
  export let variant: "line" | "circle" | "card" = "line";
</script>

{#if variant === "card"}
  <div class="skeleton-card" style="width: {width}; height: {height}">
    <div class="sk-line" style="width: 40%; height: 12px; margin-bottom: 10px"></div>
    <div class="sk-line" style="width: 70%; height: 24px; margin-bottom: 8px"></div>
    <div class="sk-line" style="width: 50%; height: 10px"></div>
  </div>
{:else}
  <div
    class="sk-line"
    class:rounded
    class:circle={variant === "circle"}
    style="width: {width}; height: {height}"
  ></div>
{/if}

<style>
  .sk-line {
    background: linear-gradient(90deg, var(--bg-tertiary) 25%, rgba(255,255,255,0.04) 50%, var(--bg-tertiary) 75%);
    background-size: 200% 100%;
    border-radius: var(--radius-md);
    animation: shimmer 1.8s ease-in-out infinite;
  }
  .rounded { border-radius: 999px; }
  .circle { border-radius: 50%; }

  .skeleton-card {
    background: var(--bg-secondary);
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius-xl, 14px);
    padding: 20px;
  }

  @keyframes shimmer {
    0% { background-position: 200% 0; }
    100% { background-position: -200% 0; }
  }
</style>
