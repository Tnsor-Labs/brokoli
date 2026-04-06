<script lang="ts">
  import { nodeTypeConfig } from "../lib/dag";
  import { icons, nodeTypeIcon } from "../lib/icons";

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
</script>

<div class="palette">
  <div class="palette-header">Nodes</div>

  {#each categories as cat}
    <div class="category">
      <span class="cat-title">{cat.title}</span>
      {#each cat.types as type}
        {@const config = nodeTypeConfig[type]}
        {@const iconDef = icons[nodeTypeIcon(type)]}
        <!-- svelte-ignore a11y_no_static_element_interactions -->
        <div
          class="palette-item"
          draggable="true"
          on:dragstart={(e) => onDragStart(e, type)}
          on:keydown={() => {}}
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
        </div>
      {/each}
    </div>
  {/each}
</div>

<style>
  .palette {
    display: flex;
    flex-direction: column;
  }

  .palette-header {
    padding: 12px 16px;
    font-size: 11px;
    font-weight: 600;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.1em;
    border-bottom: 1px solid var(--border-subtle);
  }

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
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px 10px;
    border: 1px solid transparent;
    border-radius: 6px;
    cursor: grab;
    transition: all 150ms ease;
    margin-bottom: 2px;
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
</style>
