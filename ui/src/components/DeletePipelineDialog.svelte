<script lang="ts">
  import { createEventDispatcher } from "svelte";

  export let visible = false;
  export let pipelineName = "";
  export let dependents: { id: string; name: string }[] = [];

  const dispatch = createEventDispatcher<{
    resolve: { mode: "cascade" | "decouple" | "abort" };
    cancel: void;
  }>();

  function cancel() {
    visible = false;
    dispatch("cancel");
  }
  function choose(mode: "cascade" | "decouple") {
    visible = false;
    dispatch("resolve", { mode });
  }
  function handleKey(e: KeyboardEvent) {
    if (e.key === "Escape") cancel();
  }
</script>

{#if visible}
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="overlay" on:click={cancel} on:keydown={handleKey}>
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <div class="dialog" on:click|stopPropagation on:keydown={handleKey}>
      <h3>Cannot delete <strong>{pipelineName}</strong></h3>
      <p class="subtitle">
        {dependents.length} other {dependents.length === 1 ? "pipeline depends" : "pipelines depend"} on this one.
        Choose how to resolve:
      </p>

      <ul class="dep-list">
        {#each dependents as d}
          <li>
            <span class="chevron">↳</span>
            <span class="dep-name">{d.name}</span>
          </li>
        {/each}
      </ul>

      <div class="options">
        <button class="option option-decouple" on:click={() => choose("decouple")}>
          <div class="option-title">Decouple</div>
          <div class="option-desc">
            Remove the dependency from each downstream pipeline, then delete this one.
            The {dependents.length === 1 ? "pipeline stays" : "pipelines stay"}.
          </div>
        </button>

        <button class="option option-cascade" on:click={() => choose("cascade")}>
          <div class="option-title">Cascade delete</div>
          <div class="option-desc">
            Delete this pipeline <em>and all {dependents.length} dependent {dependents.length === 1 ? "pipeline" : "pipelines"}</em>.
            This is irreversible.
          </div>
        </button>
      </div>

      <div class="actions">
        <button class="btn-cancel" on:click={cancel}>Cancel</button>
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
    width: 520px;
    max-width: 92vw;
    max-height: 90vh;
    overflow-y: auto;
    animation: fade-in 150ms ease-out;
  }
  h3 {
    font-size: 16px;
    font-weight: 600;
    margin: 0 0 6px;
    color: var(--text-primary);
  }
  h3 strong { color: var(--failed); font-weight: 700; }
  .subtitle {
    font-size: 13px;
    color: var(--text-secondary);
    line-height: 1.5;
    margin: 0 0 14px;
  }
  .dep-list {
    list-style: none;
    padding: 10px 14px;
    margin: 0 0 18px;
    background: var(--bg-tertiary);
    border-radius: var(--radius-md);
    border: 1px solid var(--border);
    max-height: 140px;
    overflow-y: auto;
  }
  .dep-list li {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 4px 0;
    font-size: 13px;
  }
  .chevron {
    color: var(--text-muted);
    font-family: var(--font-mono);
  }
  .dep-name {
    font-weight: 500;
    color: var(--text-primary);
  }
  .options {
    display: flex;
    flex-direction: column;
    gap: 10px;
    margin-bottom: 16px;
  }
  .option {
    text-align: left;
    padding: 14px 16px;
    background: var(--bg-tertiary);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    cursor: pointer;
    transition: all 150ms ease;
  }
  .option:hover { border-color: var(--accent); transform: translateY(-1px); }
  .option-title {
    font-size: 13px;
    font-weight: 600;
    margin-bottom: 4px;
    color: var(--text-primary);
  }
  .option-decouple .option-title { color: var(--accent); }
  .option-cascade .option-title { color: var(--failed); }
  .option-desc {
    font-size: 12px;
    line-height: 1.45;
    color: var(--text-secondary);
  }
  .actions {
    display: flex;
    justify-content: flex-end;
  }
  .btn-cancel {
    padding: 7px 16px;
    border-radius: var(--radius-md);
    font-size: 13px; font-weight: 500;
    background: var(--bg-tertiary); color: var(--text-secondary);
    border: none;
    cursor: pointer;
  }
  .btn-cancel:hover { background: var(--border); }

  @keyframes fade-in {
    from { opacity: 0; transform: translateY(-4px); }
    to { opacity: 1; transform: translateY(0); }
  }
</style>
