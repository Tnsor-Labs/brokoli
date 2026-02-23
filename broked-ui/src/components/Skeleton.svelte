<script lang="ts">
  export let width = "100%";
  export let height = "16px";
  export let rounded = false;
</script>

<div
  class="skeleton"
  class:rounded
  style="width: {width}; height: {height}"
></div>

<style>
  .skeleton {
    background: var(--bg-tertiary);
    border-radius: var(--radius-md);
    animation: shimmer 1.5s ease-in-out infinite;
  }
  .rounded { border-radius: 50%; }

  @keyframes shimmer {
    0% { opacity: 0.5; }
    50% { opacity: 0.8; }
    100% { opacity: 0.5; }
  }
</style>
