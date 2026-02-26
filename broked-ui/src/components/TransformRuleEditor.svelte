<script lang="ts">
  import { icons } from "../lib/icons";
  import { createEventDispatcher } from "svelte";

  export let rules: any[] = [];

  const dispatch = createEventDispatcher();

  const ruleTypes = [
    { value: "rename_columns", label: "Rename Columns" },
    { value: "filter_rows", label: "Filter Rows" },
    { value: "drop_columns", label: "Drop Columns" },
    { value: "apply_function", label: "Apply Function" },
    { value: "add_column", label: "Add Column" },
    { value: "replace_values", label: "Replace Values" },
    { value: "sort", label: "Sort" },
    { value: "deduplicate", label: "Deduplicate" },
    { value: "aggregate", label: "Aggregate" },
  ];

  function addRule() {
    rules = [...rules, { type: "rename_columns", mapping: {} }];
    emit();
  }

  function removeRule(index: number) {
    rules = rules.filter((_, i) => i !== index);
    emit();
  }

  function moveUp(index: number) {
    if (index === 0) return;
    const copy = [...rules];
    [copy[index - 1], copy[index]] = [copy[index], copy[index - 1]];
    rules = copy;
    emit();
  }

  function moveDown(index: number) {
    if (index >= rules.length - 1) return;
    const copy = [...rules];
    [copy[index], copy[index + 1]] = [copy[index + 1], copy[index]];
    rules = copy;
    emit();
  }

  function updateRule(index: number, key: string, value: any) {
    rules[index] = { ...rules[index], [key]: value };
    rules = rules;
    emit();
  }

  function updateRuleType(index: number, type: string) {
    // Reset rule with defaults for the new type
    const defaults: Record<string, any> = {
      rename_columns: { type, mapping: {} },
      filter_rows: { type, condition: "" },
      drop_columns: { type, columns: [] },
      apply_function: { type, column: "", function: "" },
      add_column: { type, name: "", expression: "" },
      replace_values: { type, column: "", mapping: {} },
      sort: { type, columns: [], ascending: true },
      deduplicate: { type, columns: [] },
      aggregate: { type, group_by: [], agg_fields: [] },
    };
    rules[index] = defaults[type] || { type };
    rules = rules;
    emit();
  }

  function emit() {
    dispatch("change", rules);
  }

  // Helper for mapping fields (key=value pairs)
  function getMappingEntries(mapping: Record<string, string>): [string, string][] {
    return Object.entries(mapping || {});
  }

  function updateMapping(ruleIndex: number, oldKey: string, newKey: string, newValue: string) {
    const m = { ...rules[ruleIndex].mapping };
    if (oldKey !== newKey) delete m[oldKey];
    m[newKey] = newValue;
    updateRule(ruleIndex, "mapping", m);
  }

  function addMappingEntry(ruleIndex: number) {
    const m = { ...rules[ruleIndex].mapping, "": "" };
    updateRule(ruleIndex, "mapping", m);
  }

  function removeMappingEntry(ruleIndex: number, key: string) {
    const m = { ...rules[ruleIndex].mapping };
    delete m[key];
    updateRule(ruleIndex, "mapping", m);
  }

  // Helper for columns list
  function updateColumnsList(ruleIndex: number, field: string, value: string) {
    const cols = value.split(",").map(s => s.trim()).filter(Boolean);
    updateRule(ruleIndex, field, cols);
  }

  // Helper for agg_fields
  function addAggField(ruleIndex: number) {
    const fields = [...(rules[ruleIndex].agg_fields || []), { column: "", function: "count", alias: "" }];
    updateRule(ruleIndex, "agg_fields", fields);
  }

  function updateAggField(ruleIndex: number, fieldIndex: number, key: string, value: string) {
    const fields = [...(rules[ruleIndex].agg_fields || [])];
    fields[fieldIndex] = { ...fields[fieldIndex], [key]: value };
    updateRule(ruleIndex, "agg_fields", fields);
  }

  function removeAggField(ruleIndex: number, fieldIndex: number) {
    const fields = (rules[ruleIndex].agg_fields || []).filter((_: any, i: number) => i !== fieldIndex);
    updateRule(ruleIndex, "agg_fields", fields);
  }
