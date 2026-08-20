<script lang="ts">
  import type { Node, RunStatus } from "../lib/types";
  import { NODE_WIDTH, NODE_HEIGHT, nodeTypeConfig, nodePortConfig } from "../lib/dag";
  import { brandNodeIcon } from "../lib/icons";
  import BrandIcon from "./BrandIcon.svelte";
  import { createEventDispatcher } from "svelte";

  export let node: Node;
  export let selected: boolean = false;
  export let status: RunStatus | null = null;
  export let readonly: boolean = false;

  const dispatch = createEventDispatcher();

  $: config = nodeTypeConfig[node.type] || { label: node.type, color: "#71717a" };
  $: portConfig = nodePortConfig[node.type] ?? { hasInput: true, hasOutput: true, maxInputs: 1 };

  let hovered = false;
  let dragging = false;

  function onMouseDown(e: MouseEvent) {
    if (readonly || e.button !== 0) return;
    if ((e.target as Element).classList.contains("port")) return;

    dragging = true;
    let moved = false;
    const startX = e.clientX - node.position.x;
    const startY = e.clientY - node.position.y;

    const onMove = (e: MouseEvent) => {
      if (!dragging) return;
      if (!moved) dispatch("moveStart", node.id);
      moved = true;
      node.position = {
        x: Math.max(0, e.clientX - startX),
        y: Math.max(0, e.clientY - startY),
      };
      node = node;
    };
    const onUp = () => {
      dragging = false;
      if (moved) dispatch("moveEnd", node.id);
      window.removeEventListener("mousemove", onMove);
      window.removeEventListener("mouseup", onUp);
    };
    window.addEventListener("mousemove", onMove);
    window.addEventListener("mouseup", onUp);
  }

  function onPortMouseDown(e: MouseEvent, side: "input" | "output") {
    e.stopPropagation();
    dispatch("portDragStart", { nodeId: node.id, side, x: e.clientX, y: e.clientY });
  }

  function onPortMouseUp(e: MouseEvent, side: "input" | "output") {
    e.stopPropagation();
    dispatch("portDragEnd", { nodeId: node.id, side });
  }

  function statusColor(s: RunStatus | null): string {
    if (!s) return "var(--text-muted)";
    const map: Record<string, string> = {
      pending: "var(--pending)",
      running: "var(--running)",
      success: "var(--success)",
      failed: "var(--failed)",
      cancelled: "var(--pending)",
      skipped: "var(--text-muted)",
    };
    return map[s] || "var(--text-muted)";
  }

  function statusLabel(s: RunStatus | null): string {
    if (!s) return "";
    const map: Record<string, string> = {
      pending: "PENDING",
      running: "RUNNING",
      success: "SUCCESS",
      failed: "FAILED",
      cancelled: "CANCELLED",
      skipped: "SKIPPED",
    };
    return map[s] || "";
  }
</script>

<g
  class="node-card"
  class:selected
  class:hovered
  class:running={status === "running"}
  transform="translate({node.position.x}, {node.position.y})"
  on:mousedown={onMouseDown}
  on:mouseenter={() => (hovered = true)}
  on:mouseleave={() => (hovered = false)}
  role="button"
  tabindex="0"
  on:keydown={() => {}}
