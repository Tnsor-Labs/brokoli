<script lang="ts">
  import { nodeTypeConfig } from "../lib/dag";
  import { icons, brandNodeIcon } from "../lib/icons";
  import BrandIcon from "./BrandIcon.svelte";
  import { createEventDispatcher } from "svelte";
  import type { NodeType, PaletteDrop } from "../lib/types";

  const dispatch = createEventDispatcher<{ add: string; paletteDrop: PaletteDrop }>();
  let searchQuery = "";

  const categories: { title: string; types: NodeType[] }[] = [
    { title: "Sources", types: ["source_file", "source_api", "source_db"] },
    { title: "Processing", types: ["transform", "code", "join", "quality_check", "sql_generate"] },
    { title: "Outputs", types: ["sink_file", "sink_db", "sink_api"] },
    { title: "Extensions", types: ["dbt", "notify"] },
    { title: "Migration", types: ["migrate"] },
    { title: "Flow Control", types: ["condition"] },
  ];

  // Pointer Events, not native HTML5 drag-and-drop: the canvas is an SVG
  // drop target, and native dragover/drop on SVG has enough cross-browser
  // quirkiness (and no touch support) that a small pointer-capture-based
  // implementation is more reliable than fighting the native DnD API.
  const DRAG_THRESHOLD = 4;

  interface ActiveDrag {
    pointerId: number;
    type: NodeType;
    startX: number;
    startY: number;
    moved: boolean;
    source: HTMLElement;
  }

  let activeDrag: ActiveDrag | null = null;
  // A completed drag's pointerup is followed by a synthetic click on the
  // same element (preventDefault on pointerdown does not suppress it) —
  // without this, every drag-drop would also fire the click-to-add
  // handler and add a second node.
  let suppressNextClick = false;

  function onPointerDown(e: PointerEvent, type: NodeType) {
    if (activeDrag || !e.isPrimary || e.button !== 0) return;

    e.preventDefault();
    const source = e.currentTarget as HTMLElement;
    source.setPointerCapture(e.pointerId);
    activeDrag = {
      pointerId: e.pointerId,
      type,
      startX: e.clientX,
      startY: e.clientY,
      moved: false,
      source,
    };
  }

  function onPointerMove(e: PointerEvent) {
    if (!activeDrag || e.pointerId !== activeDrag.pointerId || activeDrag.moved) return;

    if (
      Math.hypot(e.clientX - activeDrag.startX, e.clientY - activeDrag.startY) >= DRAG_THRESHOLD
    ) {
      activeDrag.moved = true;
      activeDrag = activeDrag;
    }
  }

  function clearDrag(pointerId?: number): ActiveDrag | null {
    if (!activeDrag || (pointerId !== undefined && pointerId !== activeDrag.pointerId)) return null;

    const drag = activeDrag;
    activeDrag = null;
    if (drag.source.hasPointerCapture(drag.pointerId)) {
      drag.source.releasePointerCapture(drag.pointerId);
    }
    return drag;
  }

  function onPointerUp(e: PointerEvent) {
    const drag = clearDrag(e.pointerId);
    if (!drag?.moved) return;

    suppressNextClick = true;
    dispatch("paletteDrop", { type: drag.type, clientX: e.clientX, clientY: e.clientY });
  }

  function onItemClick(type: NodeType) {
    if (suppressNextClick) {
      suppressNextClick = false;
      return;
    }
    dispatch("add", type);
  }

  function onPointerCancel(e: PointerEvent) {
    clearDrag(e.pointerId);
  }

  function onLostPointerCapture(e: PointerEvent) {
    clearDrag(e.pointerId);
  }

  function cancelDrag() {
    clearDrag();
  }

  function onWindowKeydown(e: KeyboardEvent) {
    if (e.key === "Escape") cancelDrag();
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

<svelte:window on:keydown={onWindowKeydown} on:blur={cancelDrag} />

<div class="palette">
  <div class="palette-header">
    <div><strong>Node library</strong><span>Drag or click to add</span></div>
    <label class="palette-search">
      <svg width="14" height="14" viewBox="0 0 24 24" fill="none" aria-hidden="true"
        ><path
          d={icons.search.d}
          stroke="currentColor"
          stroke-width="1.8"
          stroke-linecap="round"
        /></svg
      >
      <input
        bind:value={searchQuery}
        type="search"
        placeholder="Find a node"
        aria-label="Search nodes"
      />
    </label>
  </div>

  {#each visibleCategories as cat}
    <div class="category" style={`--family-color: ${nodeTypeConfig[cat.types[0]].color}`}>
      <span class="cat-title">{cat.title}</span>
      {#each cat.types as type}
        {@const config = nodeTypeConfig[type]}
        <button
          type="button"
          class="palette-item"
          class:dragging={activeDrag?.type === type}
          on:pointerdown={(e) => onPointerDown(e, type)}
          on:pointermove={onPointerMove}
          on:pointerup={onPointerUp}
          on:pointercancel={onPointerCancel}
          on:lostpointercapture={onLostPointerCapture}
          on:click={() => onItemClick(type)}
          title="Add {config.label} node"
        >
          <div class="item-icon" style={`color: ${config.color}`}>
            <BrandIcon name={brandNodeIcon(type)} size={16} />
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
  .palette-header > div {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 8px;
    margin-bottom: 10px;
  }
  .palette-header strong {
    color: var(--text-primary);
    font-size: 12px;
  }
  .palette-header span {
    color: var(--text-dim);
    font-size: 9px;
  }
  .palette-search {
    display: flex;
    align-items: center;
    gap: 7px;
    padding: 6px 8px;
    border: 1px solid var(--border-subtle);
    border-radius: 6px;
    background: var(--bg-primary);
    color: var(--text-muted);
  }
  .palette-search:focus-within {
    border-color: var(--accent);
  }
  .palette-search input {
    width: 100%;
    min-width: 0;
    padding: 0;
    border: 0;
    outline: 0;
    background: transparent;
    color: var(--text-primary);
    font-size: 11px;
  }

  .category {
    padding: 12px;
  }

  .cat-title {
    font-size: 10px;
    color: var(--family-color);
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
    font: inherit;
    text-align: left;
    touch-action: none;
    user-select: none;
  }
  .palette-item:hover {
    border-color: var(--border-subtle);
    background: var(--bg-card-hover);
  }
  .palette-item:active,
  .palette-item.dragging {
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
    background: color-mix(in srgb, var(--family-color) 10%, transparent);
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
  .no-results {
    padding: 20px 14px;
    color: var(--text-muted);
    font-size: 11px;
    text-align: center;
  }
</style>
