<script lang="ts">
  import { toasts, dismiss } from "../lib/toast";
</script>

<div class="toast-container">
  {#each $toasts as t (t.id)}
    <div class="toast toast-{t.type}" on:click={() => dismiss(t.id)} on:keydown={() => {}} role="alert">
      <span class="toast-icon">
        {#if t.type === "success"}
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none"><path d="M9 12l2 2 4-4" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" /><circle cx="12" cy="12" r="10" stroke="currentColor" stroke-width="1.5" /></svg>
        {:else if t.type === "error"}
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none"><circle cx="12" cy="12" r="10" stroke="currentColor" stroke-width="1.5" /><path d="M15 9l-6 6M9 9l6 6" stroke="currentColor" stroke-width="2" stroke-linecap="round" /></svg>
        {:else if t.type === "warning"}
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none"><path d="M12 9v4m0 4h.01M10.29 3.86L1.82 18a2 2 0 001.71 3h16.94a2 2 0 001.71-3L13.71 3.86a2 2 0 00-3.42 0z" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" /></svg>
        {:else}
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none"><circle cx="12" cy="12" r="10" stroke="currentColor" stroke-width="1.5" /><path d="M12 16v-4m0-4h.01" stroke="currentColor" stroke-width="2" stroke-linecap="round" /></svg>
        {/if}
      </span>
      <span class="toast-msg">{t.message}</span>
    </div>
  {/each}
</div>

<style>
  .toast-container {
    position: fixed;
    bottom: 20px;
    right: 20px;
    z-index: 9999;
    display: flex;
    flex-direction: column-reverse;
    gap: 6px;
    pointer-events: none;
  }

  .toast {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 10px 16px;
    border-radius: 8px;
    font-size: 12.5px;
    font-weight: 500;
    max-width: 380px;
    pointer-events: auto;
    cursor: pointer;
    animation: slide-in 200ms ease-out;
    border: 1px solid;
  }

  .toast-success {
    background: var(--success-bg);
    border-color: rgba(34, 197, 94, 0.2);
    color: #22c55e;
  }
  .toast-error {
    background: var(--failed-bg);
    border-color: rgba(239, 68, 68, 0.2);
    color: #ef4444;
  }
  .toast-warning {
    background: var(--warning-bg);
    border-color: rgba(245, 158, 11, 0.2);
    color: #f59e0b;
  }
  .toast-info {
    background: var(--accent-glow);
    border-color: rgba(99, 102, 241, 0.2);
    color: var(--accent-text);
  }

  .toast-icon { flex-shrink: 0; display: flex; }
  .toast-msg { line-height: 1.4; }

  @keyframes slide-in {
    from { opacity: 0; transform: translateY(8px); }
    to { opacity: 1; transform: translateY(0); }
  }
</style>
