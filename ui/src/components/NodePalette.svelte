<script lang="ts">
  import { nodeTypeConfig } from "../lib/dag";
  import { icons, nodeTypeIcon } from "../lib/icons";
  import { createEventDispatcher } from "svelte";

  const dispatch = createEventDispatcher<{ add: string }>();
  let searchQuery = "";

  const categories = [
    { title: "Sources", types: ["source_file", "source_api", "source_db"] },
    { title: "Processing", types: ["transform", "code", "join", "quality_check", "sql_generate"] },
    { title: "Outputs", types: ["sink_file", "sink_db", "sink_api"] },
    { title: "Integrations", types: ["dbt", "notify"] },
    { title: "Migration", types: ["migrate"] },
    { title: "Flow Control", types: ["condition"] },
  ];

  function onDragStart(e: DragEvent, type: string) {
    e.dataTransfer?.setData("text/plain", type);
    if (e.dataTransfer) e.dataTransfer.effectAllowed = "copy";
  }

  $: visibleCategories = categories
    .map((category) => ({
      ...category,
      types: category.types.filter((type) =>
        nodeTypeConfig[type].label.toLowerCase().includes(searchQuery.trim().toLowerCase()),
      ),
    }))
    .filter((category) => category.types.length > 0);
</script>

<div class="palette">
  <div class="palette-header">
    <div><strong>Node library</strong><span>Drag or click to add</span></div>
    <label class="palette-search">
      <svg width="14" height="14" viewBox="0 0 24 24" fill="none" aria-hidden="true"><path d={icons.search.d} stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /></svg>
      <input bind:value={searchQuery} type="search" placeholder="Find a node" aria-label="Search nodes" />
    </label>
  </div>

  {#each visibleCategories as cat}
    <div class="category">
      <span class="cat-title">{cat.title}</span>
      {#each cat.types as type}
        {@const config = nodeTypeConfig[type]}
        {@const iconDef = icons[nodeTypeIcon(type)]}
        <button
          type="button"
          class="palette-item"
          draggable="true"
          on:dragstart={(e) => onDragStart(e, type)}
          on:click={() => dispatch("add", type)}
          title="Add {config.label} node"
        >
          <div class="item-icon">
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none">
              <path
                d={iconDef.d}
                stroke={config.color}
                stroke-width="1.5"
                stroke-linecap="round"
                stroke-linejoin="round"
              />
            </svg>
          </div>
          <span class="item-label">{config.label}</span>
        </button>
      {/each}
    </div>
  {/each}
  {#if visibleCategories.length === 0}<p class="no-results">No nodes match “{searchQuery}”.</p>{/if}
</div>

<style>
  .palette {
    display: flex;
    flex-direction: column;
  }

  .palette-header {
    padding: 14px 12px 12px;
    border-bottom: 1px solid var(--border-subtle);
  }
  .palette-header > div { display: flex; align-items: baseline; justify-content: space-between; gap: 8px; margin-bottom: 10px; }
  .palette-header strong { color: var(--text-primary); font-size: 12px; }
  .palette-header span { color: var(--text-dim); font-size: 9px; }
  .palette-search { display: flex; align-items: center; gap: 7px; padding: 6px 8px; border: 1px solid var(--border-subtle); border-radius: 6px; background: var(--bg-primary); color: var(--text-muted); }
  .palette-search:focus-within { border-color: var(--accent); }
  .palette-search input { width: 100%; min-width: 0; padding: 0; border: 0; outline: 0; background: transparent; color: var(--text-primary); font-size: 11px; }

  .category {
    padding: 12px;
  }

  .cat-title {
    font-size: 10px;
    color: var(--text-dim);
    text-transform: uppercase;
    letter-spacing: 0.1em;
    font-weight: 600;
    padding: 0 4px;
    margin-bottom: 6px;
    display: block;
  }

  .palette-item {
    width: 100%;
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px 10px;
    border: 1px solid transparent;
    border-radius: 6px;
    cursor: grab;
    transition: all 150ms ease;
    margin-bottom: 2px;
    color: inherit;
    text-align: left;
  }
  .palette-item:hover {
    border-color: var(--border-subtle);
    background: var(--bg-card-hover);
  }
  .palette-item:active {
    cursor: grabbing;
    opacity: 0.6;
    transform: scale(0.98);
  }

  .item-icon {
    width: 28px;
    height: 28px;
    display: flex;
    align-items: center;
    justify-content: center;
    border-radius: 6px;
    background: transparent;
    flex-shrink: 0;
  }

  .item-label {
    font-size: 12.5px;
    font-weight: 500;
    color: var(--text-secondary);
  }
  .palette-item:hover .item-label {
    color: var(--text-primary);
  }
  .no-results { padding: 20px 14px; color: var(--text-muted); font-size: 11px; text-align: center; }
</style>
