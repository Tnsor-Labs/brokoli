<script lang="ts">
  import { onMount } from "svelte";
  import { api } from "../lib/api";
  import type { DependencyRule, Pipeline } from "../lib/types";

  export let rules: DependencyRule[] = [];
  export let legacyDependsOn: string[] = [];
  export let currentPipelineId: string = "";
  export let onChange: (rules: DependencyRule[], legacy: string[]) => void = () => {};

  let availablePipelines: { id: string; name: string }[] = [];
  let loading = true;
  let showAdd = false;
  let newPipelineId = "";
  let newState: "succeeded" | "completed" | "failed" = "succeeded";
  let newMode: "gate" | "trigger" = "gate";
  let newWithinHours = 0;

  onMount(async () => {
    try {
      const res: any = await api.pipelines.list();
      const items: Pipeline[] = Array.isArray(res) ? res : (res.items || []);
      availablePipelines = items
        .filter(p => p.id !== currentPipelineId)
        .map(p => ({ id: p.id, name: p.name }));
    } catch {
      availablePipelines = [];
    }
    loading = false;
  });

  // Merged view: legacy strings rendered as implicit gate/succeeded rules,
  // but editable as first-class rules. When the user edits a legacy dep, we
  // promote it into DependencyRules and drop it from legacyDependsOn.
  $: merged = [
    ...rules.map((r, i) => ({ rule: r, origin: "rule" as const, index: i })),
    ...legacyDependsOn
      .filter(id => !rules.some(r => r.pipeline_id === id))
      .map((id, i) => ({
        rule: { pipeline_id: id, state: "succeeded" as const, mode: "gate" as const },
        origin: "legacy" as const,
        index: i,
      })),
  ];

  function nameFor(id: string): string {
    return availablePipelines.find(p => p.id === id)?.name || id.slice(0, 8);
  }

  function addRule() {
    if (!newPipelineId) return;
    const rule: DependencyRule = {
      pipeline_id: newPipelineId,
      state: newState,
      mode: newMode,
    };
    if (newWithinHours > 0) {
      rule.within_seconds = newWithinHours * 3600;
    }
    rules = [...rules, rule];
    onChange(rules, legacyDependsOn);
    newPipelineId = "";
    newWithinHours = 0;
    showAdd = false;
  }

  function removeRule(origin: "rule" | "legacy", index: number, pipelineId: string) {
    if (origin === "rule") {
      rules = rules.filter((_, i) => i !== index);
    } else {
      legacyDependsOn = legacyDependsOn.filter(id => id !== pipelineId);
    }
    onChange(rules, legacyDependsOn);
  }

  function updateRule(index: number, patch: Partial<DependencyRule>) {
    rules = rules.map((r, i) => (i === index ? { ...r, ...patch } : r));
    onChange(rules, legacyDependsOn);
  }

  function promoteLegacy(pipelineId: string) {
    legacyDependsOn = legacyDependsOn.filter(id => id !== pipelineId);
    rules = [
      ...rules,
      { pipeline_id: pipelineId, state: "succeeded", mode: "gate" },
    ];
    onChange(rules, legacyDependsOn);
  }

  function hoursFor(rule: DependencyRule): number {
    return rule.within_seconds ? Math.round(rule.within_seconds / 3600) : 0;
  }
</script>