</script>

<div class="rule-editor">
  {#each rules as rule, i}
    <div class="rule-card">
      <div class="rule-header">
        <select class="rule-type-select" value={rule.type} on:change={(e) => updateRuleType(i, e.currentTarget.value)}>
          {#each ruleTypes as rt}
            <option value={rt.value}>{rt.label}</option>
          {/each}
        </select>
        <div class="rule-actions">
          <button class="rule-btn" on:click={() => moveUp(i)} disabled={i === 0} title="Move up">^</button>
          <button class="rule-btn" on:click={() => moveDown(i)} disabled={i === rules.length - 1} title="Move down">v</button>
          <button class="rule-btn danger" on:click={() => removeRule(i)} title="Remove">
            <svg width="12" height="12" viewBox="0 0 24 24" fill="none">
              <path d={icons.trash.d} stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" />
            </svg>
          </button>
        </div>
      </div>

      <div class="rule-body">
        {#if rule.type === "rename_columns" || rule.type === "replace_values"}
          {#if rule.type === "replace_values"}
            <div class="mini-field">
              <label>Column</label>
              <input value={rule.column || ""} on:input={(e) => updateRule(i, "column", e.currentTarget.value)} placeholder="column_name" />
            </div>
          {/if}
          <div class="mapping-list">
            {#each getMappingEntries(rule.mapping) as [key, val], mi}
              <div class="mapping-row">
                <input value={key} on:change={(e) => updateMapping(i, key, e.currentTarget.value, val)} placeholder={rule.type === "rename_columns" ? "old_name" : "old_value"} />
                <span class="arrow">-></span>
                <input value={val} on:change={(e) => updateMapping(i, key, key, e.currentTarget.value)} placeholder={rule.type === "rename_columns" ? "new_name" : "new_value"} />
                <button class="rule-btn mini danger" on:click={() => removeMappingEntry(i, key)}>x</button>
              </div>
            {/each}
            <button class="btn-add-row" on:click={() => addMappingEntry(i)}>+ Add mapping</button>
          </div>

        {:else if rule.type === "filter_rows"}
          <div class="mini-field">
            <label>Condition</label>
            <input value={rule.condition || ""} on:input={(e) => updateRule(i, "condition", e.currentTarget.value)} placeholder="status in [active] or col = value" />
          </div>

        {:else if rule.type === "drop_columns" || rule.type === "deduplicate" || rule.type === "sort"}
          <div class="mini-field">
            <label>Columns (comma-separated)</label>
            <input value={(rule.columns || []).join(", ")} on:input={(e) => updateColumnsList(i, "columns", e.currentTarget.value)} placeholder="col1, col2" />
          </div>
          {#if rule.type === "sort"}
            <label class="toggle-inline">
              <input type="checkbox" checked={rule.ascending !== false} on:change={(e) => updateRule(i, "ascending", e.currentTarget.checked)} />
              Ascending
            </label>
          {/if}

        {:else if rule.type === "apply_function"}
          <div class="mini-field">
            <label>Column</label>
            <input value={rule.column || ""} on:input={(e) => updateRule(i, "column", e.currentTarget.value)} placeholder="column_name" />
          </div>
          <div class="mini-field">
            <label>Function</label>
            <select value={rule.function || ""} on:change={(e) => updateRule(i, "function", e.currentTarget.value)}>
              <option value="">Select...</option>
              <option value="lower">lower</option>
              <option value="upper">upper</option>
              <option value="trim">trim</option>
              <option value="title">title</option>
            </select>
          </div>

        {:else if rule.type === "add_column"}
          <div class="mini-field">
            <label>Name</label>
            <input value={rule.name || ""} on:input={(e) => updateRule(i, "name", e.currentTarget.value)} placeholder="new_column" />
          </div>
          <div class="mini-field">
            <label>Expression</label>
            <input value={rule.expression || ""} on:input={(e) => updateRule(i, "expression", e.currentTarget.value)} placeholder="col1 + ' ' + col2" />
          </div>

        {:else if rule.type === "aggregate"}
          <div class="mini-field">
            <label>Group By (comma-separated)</label>
            <input value={(rule.group_by || []).join(", ")} on:input={(e) => updateColumnsList(i, "group_by", e.currentTarget.value)} placeholder="category, region" />
          </div>
          <div class="agg-fields">
            <span class="agg-title">Aggregations</span>
            {#each rule.agg_fields || [] as af, fi}
              <div class="agg-row">
                <select value={af.function} on:change={(e) => updateAggField(i, fi, "function", e.currentTarget.value)}>
                  <option value="count">count</option>
                  <option value="sum">sum</option>
                  <option value="avg">avg</option>
                  <option value="min">min</option>
                  <option value="max">max</option>
                </select>
                <input value={af.column} on:input={(e) => updateAggField(i, fi, "column", e.currentTarget.value)} placeholder="column" />
                <input value={af.alias} on:input={(e) => updateAggField(i, fi, "alias", e.currentTarget.value)} placeholder="alias" />
                <button class="rule-btn mini danger" on:click={() => removeAggField(i, fi)}>x</button>
              </div>
            {/each}
            <button class="btn-add-row" on:click={() => addAggField(i)}>+ Add aggregation</button>
          </div>
        {/if}
      </div>
    </div>
  {/each}

  <button class="btn-add-rule" on:click={addRule}>+ Add Transform Rule</button>
</div>

<style>
  .rule-editor {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }

  .rule-card {
    background: var(--bg-primary);
    border: 1px solid var(--border);
    border-radius: 6px;
    overflow: hidden;
  }

  .rule-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 6px 8px;
    background: var(--bg-tertiary);
    border-bottom: 1px solid var(--border);
  }

  .rule-type-select {
    font-size: 11px;
    font-weight: 600;
    padding: 2px 6px;
    background: var(--bg-primary);
    border: 1px solid var(--border);
    border-radius: 4px;
    color: var(--text-primary);
  }

  .rule-actions {
    display: flex;
    gap: 2px;
  }
  .rule-btn {
    padding: 2px 6px;
    border-radius: 3px;
    font-size: 10px;
    color: var(--text-muted);
    transition: all 150ms ease;
  }
  .rule-btn:hover { color: var(--text-primary); background: var(--border); }
  .rule-btn.danger:hover { color: var(--failed); background: var(--failed-bg); }
  .rule-btn.mini { padding: 1px 4px; font-size: 9px; }

  .rule-body {
    padding: 8px;
  }

  .mini-field {
    margin-bottom: 6px;
  }
  .mini-field label {
    display: block;
    font-size: 9.5px;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.06em;
    margin-bottom: 2px;
  }
  .mini-field input,
  .mini-field select {
    width: 100%;
    font-size: 11px;
    padding: 3px 6px;
  }

  .mapping-list {
    display: flex;
    flex-direction: column;
    gap: 3px;
  }
  .mapping-row {
    display: flex;
    align-items: center;
    gap: 4px;
  }
  .mapping-row input {
    flex: 1;
    font-size: 11px;
    padding: 3px 6px;
  }
  .arrow {
    color: var(--text-muted);
    font-size: 10px;
    font-family: var(--font-mono);
    flex-shrink: 0;
  }

  .toggle-inline {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 11px;
    color: var(--text-secondary);
    cursor: pointer;
  }
  .toggle-inline input { width: 14px; height: 14px; accent-color: var(--accent); }

  .agg-fields { margin-top: 6px; }
  .agg-title {
    font-size: 9.5px;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.06em;
    display: block;
    margin-bottom: 4px;
  }
  .agg-row {
    display: flex;
    gap: 4px;
    margin-bottom: 3px;
  }
  .agg-row select,
  .agg-row input {
    flex: 1;
    font-size: 11px;
    padding: 3px 6px;
  }
  .agg-row select { flex: 0.7; }

  .btn-add-row,
  .btn-add-rule {
    font-size: 11px;
    color: var(--accent);
    padding: 4px 8px;
    border-radius: 4px;
    transition: all 150ms ease;
    text-align: left;
    margin-top: 4px;
  }
  .btn-add-row:hover,
  .btn-add-rule:hover {
    background: var(--accent-glow);
  }
  .btn-add-rule {
    font-weight: 500;
  }
</style>
