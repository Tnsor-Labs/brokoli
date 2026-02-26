<script lang="ts">
  import type { Node, Edge, RunStatus } from "../lib/types";
  import { edgePath, outputPort, inputPort, NODE_WIDTH, NODE_HEIGHT } from "../lib/dag";
  import NodeCard from "./NodeCard.svelte";
  import { createEventDispatcher } from "svelte";
  import { theme } from "../lib/theme";

  export let nodes: Node[] = [];
  export let edges: Edge[] = [];
  export let selectedNodeId: string | null = null;
  export let nodeStatuses: Record<string, RunStatus> = {};
  export let readonly: boolean = false;

  const dispatch = createEventDispatcher();

  let svgEl: SVGSVGElement;
  let viewBox = { x: 0, y: 0, w: 1200, h: 600 };

  // Connection drawing state
  let drawing = false;
  let drawFromNodeId: string | null = null;
  let drawFromSide: "input" | "output" | null = null;
  let drawStart = { x: 0, y: 0 };
  let drawEnd = { x: 0, y: 0 };
  let nearestTarget: { nodeId: string; side: string; pos: { x: number; y: number } } | null = null;

  // Pan state
  let panning = false;
  let panStart = { x: 0, y: 0 };

  // --- Theme-aware colors for SVG defs ---
  // SVG <pattern> and <marker> fills can't use CSS variables in all browsers,
  // so we derive them from the theme store.
  $: isDark = $theme === "dark";
  $: dotColor = isDark ? "rgba(39,39,42,0.5)" : "rgba(208,213,221,0.6)";
  $: dotLargeColor = isDark ? "rgba(63,63,70,0.3)" : "rgba(152,162,179,0.35)";
  $: edgeColor = isDark ? "#3f3f46" : "#667085";
  $: edgeHoverColor = isDark ? "#ef4444" : "#f04438";
  $: accentColor = isDark ? "#6366f1" : "#0d9488";
  $: flowColor = isDark ? "#3b82f6" : "#2563eb";

  // --- Coordinate conversion using SVG native API ---
  function clientToSvg(clientX: number, clientY: number): { x: number; y: number } {
    if (!svgEl) return { x: clientX, y: clientY };
    const pt = svgEl.createSVGPoint();
    pt.x = clientX;
    pt.y = clientY;
    const ctm = svgEl.getScreenCTM();
    if (!ctm) return { x: clientX, y: clientY };
    const svgPt = pt.matrixTransform(ctm.inverse());
    return { x: svgPt.x, y: svgPt.y };
  }

  function nodeMap(): Map<string, Node> {
    return new Map(nodes.map((n) => [n.id, n]));
  }

  function getEdgePaths() {
    const nm = nodeMap();
    return edges
      .map((e) => {
        const from = nm.get(e.from);
        const to = nm.get(e.to);
        if (!from || !to) return null;
        return {
          path: edgePath(outputPort(from), inputPort(to)),
          fromId: e.from,
          toId: e.to,
        };
      })
      .filter(Boolean) as { path: string; fromId: string; toId: string }[];
  }

  function findNearestPort(svgX: number, svgY: number, excludeNodeId: string, dragFromSide: "input" | "output"): typeof nearestTarget {
    const SNAP_DISTANCE = 50;
    let best: typeof nearestTarget = null;
    let bestDist = SNAP_DISTANCE;

    for (const node of nodes) {
      if (node.id === excludeNodeId) continue;

      const ports: { side: "input" | "output"; pos: { x: number; y: number } }[] = [
        { side: "input", pos: inputPort(node) },
        { side: "output", pos: outputPort(node) },
      ];

      for (const port of ports) {
        if (port.side === dragFromSide) continue;

        const dx = svgX - port.pos.x;
        const dy = svgY - port.pos.y;
        const dist = Math.sqrt(dx * dx + dy * dy);

        if (dist < bestDist) {
          bestDist = dist;
          best = { nodeId: node.id, side: port.side, pos: port.pos };
        }
      }
    }
    return best;
  }

  function onPortDragStart(e: CustomEvent<{ nodeId: string; side: "input" | "output"; x: number; y: number }>) {
    if (readonly) return;
    const { nodeId, side } = e.detail;
    drawing = true;
    drawFromNodeId = nodeId;
    drawFromSide = side;

    const nm = nodeMap();
    const node = nm.get(nodeId);
    if (!node) return;

    drawStart = side === "output" ? outputPort(node) : inputPort(node);
    drawEnd = { ...drawStart };

    const onMove = (ev: MouseEvent) => {
      const pt = clientToSvg(ev.clientX, ev.clientY);
      const snap = findNearestPort(pt.x, pt.y, nodeId, side);
      nearestTarget = snap;
      drawEnd = snap ? snap.pos : pt;
    };

    const onUp = () => {
      window.removeEventListener("mousemove", onMove);
      window.removeEventListener("mouseup", onUp);

      if (nearestTarget && drawFromNodeId) {
        let from: string;
        let to: string;

        if (drawFromSide === "output" && nearestTarget.side === "input") {
          from = drawFromNodeId;
          to = nearestTarget.nodeId;
        } else if (drawFromSide === "input" && nearestTarget.side === "output") {
          from = nearestTarget.nodeId;
          to = drawFromNodeId;
        } else {
          from = drawFromNodeId;
          to = nearestTarget.nodeId;
        }

        if (from !== to && !edges.some((e) => e.from === from && e.to === to)) {
          edges = [...edges, { from, to }];
          dispatch("edgeAdded", { from, to });
        }
      }

      drawing = false;
      drawFromNodeId = null;
      drawFromSide = null;
      nearestTarget = null;
    };

    window.addEventListener("mousemove", onMove);
    window.addEventListener("mouseup", onUp);
  }

  function onPortDragEnd(e: CustomEvent<{ nodeId: string; side: "input" | "output" }>) {
    if (!drawing || !drawFromNodeId) return;
    const { nodeId: targetNodeId, side: targetSide } = e.detail;
    if (drawFromNodeId === targetNodeId) return;

    let from: string;
    let to: string;
    if (drawFromSide === "output" && targetSide === "input") {
      from = drawFromNodeId;
      to = targetNodeId;
    } else if (drawFromSide === "input" && targetSide === "output") {
      from = targetNodeId;
      to = drawFromNodeId;
    } else {
      return;
    }

    if (!edges.some((e) => e.from === from && e.to === to)) {
      edges = [...edges, { from, to }];
    }

    drawing = false;
    drawFromNodeId = null;
    nearestTarget = null;
  }

  function selectNode(nodeId: string) {
    selectedNodeId = nodeId;
    dispatch("selectNode", nodeId);
  }

  function onCanvasClick(e: MouseEvent) {
    if (e.target === e.currentTarget || (e.target as Element).classList.contains("canvas-bg")) {
      selectedNodeId = null;
      dispatch("selectNode", null);
    }
  }

  function onCanvasMouseDown(e: MouseEvent) {
    if (e.button === 1 || (e.button === 0 && e.altKey)) {
      panning = true;
      panStart = { x: e.clientX + viewBox.x, y: e.clientY + viewBox.y };
      e.preventDefault();

      const onMove = (ev: MouseEvent) => {
        viewBox.x = panStart.x - ev.clientX;
        viewBox.y = panStart.y - ev.clientY;
        viewBox = viewBox;
      };
      const onUp = () => {
        panning = false;
        window.removeEventListener("mousemove", onMove);
        window.removeEventListener("mouseup", onUp);
      };
      window.addEventListener("mousemove", onMove);
      window.addEventListener("mouseup", onUp);
    }
  }

  function onDrop(e: DragEvent) {
    if (readonly) return;
    e.preventDefault();
    const nodeType = e.dataTransfer?.getData("text/plain");
    if (!nodeType) return;
    const pt = clientToSvg(e.clientX, e.clientY);
    dispatch("addNode", { type: nodeType, x: pt.x - NODE_WIDTH / 2, y: pt.y - NODE_HEIGHT / 2 });
  }

  function onDragOver(e: DragEvent) { e.preventDefault(); }

  function onDeleteEdge(fromId: string, toId: string) {
    edges = edges.filter((e) => !(e.from === fromId && e.to === toId));
  }

  // Recompute edge paths whenever nodes or edges change
  $: edgePaths = (() => { nodes; edges; return getEdgePaths(); })();
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<svg
  class="canvas"
  bind:this={svgEl}
  viewBox="{viewBox.x} {viewBox.y} {viewBox.w} {viewBox.h}"
  on:click={onCanvasClick}
  on:mousedown={onCanvasMouseDown}
  on:drop={onDrop}
  on:dragover={onDragOver}
  on:keydown={() => {}}