<div class="dep-picker">
  {#if loading}
    <div class="empty">Loading pipelines…</div>
  {:else if merged.length === 0 && !showAdd}
    <div class="empty">
      <p>No upstream dependencies. This pipeline runs independently.</p>
      <button class="add-btn" on:click={() => (showAdd = true)} disabled={availablePipelines.length === 0}>
        + Add dependency
      </button>
    </div>
  {:else}
    <div class="rules">
      {#each merged as entry (entry.origin + entry.index + entry.rule.pipeline_id)}
        <div class="rule" class:legacy={entry.origin === "legacy"}>
          <div class="rule-header">
            <span class="pipe-name">{nameFor(entry.rule.pipeline_id)}</span>
            {#if entry.origin === "legacy"}
              <span class="legacy-tag" title="Imported from legacy depends_on">legacy</span>
              <button class="promote" on:click={() => promoteLegacy(entry.rule.pipeline_id)}>
                Edit
              </button>
            {/if}
            <button
              class="remove"
              on:click={() => removeRule(entry.origin, entry.index, entry.rule.pipeline_id)}
              aria-label="Remove"
            >×</button>
          </div>
          {#if entry.origin === "rule"}
            <div class="rule-fields">
              <label class="field">
                <span class="field-label">State</span>
                <select
                  value={entry.rule.state || "succeeded"}
                  on:change={(e) => updateRule(entry.index, { state: e.currentTarget.value as any })}
                >
                  <option value="succeeded">Succeeded</option>
                  <option value="completed">Completed (any)</option>
                  <option value="failed">Failed</option>
                </select>
              </label>
              <label class="field">
                <span class="field-label">Mode</span>
                <select
                  value={entry.rule.mode || "gate"}
                  on:change={(e) => updateRule(entry.index, { mode: e.currentTarget.value as any })}
                >
                  <option value="gate">Gate (block)</option>
                  <option value="trigger">Trigger (auto-fire)</option>
                </select>
              </label>
              <label class="field">
                <span class="field-label">Within (hours)</span>
                <input
                  type="number"
                  min="0"
                  step="1"
                  value={hoursFor(entry.rule)}
                  on:input={(e) => {
                    const h = Math.max(0, parseInt(e.currentTarget.value || "0", 10));
                    updateRule(entry.index, { within_seconds: h > 0 ? h * 3600 : undefined });
                  }}
                  placeholder="any"
                />
              </label>
            </div>
          {:else}
            <div class="rule-hint">
              Gate · Succeeded · any freshness (click Edit to customize)
            </div>
          {/if}
        </div>
      {/each}
    </div>

    {#if !showAdd && availablePipelines.length > 0}
      <button class="add-btn ghost" on:click={() => (showAdd = true)}>+ Add dependency</button>
    {/if}
  {/if}

  {#if showAdd}
    <div class="add-form">
      <label class="field">
        <span class="field-label">Upstream pipeline</span>
        <select bind:value={newPipelineId}>
          <option value="">Choose…</option>
          {#each availablePipelines as p}
            <option value={p.id}>{p.name}</option>
          {/each}
        </select>
      </label>
      <label class="field">
        <span class="field-label">State</span>
        <select bind:value={newState}>
          <option value="succeeded">Succeeded</option>
          <option value="completed">Completed</option>
          <option value="failed">Failed</option>
        </select>
      </label>
      <label class="field">
        <span class="field-label">Mode</span>
        <select bind:value={newMode}>
          <option value="gate">Gate</option>
          <option value="trigger">Trigger</option>
        </select>
      </label>
      <label class="field">
        <span class="field-label">Within (hrs)</span>
        <input type="number" min="0" bind:value={newWithinHours} placeholder="any" />
      </label>
      <div class="add-actions">
        <button class="btn-cancel" on:click={() => (showAdd = false)}>Cancel</button>
        <button class="btn-add" on:click={addRule} disabled={!newPipelineId}>Add</button>
      </div>
    </div>
  {/if}
</div>

<style>
  .dep-picker { display: flex; flex-direction: column; gap: 8px; }
  .empty {
    padding: 14px;
    border: 1px dashed var(--border);
    border-radius: var(--radius-md);
    color: var(--text-muted);
    font-size: 13px;
    text-align: center;
  }
  .empty p { margin: 0 0 10px; }
  .rules { display: flex; flex-direction: column; gap: 8px; }
  .rule {
    background: var(--bg-tertiary);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    padding: 10px 12px;
  }
  .rule.legacy { border-style: dashed; opacity: 0.9; }
  .rule-header {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-bottom: 8px;
  }
  .pipe-name { font-weight: 600; font-size: 13px; flex: 1; }
  .legacy-tag {
    font-size: 10px; font-family: var(--font-mono);
    padding: 1px 6px; border-radius: 3px;
    background: var(--pending-bg); color: var(--pending);
    text-transform: uppercase;
  }
  .promote {
    font-size: 11px; padding: 3px 8px;
    background: transparent; color: var(--accent);
    border: 1px solid var(--accent);
    border-radius: var(--radius-sm);
  }
  .remove {
    width: 22px; height: 22px;
    border: none; background: transparent;
    color: var(--text-muted); font-size: 18px;
    cursor: pointer; border-radius: var(--radius-sm);
  }
  .remove:hover { background: var(--failed-bg); color: var(--failed); }
  .rule-fields {
    display: grid;
    grid-template-columns: 1fr 1fr 110px;
    gap: 8px;
  }
  .rule-hint { font-size: 11px; color: var(--text-muted); font-style: italic; }
  .field { display: flex; flex-direction: column; gap: 3px; }
  .field-label { font-size: 10px; color: var(--text-muted); text-transform: uppercase; letter-spacing: 0.05em; }
  .field select, .field input {
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    color: var(--text-primary);
    border-radius: var(--radius-sm);
    padding: 5px 7px;
    font-size: 12px;
  }
  .add-btn {
    padding: 8px 12px;
    border: 1px solid var(--accent);
    background: var(--accent);
    color: white;
    border-radius: var(--radius-md);
    font-size: 12px; font-weight: 500;
    cursor: pointer;
  }
  .add-btn:disabled { opacity: 0.5; cursor: not-allowed; }
  .add-btn.ghost {
    background: transparent; color: var(--accent);
    border: 1px dashed var(--accent);
  }
  .add-form {
    display: grid;
    grid-template-columns: 2fr 1fr 1fr 1fr;
    gap: 8px;
    padding: 10px;
    border: 1px solid var(--accent);
    border-radius: var(--radius-md);
    background: var(--bg-secondary);
  }
  .add-actions {
    grid-column: 1 / -1;
    display: flex; gap: 6px; justify-content: flex-end;
  }
  .btn-cancel, .btn-add {
    padding: 6px 14px;
    border-radius: var(--radius-sm);
    font-size: 12px; font-weight: 500;
    border: none; cursor: pointer;
  }
  .btn-cancel { background: var(--bg-tertiary); color: var(--text-secondary); }
  .btn-add { background: var(--accent); color: white; }
  .btn-add:disabled { opacity: 0.5; cursor: not-allowed; }
</style>
