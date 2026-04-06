<script lang="ts">
  import { createEventDispatcher } from "svelte";

  export let visible = false;
  export let title = "Confirm";
  export let message = "Are you sure?";
  export let confirmLabel = "Confirm";
  export let cancelLabel = "Cancel";
  export let destructive = false;

  const dispatch = createEventDispatcher();

  function confirm() {
    dispatch("confirm");
    visible = false;
  }
  function cancel() {
    dispatch("cancel");
    visible = false;
  }
  function handleKeydown(e: KeyboardEvent) {
    if (e.key === "Escape") cancel();
    if (e.key === "Enter") confirm();
  }
</script>

{#if visible}
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="overlay" on:click={cancel} on:keydown={handleKeydown}>
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <div class="dialog" on:click|stopPropagation on:keydown={handleKeydown}>
      <h3>{title}</h3>
      <p>{message}</p>
      <div class="actions">
        <button class="btn-cancel" on:click={cancel}>{cancelLabel}</button>
        <button class="btn-confirm" class:destructive on:click={confirm}>{confirmLabel}</button>
      </div>
    </div>
  </div>
{/if}

<style>
  .overlay {
    position: fixed; inset: 0;
    background: var(--bg-overlay);
    z-index: 2000;
    display: flex; align-items: center; justify-content: center;
  }
  .dialog {
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    border-radius: 12px;
    padding: 24px;
    width: 400px;
    max-width: 90vw;
    animation: fade-in 150ms ease-out;
  }
  .dialog h3 { font-size: 16px; font-weight: 600; margin-bottom: 8px; }
  .dialog p { font-size: 13px; color: var(--text-secondary); line-height: 1.5; margin-bottom: 20px; }
  .actions { display: flex; justify-content: flex-end; gap: 8px; }
  .btn-cancel {
    padding: 7px 16px; border-radius: var(--radius-md);
    font-size: 13px; font-weight: 500;
    background: var(--bg-tertiary); color: var(--text-secondary);
    transition: all 150ms ease;
  }
  .btn-cancel:hover { background: var(--border); }
  .btn-confirm {
    padding: 7px 16px; border-radius: var(--radius-md);
    font-size: 13px; font-weight: 500;
    background: var(--accent); color: white;
    transition: all 150ms ease;
  }
  .btn-confirm:hover { opacity: 0.9; }
  .btn-confirm.destructive { background: var(--failed); }
  .btn-confirm.destructive:hover { opacity: 0.9; }
</style>
