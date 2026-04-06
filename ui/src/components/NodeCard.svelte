<script lang="ts">
  import type { Node, RunStatus } from "../lib/types";
  import { NODE_WIDTH, NODE_HEIGHT, nodeTypeConfig } from "../lib/dag";
  import { icons, nodeTypeIcon } from "../lib/icons";
  import { createEventDispatcher } from "svelte";

  export let node: Node;
  export let selected: boolean = false;
  export let status: RunStatus | null = null;
  export let readonly: boolean = false;

  const dispatch = createEventDispatcher();

  $: config = nodeTypeConfig[node.type] || { label: node.type, color: "#71717a" };
  $: icon = icons[nodeTypeIcon(node.type)] || icons.file;

  let hovered = false;
  let dragging = false;

  function onMouseDown(e: MouseEvent) {
    if (readonly || e.button !== 0) return;
    if ((e.target as Element).classList.contains("port")) return;

    dragging = true;
    const startX = e.clientX - node.position.x;
    const startY = e.clientY - node.position.y;

    const onMove = (e: MouseEvent) => {
      if (!dragging) return;
      node.position = {
        x: Math.max(0, e.clientX - startX),
        y: Math.max(0, e.clientY - startY),
      };
      node = node;
    };
    const onUp = () => {
      dragging = false;
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
    if (!s) return config.color;
    const map: Record<string, string> = {
      pending: "var(--pending)",
      running: "var(--running)",
      success: "var(--success)",
      failed: "var(--failed)",
      cancelled: "var(--pending)",
    };
    return map[s] || config.color;
  }

  function statusLabel(s: RunStatus | null): string {
    if (!s) return "";
    const map: Record<string, string> = {
      pending: "PENDING",
      running: "RUNNING",
      success: "OK",
      failed: "FAILED",
      cancelled: "CANCEL",
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
  on:mouseenter={() => hovered = true}
  on:mouseleave={() => hovered = false}
  role="button"
  tabindex="0"
  on:keydown={() => {}}
>
  <!-- Shadow -->
  <rect
    x="2" y="2"
    width={NODE_WIDTH}
    height={NODE_HEIGHT}
    rx="8"
    fill="var(--shadow-node)"
    class="shadow"
  />

  <!-- Card body -->
  <rect
    class="card-bg"
    x="0" y="0"
    width={NODE_WIDTH}
    height={NODE_HEIGHT}
    rx="8"
  />

  <!-- Left accent bar — full height, clipped to card shape -->
  <clipPath id="left-bar-{node.id}">
    <rect x="0" y="0" width="36" height={NODE_HEIGHT} rx="8" />
  </clipPath>
  <rect
    x="0" y="0"
    width="36"
    height={NODE_HEIGHT}
    fill={config.color}
    opacity="0.08"
    clip-path="url(#left-bar-{node.id})"
  />
  <rect
    x="0" y="0"
    width="3"
    height={NODE_HEIGHT}
    fill={statusColor(status)}
    clip-path="url(#left-bar-{node.id})"
    class="stripe"
  />

  <!-- Icon — clean, no background -->
  <g transform="translate(12, {NODE_HEIGHT / 2 - 9}) scale(0.75)">
    <path
      d={icon.d}
      fill="none"
      stroke={config.color}
      stroke-width="1.5"
      stroke-linecap="round"
      stroke-linejoin="round"
    />
  </g>

  <!-- Separator line -->
  <line
    x1="36" y1="8"
    x2="36" y2={NODE_HEIGHT - 8}
    stroke={config.color}
    opacity="0.15"
    stroke-width="1"
  />

  <!-- Name -->
  <text x="44" y={NODE_HEIGHT / 2 - 5} class="node-name">
    {(node.name || config.label).length > 18
      ? (node.name || config.label).slice(0, 17) + "…"
      : (node.name || config.label)}
  </text>

  <!-- Type label -->
  <text x="44" y={NODE_HEIGHT / 2 + 10} class="node-type">
    {config.label}
  </text>

  <!-- Status badge (top-right) -->
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
    <circle
      class="port port-visual"
      cx="-1"
      cy={NODE_HEIGHT / 2}
      r="5"
    />
    <circle
      class="port-dot"
      cx="-1"
      cy={NODE_HEIGHT / 2}
      r="2"
    />
  </g>

  <!-- Output port (right) -->
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
    <circle
      class="port port-visual"
      cx={NODE_WIDTH + 1}
      cy={NODE_HEIGHT / 2}
      r="5"
    />
    <circle
      class="port-dot"
      cx={NODE_WIDTH + 1}
      cy={NODE_HEIGHT / 2}
      r="2"
    />
  </g>
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
    transition: stroke 150ms ease, fill 150ms ease;
  }
  .hovered .card-bg {
    fill: var(--bg-card-hover);
    stroke: var(--border-hover);
  }
  .selected .card-bg {
    stroke: var(--accent);
    stroke-width: 1.5;
  }
  .running .card-bg {
    animation: running-glow 2s ease-in-out infinite;
  }

  .stripe {
    transition: fill 200ms ease;
  }

  .node-name {
    fill: var(--text-primary);
    font-family: 'Inter', system-ui, sans-serif;
    font-size: 12px;
    font-weight: 600;
    letter-spacing: -0.01em;
  }
  .node-type {
    fill: var(--text-muted);
    font-family: 'JetBrains Mono', monospace;
    font-size: 9px;
    letter-spacing: 0.04em;
    text-transform: uppercase;
  }

  .status-text {
    font-family: 'JetBrains Mono', monospace;
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
    stroke: var(--accent);
    stroke-width: 1.5;
    transition: all 150ms ease;
    pointer-events: none;
  }
  .port-hit:hover ~ .port-visual {
    fill: var(--accent);
    r: 6;
  }

  .port-dot {
    fill: var(--accent);
    pointer-events: none;
    transition: all 150ms ease;
  }
  .port-hit:hover ~ .port-dot {
    fill: var(--bg-primary);
  }

  @keyframes running-glow {
    0%, 100% { stroke: var(--running); stroke-opacity: 0.8; }
    50% { stroke: var(--running); stroke-opacity: 0.3; }
  }
</style>