>
  <defs>
    <pattern id="grid-small" width="20" height="20" patternUnits="userSpaceOnUse">
      <circle cx="10" cy="10" r="0.5" fill={dotColor} />
    </pattern>
    <pattern id="grid-large" width="100" height="100" patternUnits="userSpaceOnUse">
      <rect width="100" height="100" fill="url(#grid-small)" />
      <circle cx="0" cy="0" r="0.8" fill={dotLargeColor} />
    </pattern>
    <marker id="arrowhead" markerWidth="8" markerHeight="6" refX="7" refY="3" orient="auto">
      <polygon points="0 0.5, 7 3, 0 5.5" fill={edgeColor} />
    </marker>
    <marker id="arrowhead-hover" markerWidth="8" markerHeight="6" refX="7" refY="3" orient="auto">
      <polygon points="0 0.5, 7 3, 0 5.5" fill={edgeHoverColor} />
    </marker>
    <marker id="arrowhead-active" markerWidth="8" markerHeight="6" refX="7" refY="3" orient="auto">
      <polygon points="0 0.5, 7 3, 0 5.5" fill={accentColor} />
    </marker>
    <marker id="arrowhead-drawing" markerWidth="8" markerHeight="6" refX="7" refY="3" orient="auto">
      <polygon points="0 0.5, 7 3, 0 5.5" fill={accentColor} opacity="0.6" />
    </marker>
  </defs>

  <rect
    class="canvas-bg"
    x={viewBox.x - 200}
    y={viewBox.y - 200}
    width={viewBox.w + 400}
    height={viewBox.h + 400}
    fill="url(#grid-large)"
  />

  <!-- Edges -->
  {#each edgePaths as edge}
    <g class="edge-group">
      {#if !readonly}
        <!-- svelte-ignore a11y_no_static_element_interactions -->
        <path
          class="edge-hit"
          d={edge.path}
          fill="none"
          stroke="transparent"
          stroke-width="14"
          on:dblclick={() => onDeleteEdge(edge.fromId, edge.toId)}
          on:keydown={() => {}}
        />
      {/if}
      <path
        class="edge-path"
        d={edge.path}
        fill="none"
        stroke={edgeColor}
        stroke-width="1.5"
        marker-end="url(#arrowhead)"
      />
      {#if nodeStatuses[edge.fromId] === "success" && nodeStatuses[edge.toId] === "running"}
        <path
          class="edge-flow"
          d={edge.path}
          fill="none"
          stroke={flowColor}
          stroke-width="2"
          stroke-dasharray="6 6"
          marker-end="url(#arrowhead-active)"
        />
      {/if}
    </g>
  {/each}

  <!-- Drawing edge preview -->
  {#if drawing}
    <path
      class="edge-drawing"
      d={edgePath(drawStart, drawEnd)}
      fill="none"
      stroke={accentColor}
      stroke-width="2"
      stroke-dasharray="6 4"
      opacity={nearestTarget ? 0.9 : 0.5}
      marker-end="url(#arrowhead-drawing)"
    />
    {#if nearestTarget}
      <circle
        cx={nearestTarget.pos.x}
        cy={nearestTarget.pos.y}
        r="8"
        fill="none"
        stroke={accentColor}
        stroke-width="2"
        class="snap-ring"
      />
      <circle
        cx={nearestTarget.pos.x}
        cy={nearestTarget.pos.y}
        r="4"
        fill={accentColor}
        opacity="0.6"
      />
    {/if}
  {/if}

  <!-- Nodes -->
  {#each nodes as node (node.id)}
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <g on:click|stopPropagation={() => selectNode(node.id)} on:keydown={() => {}}>
      <NodeCard
        bind:node={node}
        selected={selectedNodeId === node.id}
        status={nodeStatuses[node.id] || null}
        {readonly}
        on:portDragStart={onPortDragStart}
        on:portDragEnd={onPortDragEnd}
      />
    </g>
  {/each}

  <!-- Empty state -->
  {#if nodes.length === 0 && !readonly}
    <text x={viewBox.x + viewBox.w / 2} y={viewBox.y + viewBox.h / 2} text-anchor="middle" class="empty-text">
      Drag nodes from the palette to build your pipeline
    </text>
    <text x={viewBox.x + viewBox.w / 2} y={viewBox.y + viewBox.h / 2 + 22} text-anchor="middle" class="empty-hint">
      Connect them by dragging from port to port
    </text>
  {/if}
</svg>

<style>
  .canvas {
    width: 100%;
    height: 100%;
    background: var(--bg-canvas);
    border-radius: 8px;
    border: 1px solid var(--border-subtle);
    user-select: none;
    transition: background 200ms ease, border-color 200ms ease;
  }

  .canvas-bg { cursor: default; }

  .edge-path { transition: stroke 200ms ease; }
  .edge-hit { cursor: pointer; }
  .edge-hit:hover + .edge-path {
    stroke: var(--canvas-edge-hover);
    stroke-width: 2;
  }

  .edge-flow { animation: flow 0.8s linear infinite; }
  .edge-drawing { animation: dash 0.6s linear infinite; }

  .snap-ring { animation: snap-pulse 0.8s ease-in-out infinite; }

  @keyframes flow { to { stroke-dashoffset: -12; } }
  @keyframes dash { to { stroke-dashoffset: -10; } }
  @keyframes snap-pulse {
    0%, 100% { r: 8; opacity: 0.8; }
    50% { r: 11; opacity: 0.4; }
  }

  .empty-text {
    fill: var(--text-dim);
    font-family: 'Inter', system-ui, sans-serif;
    font-size: 14px;
    font-weight: 500;
  }
  .empty-hint {
    fill: var(--text-ghost);
    font-family: 'Inter', system-ui, sans-serif;
    font-size: 12px;
  }
</style>