>
  <!-- Shadow -->
  <rect
    x="2"
    y="2"
    width={NODE_WIDTH}
    height={NODE_HEIGHT}
    rx="8"
    fill="var(--shadow-node)"
    class="shadow"
  />

  <!-- Card body -->
  <rect class="card-bg" x="0" y="0" width={NODE_WIDTH} height={NODE_HEIGHT} rx="8" />

  <!-- Family rail stays independent from runtime status. -->
  <clipPath id="left-bar-{node.id}">
    <rect x="0" y="0" width="8" height={NODE_HEIGHT} rx="8" />
  </clipPath>
  <rect
    x="0"
    y="0"
    width="4"
    height={NODE_HEIGHT}
    fill={config.color}
    clip-path="url(#left-bar-{node.id})"
  />

  <!-- 28px family tile keeps taxonomy local to the icon. -->
  <g class="icon-tile" transform="translate(8, {NODE_HEIGHT / 2 - 14})" color={config.color}>
    <rect width="28" height="28" rx="6" fill={config.color} opacity="0.1" />
    <g transform="translate(5, 5)">
      <BrandIcon name={brandNodeIcon(node.type)} size={18} />
    </g>
  </g>

  <!-- Separator line -->
  <line
    x1="40"
    y1="8"
    x2="40"
    y2={NODE_HEIGHT - 8}
    stroke={config.color}
    opacity="0.15"
    stroke-width="1"
  />

  <!-- Name -->
  <text x="48" y={NODE_HEIGHT / 2 - 5} class="node-name">
    {(node.name || config.label).length > 18
      ? (node.name || config.label).slice(0, 17) + "…"
      : node.name || config.label}
  </text>

  <!-- Type label -->
  <text x="48" y={NODE_HEIGHT / 2 + 10} class="node-type">
    {config.label}
  </text>

  <!-- Runtime status remains an explicit, independent marker. -->
  {#if status}
    <g class="status-badge">
      <rect
        x={NODE_WIDTH - 8 - statusLabel(status).length * 5.2}
        y="5"
        width={statusLabel(status).length * 5.2 + 8}
        height="14"
        rx="3"
        fill={statusColor(status)}
        opacity="0.12"
      />
      <text
        x={NODE_WIDTH - 4}
        y="15"
        text-anchor="end"
        class="status-text"
        fill={statusColor(status)}
      >
        {statusLabel(status)}
      </text>
    </g>
  {/if}

  <!-- Input port (left) -->
  {#if portConfig.hasInput}
    <g class="port-group" class:visible={hovered || selected || !readonly}>
      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <circle
        class="port port-hit"
        cx="-1"
        cy={NODE_HEIGHT / 2}
        r="12"
        on:mousedown={(e) => onPortMouseDown(e, "input")}
        on:mouseup={(e) => onPortMouseUp(e, "input")}
      />
      <circle class="port port-visual" cx="-1" cy={NODE_HEIGHT / 2} r="5" />
      <circle class="port-dot" cx="-1" cy={NODE_HEIGHT / 2} r="2" />
    </g>
  {/if}

  <!-- Output port (right) -->
  {#if portConfig.hasOutput}
    <g class="port-group" class:visible={hovered || selected || !readonly}>
      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <circle
        class="port port-hit"
        cx={NODE_WIDTH + 1}
        cy={NODE_HEIGHT / 2}
        r="12"
        on:mousedown={(e) => onPortMouseDown(e, "output")}
        on:mouseup={(e) => onPortMouseUp(e, "output")}
      />
      <circle class="port port-visual" cx={NODE_WIDTH + 1} cy={NODE_HEIGHT / 2} r="5" />
      <circle class="port-dot" cx={NODE_WIDTH + 1} cy={NODE_HEIGHT / 2} r="2" />
    </g>
  {/if}

  <!-- Standalone label for migrate (no ports) -->
  {#if node.type === "migrate"}
    <text x={NODE_WIDTH / 2} y={NODE_HEIGHT - 6} text-anchor="middle" class="standalone-label"
      >standalone</text
    >
  {/if}
</g>

<style>
  .node-card {
    cursor: grab;
  }
  .node-card:active {
    cursor: grabbing;
  }

  .shadow {
    opacity: 0;
    transition: opacity 200ms ease;
  }
  .hovered .shadow,
  .selected .shadow {
    opacity: 1;
  }

  .card-bg {
    fill: var(--bg-secondary);
    stroke: var(--border);
    stroke-width: 1;
    transition:
      stroke 150ms ease,
      fill 150ms ease;
  }
  .hovered .card-bg {
    fill: var(--bg-card-hover);
    stroke: var(--border-hover);
  }
  .selected .card-bg {
    stroke: var(--accent);
    stroke-width: 1.5;
  }
  .node-name {
    fill: var(--text-primary);
    font-family: "Inter", system-ui, sans-serif;
    font-size: 12px;
    font-weight: 600;
    letter-spacing: -0.01em;
  }
  .node-type {
    fill: var(--text-muted);
    font-family: "JetBrains Mono", monospace;
    font-size: 9px;
    letter-spacing: 0.04em;
    text-transform: uppercase;
  }

  .status-text {
    font-family: "JetBrains Mono", monospace;
    font-size: 7.5px;
    font-weight: 600;
    letter-spacing: 0.05em;
  }

  /* Ports */
  .port-group {
    opacity: 0;
    transition: opacity 150ms ease;
  }
  .port-group.visible {
    opacity: 1;
  }

  .port-hit {
    fill: transparent;
    cursor: crosshair;
  }

  .port-visual {
    fill: var(--bg-primary);
    stroke: var(--border-hover);
    stroke-width: 1.5;
    transition: all 150ms ease;
    pointer-events: none;
  }
  .port-hit:hover ~ .port-visual {
    fill: var(--accent);
    r: 6;
  }

  .port-dot {
    fill: var(--border-hover);
    pointer-events: none;
    transition: all 150ms ease;
  }
  .port-hit:hover ~ .port-dot {
    fill: var(--bg-primary);
  }

  .standalone-label {
    fill: var(--text-muted);
    font-family: "JetBrains Mono", monospace;
    font-size: 8px;
    letter-spacing: 0.04em;
    opacity: 0.6;
  }
</style>
