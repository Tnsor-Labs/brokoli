<script lang="ts">
  import { onMount } from "svelte";
  import { notify } from "../lib/toast";
  import { authHeaders } from "../lib/auth";
  import { theme } from "../lib/theme";
  import Skeleton from "../components/Skeleton.svelte";

  interface LineageNode {
    id: string;
    type: string;        // file, table, api, processing
    name: string;
    sub_type?: string;   // transform, join, code, quality_check, sql_generate
    pipeline_id?: string;
    pipeline?: string;
  }
  interface LineageEdge {
    from: string;
    to: string;
    pipeline_id: string;
    pipeline: string;
  }
  interface Pos { x: number; y: number; }

  let nodes: LineageNode[] = [];
  let edges: LineageEdge[] = [];
  let loading = true;
  let selectedNodeId: string | null = null;
  let hoveredEdge: { from: string; to: string } | null = null;

  // SVG / viewport
  let svgEl: SVGSVGElement;
  let containerEl: HTMLDivElement;
  let viewBox = { x: 0, y: 0, w: 1200, h: 700 };

  // Node positions (draggable)
  let positions = new Map<string, Pos>();

  // Node dimensions
  const NW = 200;
  const NH = 52;
  const COL_GAP = 300;
  const ROW_GAP = 80;

  onMount(async () => {
    try {
      const res = await fetch("/api/lineage", { headers: authHeaders() });
      const data = await res.json();
      nodes = data.nodes || [];
      edges = data.edges || [];
      layoutNodes();
    } catch {
      notify.error("Failed to load lineage");
    } finally {
      loading = false;
    }
  });

  // ── Layered DAG layout (topological) ──────────────────────────
  function layoutNodes() {
    if (nodes.length === 0) return;

    // Build adjacency
    const outgoing = new Map<string, string[]>();
    const incoming = new Map<string, string[]>();
    const allIds = new Set(nodes.map(n => n.id));

    for (const n of nodes) {
      outgoing.set(n.id, []);
      incoming.set(n.id, []);
    }
    for (const e of edges) {
      if (allIds.has(e.from) && allIds.has(e.to)) {
        outgoing.get(e.from)!.push(e.to);
        incoming.get(e.to)!.push(e.from);
      }
    }

    // Assign layers via longest-path from sources
    const layer = new Map<string, number>();
    const visited = new Set<string>();

    function assignLayer(id: string): number {
      if (layer.has(id)) return layer.get(id)!;
      if (visited.has(id)) return 0; // cycle protection
      visited.add(id);
      const parents = incoming.get(id) || [];
      const myLayer = parents.length === 0 ? 0 : Math.max(...parents.map(assignLayer)) + 1;
      layer.set(id, myLayer);
      return myLayer;
    }

    for (const n of nodes) assignLayer(n.id);

    // Group by layer
    const layers = new Map<number, string[]>();
    for (const [id, l] of layer) {
      if (!layers.has(l)) layers.set(l, []);
      layers.get(l)!.push(id);
    }

    // Position nodes
    const maxLayer = Math.max(...layers.keys(), 0);
    const newPositions = new Map<string, Pos>();

    for (let col = 0; col <= maxLayer; col++) {
      const ids = layers.get(col) || [];
      const totalH = ids.length * NH + (ids.length - 1) * ROW_GAP;
      const startY = Math.max(40, (Math.max(500, nodes.length * 60) - totalH) / 2);

      for (let row = 0; row < ids.length; row++) {
        newPositions.set(ids[row], {
          x: 60 + col * COL_GAP,
          y: startY + row * (NH + ROW_GAP),
        });
      }
    }

    positions = newPositions;

    // Fit viewBox to content
    let minX = Infinity, minY = Infinity, maxX = 0, maxY = 0;
    for (const p of positions.values()) {
      minX = Math.min(minX, p.x);
      minY = Math.min(minY, p.y);
      maxX = Math.max(maxX, p.x + NW);
      maxY = Math.max(maxY, p.y + NH);
    }
    viewBox = {
      x: minX - 40,
      y: minY - 40,
      w: Math.max(800, maxX - minX + NW + 80),
      h: Math.max(500, maxY - minY + NH + 120),
    };
  }

  // ── Coordinate helpers ────────────────────────────────────────
  function clientToSvg(cx: number, cy: number): Pos {
    if (!svgEl) return { x: cx, y: cy };
    const pt = svgEl.createSVGPoint();
    pt.x = cx; pt.y = cy;
    const ctm = svgEl.getScreenCTM();
    if (!ctm) return { x: cx, y: cy };
    const s = pt.matrixTransform(ctm.inverse());
    return { x: s.x, y: s.y };
  }

  // ── Pan ───────────────────────────────────────────────────────
  let panning = false;
  function onPanStart(e: MouseEvent) {
    if (e.button !== 0 && e.button !== 1) return;
    // Only pan if clicking on canvas bg (not a node)
    const target = e.target as Element;
    if (!target.classList.contains("lineage-bg") && target.tagName !== "svg") return;
    panning = true;
    const sx = e.clientX + viewBox.x;
    const sy = e.clientY + viewBox.y;
    const onMove = (ev: MouseEvent) => {
      viewBox.x = sx - ev.clientX;
      viewBox.y = sy - ev.clientY;
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

  // ── Zoom ──────────────────────────────────────────────────────
  function onWheel(e: WheelEvent) {
    e.preventDefault();
    const zoomFactor = e.deltaY > 0 ? 1.08 : 1 / 1.08;
    const pt = clientToSvg(e.clientX, e.clientY);
    viewBox.x = pt.x - (pt.x - viewBox.x) * zoomFactor;
    viewBox.y = pt.y - (pt.y - viewBox.y) * zoomFactor;
    viewBox.w *= zoomFactor;
    viewBox.h *= zoomFactor;
    viewBox = viewBox;
  }

  // ── Node drag ─────────────────────────────────────────────────
  function onNodeDrag(e: MouseEvent, nodeId: string) {
    e.stopPropagation();
    e.preventDefault();
    const p = positions.get(nodeId);
    if (!p) return;

    const startSvg = clientToSvg(e.clientX, e.clientY);
    const offX = p.x - startSvg.x;
    const offY = p.y - startSvg.y;

    const onMove = (ev: MouseEvent) => {
      const cur = clientToSvg(ev.clientX, ev.clientY);
      positions.set(nodeId, { x: cur.x + offX, y: cur.y + offY });
      positions = new Map(positions); // trigger reactivity
    };
    const onUp = () => {
      window.removeEventListener("mousemove", onMove);
      window.removeEventListener("mouseup", onUp);
    };
    window.addEventListener("mousemove", onMove);
    window.addEventListener("mouseup", onUp);
  }

  // ── Reactive edge geometry (recomputed whenever positions changes) ──
  interface EdgeGeo {
    from: string;
    to: string;
    pipeline: string;
    pipeline_id: string;
    path: string;
    mid: Pos;
  }

  $: edgeGeos = (() => {
    // Reference positions to trigger reactivity
    const _p = positions;
    const result: EdgeGeo[] = [];
    for (const e of edges) {
      const fromPos = _p.get(e.from);
      const toPos = _p.get(e.to);
      if (!fromPos || !toPos) continue;
      const x1 = fromPos.x + NW;
      const y1 = fromPos.y + NH / 2;
      const x2 = toPos.x;
      const y2 = toPos.y + NH / 2;
      const dx = Math.abs(x2 - x1) * 0.5;
      result.push({
        from: e.from,
        to: e.to,
        pipeline: e.pipeline,
        pipeline_id: e.pipeline_id,
        path: `M ${x1} ${y1} C ${x1 + dx} ${y1}, ${x2 - dx} ${y2}, ${x2} ${y2}`,
        mid: {
          x: (x1 + x2) / 2,
          y: (y1 + y2) / 2,
        },
      });
    }
    return result;
  })();

  // ── Node helpers ──────────────────────────────────────────────
  function nodeColor(n: LineageNode): string {
    if (n.type === "processing") {
      switch (n.sub_type) {
        case "transform": return "var(--node-transform)";
        case "code": return "var(--node-code)";
        case "join": return "var(--node-join)";
        case "quality_check": return "var(--node-quality)";
        case "sql_generate": return "var(--node-sql)";
        default: return "var(--text-muted)";
      }
    }
    switch (n.type) {
      case "file": return "var(--node-source-file)";
      case "table": return "var(--node-sql)";
      case "api": return "var(--node-source-api)";
      default: return "var(--text-muted)";
    }
  }

  function typeLabel(n: LineageNode): string {
    if (n.type === "processing") {
      const labels: Record<string, string> = {
        transform: "TRANSFORM",
        code: "PYTHON",
        join: "JOIN",
        quality_check: "QUALITY",
        sql_generate: "SQL GEN",
      };
      return labels[n.sub_type || ""] || "PROCESS";
    }
    switch (n.type) {
      case "file": return "FILE";
      case "table": return "TABLE";
      case "api": return "API";
      default: return n.type.toUpperCase();
    }
  }

  function typeBadgeLetter(n: LineageNode): string {
    if (n.type === "processing") {
      const letters: Record<string, string> = {
        transform: "T",
        code: "Py",
        join: "J",
        quality_check: "Q",
        sql_generate: "S",
      };
      return letters[n.sub_type || ""] || "P";
    }
    switch (n.type) {
      case "file": return "F";
      case "table": return "T";
      case "api": return "A";
      default: return "?";
    }
  }

  function truncate(s: string, max: number): string {
    return s.length > max ? s.slice(0, max - 1) + "…" : s;
  }

  function connectedPipelines(nodeId: string): { name: string; id: string }[] {
    const seen = new Map<string, string>();
    for (const e of edges) {
      if (e.from === nodeId || e.to === nodeId) {
        seen.set(e.pipeline_id, e.pipeline);
      }
    }
    return [...seen].map(([id, name]) => ({ id, name }));
  }

  function upstreamNodes(nodeId: string): string[] {
    return edges.filter(e => e.to === nodeId).map(e => e.from);
  }
  function downstreamNodes(nodeId: string): string[] {
    return edges.filter(e => e.from === nodeId).map(e => e.to);
  }

  function fitToView() {
    layoutNodes();
  }

  // Theme-reactive colors for SVG
  $: isDark = $theme === "dark";
  $: gridDot = isDark ? "rgba(39,39,42,0.5)" : "rgba(208,213,221,0.6)";
  $: gridDotLg = isDark ? "rgba(63,63,70,0.3)" : "rgba(152,162,179,0.35)";
  $: edgeStroke = isDark ? "#3f3f46" : "#98a2b3";
  $: edgeHover = isDark ? "#6366f1" : "#0d9488";

  $: selectedNode = nodes.find(n => n.id === selectedNodeId) || null;
</script>

<div class="lineage-page animate-in">
  <header class="page-header">
    <div class="header-left">
      <h1>Data Lineage</h1>
      <span class="meta">{nodes.filter(n => n.type !== 'processing').length} assets, {nodes.filter(n => n.type === 'processing').length} steps, {edges.length} connections</span>
    </div>
    <div class="header-right">
      <button class="btn-sm" on:click={fitToView} title="Reset layout">
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none">
          <path d="M3 3h7v7H3zM14 3h7v7h-7zM3 14h7v7H3zM14 14h7v7h-7z" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
        </svg>
        Reset Layout
      </button>
    </div>
  </header>

  {#if loading}
    <div style="display:flex;flex-direction:column;gap:8px">
      <Skeleton height="400px" />
    </div>
  {:else if nodes.length === 0}
    <div class="empty-state">
      <p>No data lineage detected.</p>
      <p class="hint">Create pipelines with source and sink nodes to see the data flow.</p>
    </div>
  {:else}
    <div class="lineage-container" bind:this={containerEl}>
      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <svg
        class="lineage-graph"
        bind:this={svgEl}
        viewBox="{viewBox.x} {viewBox.y} {viewBox.w} {viewBox.h}"
        on:mousedown={onPanStart}
        on:wheel={onWheel}
        on:keydown={() => {}}
      >
        <defs>
          <pattern id="lin-grid-sm" width="20" height="20" patternUnits="userSpaceOnUse">
            <circle cx="10" cy="10" r="0.5" fill={gridDot} />
          </pattern>
          <pattern id="lin-grid-lg" width="100" height="100" patternUnits="userSpaceOnUse">
            <rect width="100" height="100" fill="url(#lin-grid-sm)" />
            <circle cx="0" cy="0" r="0.8" fill={gridDotLg} />
          </pattern>
          <marker id="lin-arrow" markerWidth="8" markerHeight="6" refX="7" refY="3" orient="auto">
            <polygon points="0 0.5, 7 3, 0 5.5" fill={edgeStroke} />
          </marker>
          <marker id="lin-arrow-hl" markerWidth="8" markerHeight="6" refX="7" refY="3" orient="auto">
            <polygon points="0 0.5, 7 3, 0 5.5" fill={edgeHover} />
          </marker>
        </defs>

        <!-- Background -->
        <rect
          class="lineage-bg"
          x={viewBox.x - 1000}
          y={viewBox.y - 1000}
          width={viewBox.w + 2000}
          height={viewBox.h + 2000}
          fill="url(#lin-grid-lg)"
        />

        <!-- Edges -->
        {#each edgeGeos as eg}
          {@const isHl = hoveredEdge?.from === eg.from && hoveredEdge?.to === eg.to}
          {@const isNodeHl = selectedNodeId === eg.from || selectedNodeId === eg.to}
          <!-- Hit area -->
          <!-- svelte-ignore a11y_no_static_element_interactions -->
          <path
            d={eg.path}
            fill="none"
            stroke="transparent"
            stroke-width="16"
            class="edge-hit"
            on:mouseenter={() => hoveredEdge = { from: eg.from, to: eg.to }}
            on:mouseleave={() => hoveredEdge = null}
            on:keydown={() => {}}
          />
          <!-- Visible edge -->
          <path
            d={eg.path}
            fill="none"
            stroke={isHl || isNodeHl ? edgeHover : edgeStroke}
            stroke-width={isHl || isNodeHl ? 2 : 1.5}
            opacity={isHl || isNodeHl ? 1 : 0.5}
            marker-end={isHl || isNodeHl ? "url(#lin-arrow-hl)" : "url(#lin-arrow)"}
            class="edge-line"
          />
          <!-- Pipeline label — only show on hover -->
          {#if isHl}
            <g transform="translate({eg.mid.x}, {eg.mid.y - 8})">
              <rect
                x={-eg.pipeline.length * 3.2 - 6}
                y="-9"
                width={eg.pipeline.length * 6.4 + 12}
                height="18"
                rx="4"
                class="edge-label-bg"
              />
              <text
                text-anchor="middle"
                class="edge-label"
                dominant-baseline="middle"
              >
                {eg.pipeline}
              </text>
            </g>
          {/if}
        {/each}

        <!-- Nodes -->
        {#each nodes as node (node.id)}
          {@const p = positions.get(node.id)}
          {#if p}
            {@const isSelected = selectedNodeId === node.id}
            {@const isConnected = selectedNodeId && (upstreamNodes(selectedNodeId).includes(node.id) || downstreamNodes(selectedNodeId).includes(node.id))}
            <!-- svelte-ignore a11y_no_static_element_interactions -->
            <g
              class="lin-node"
              class:selected={isSelected}
              class:connected={isConnected}
              class:processing={node.type === 'processing'}
              transform="translate({p.x}, {p.y})"
              on:mousedown={(e) => {
                if (e.button === 0) {
                  selectedNodeId = selectedNodeId === node.id ? null : node.id;
                  onNodeDrag(e, node.id);
                }
              }}
              on:keydown={() => {}}
            >
              <!-- Card bg -->
              <rect
                width={NW} height={NH} rx="8"
                class="lin-node-bg"
              />

              <!-- Left color bar -->
              <clipPath id="lin-clip-{node.id}">
                <rect width="4" height={NH} rx="8" />
              </clipPath>
              <rect
                width="4" height={NH}
                fill={nodeColor(node)}
                clip-path="url(#lin-clip-{node.id})"
              />

              <!-- Type badge -->
              <rect
                x="14" y={(NH - 22) / 2}
                width="22" height="22"
                rx="5"
                fill={nodeColor(node)}
                opacity="0.1"
              />
              <text
                x="25"
                y={NH / 2 + 1}
                text-anchor="middle"
                dominant-baseline="middle"
                class="lin-type-badge"
                fill={nodeColor(node)}
              >
                {typeBadgeLetter(node)}
              </text>

              <!-- Name -->
              <text
                x="44" y={NH / 2 - 5}
                class="lin-node-name"
                dominant-baseline="auto"
              >
                {truncate(node.name, 18)}
              </text>

              <!-- Type label -->
              <text
                x="44" y={NH / 2 + 9}
                class="lin-node-type"
              >
                {typeLabel(node)}
              </text>
            </g>
          {/if}
        {/each}
      </svg>

      <!-- Detail panel (slides in on selection) -->
      {#if selectedNode}
        <div class="detail-panel">
          <div class="detail-header">
            <div class="detail-type-badge" style="background: {nodeColor(selectedNode)}; opacity: 0.15;">
            </div>
            <div class="detail-title-wrap">
              <h3 class="detail-title">{selectedNode.name}</h3>
              <span class="detail-type" style="color: {nodeColor(selectedNode)}">{typeLabel(selectedNode)}</span>
            </div>
            <button class="detail-close" on:click={() => selectedNodeId = null}>
              <svg width="16" height="16" viewBox="0 0 24 24" fill="none">
                <path d="M18 6L6 18M6 6l12 12" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" />
              </svg>
            </button>
          </div>

          <div class="detail-section">
            <span class="detail-label">{selectedNode.type === 'processing' ? 'Node ID' : 'Asset ID'}</span>
            <code class="detail-code">{selectedNode.id}</code>
          </div>

          {#if selectedNode.type === 'processing' && selectedNode.pipeline}
            <div class="detail-section">
              <span class="detail-label">Pipeline</span>
              <a href="#/pipelines/{selectedNode.pipeline_id}" class="pipeline-tag">{selectedNode.pipeline}</a>
            </div>
          {/if}

          <div class="detail-section">
            <span class="detail-label">Used by pipelines</span>
            <div class="pipeline-tags">
              {#each connectedPipelines(selectedNode.id) as p}
                <a href="#/pipelines/{p.id}" class="pipeline-tag">{p.name}</a>
              {/each}
            </div>
          </div>

          {#if upstreamNodes(selectedNode.id).length > 0}
            <div class="detail-section">
              <span class="detail-label">Upstream ({upstreamNodes(selectedNode.id).length})</span>
              <div class="dep-list">
                {#each upstreamNodes(selectedNode.id) as uid}
                  {@const un = nodes.find(n => n.id === uid)}
                  {#if un}
                    <!-- svelte-ignore a11y_no_static_element_interactions -->
                    <div class="dep-item" on:click={() => selectedNodeId = uid} on:keydown={() => {}}>
                      <span class="dep-dot" style="background: {nodeColor(un)}"></span>
                      <span class="dep-name">{truncate(un.name, 24)}</span>
                    </div>
                  {/if}
                {/each}
              </div>
            </div>
          {/if}

          {#if downstreamNodes(selectedNode.id).length > 0}
            <div class="detail-section">
              <span class="detail-label">Downstream ({downstreamNodes(selectedNode.id).length})</span>
              <div class="dep-list">
                {#each downstreamNodes(selectedNode.id) as did}
                  {@const dn = nodes.find(n => n.id === did)}
                  {#if dn}
                    <!-- svelte-ignore a11y_no_static_element_interactions -->
                    <div class="dep-item" on:click={() => selectedNodeId = did} on:keydown={() => {}}>
                      <span class="dep-dot" style="background: {nodeColor(dn)}"></span>
                      <span class="dep-name">{truncate(dn.name, 24)}</span>
                    </div>
                  {/if}
                {/each}
              </div>
            </div>
          {/if}
        </div>
      {/if}
    </div>
  {/if}
</div>

<style>
  .lineage-page {
    display: flex;
    flex-direction: column;
    height: 100%;
  }

  .page-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: var(--space-md);
    flex-shrink: 0;
  }
  .header-left { display: flex; align-items: baseline; gap: 12px; }
  .page-header h1 { font-size: 1.5rem; font-weight: 600; letter-spacing: -0.02em; }
  .meta { font-size: 0.8125rem; color: var(--text-muted); font-family: var(--font-mono); }
  .header-right { display: flex; gap: 8px; }
  .btn-sm {
    display: flex; align-items: center; gap: 6px;
    padding: 6px 12px; border-radius: 6px; font-size: 12px; font-weight: 500;
    background: var(--bg-secondary); border: 1px solid var(--border);
    color: var(--text-secondary); cursor: pointer; transition: all 150ms ease;
  }
  .btn-sm:hover { background: var(--bg-tertiary); color: var(--text-primary); }

  .empty-state {
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    padding: var(--space-xl);
    text-align: center;
    color: var(--text-secondary);
  }
  .hint { color: var(--text-muted); font-size: 0.875rem; margin-top: var(--space-xs); }

  .lineage-container {
    flex: 1;
    display: flex;
    gap: 0;
    position: relative;
    min-height: 0;
  }

  .lineage-graph {
    flex: 1;
    background: var(--bg-canvas);
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius-lg);
    cursor: grab;
    user-select: none;
  }
  .lineage-graph:active { cursor: grabbing; }

  .lineage-bg { cursor: grab; }

  /* Edges */
  .edge-hit { cursor: pointer; }
  .edge-line { transition: stroke 150ms ease, opacity 150ms ease; pointer-events: none; }

  .edge-label-bg {
    fill: var(--bg-secondary);
    stroke: var(--border);
    stroke-width: 1;
  }
  .edge-label {
    fill: var(--text-secondary);
    font-family: var(--font-mono);
    font-size: 9px;
    font-weight: 500;
  }

  /* Nodes */
  .lin-node { cursor: grab; }
  .lin-node:active { cursor: grabbing; }

  .lin-node-bg {
    fill: var(--bg-secondary);
    stroke: var(--border);
    stroke-width: 1;
    transition: stroke 150ms ease, fill 150ms ease;
  }
  .lin-node:hover .lin-node-bg {
    stroke: var(--border-hover);
    fill: var(--bg-card-hover);
  }
  .lin-node.selected .lin-node-bg {
    stroke: var(--accent);
    stroke-width: 1.5;
  }
  .lin-node.connected .lin-node-bg {
    stroke: var(--accent);
    stroke-width: 1;
    opacity: 0.8;
  }
  .lin-node.processing .lin-node-bg {
    stroke-dasharray: 4 2;
    opacity: 0.9;
  }

  .lin-type-badge {
    font-family: var(--font-mono);
    font-size: 10px;
    font-weight: 700;
  }
  .lin-node-name {
    fill: var(--text-primary);
    font-family: 'Inter', system-ui, sans-serif;
    font-size: 11.5px;
    font-weight: 600;
    letter-spacing: -0.01em;
  }
  .lin-node-type {
    fill: var(--text-muted);
    font-family: var(--font-mono);
    font-size: 8.5px;
    letter-spacing: 0.06em;
    text-transform: uppercase;
  }

  /* Detail panel */
  .detail-panel {
    position: absolute;
    right: 8px;
    top: 8px;
    bottom: 8px;
    width: 280px;
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    padding: 0;
    overflow-y: auto;
    box-shadow: -4px 0 20px rgba(0,0,0,0.1);
    animation: slide-in 200ms ease-out;
  }

  @keyframes slide-in {
    from { transform: translateX(20px); opacity: 0; }
    to { transform: translateX(0); opacity: 1; }
  }

  .detail-header {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    padding: 16px;
    border-bottom: 1px solid var(--border-subtle);
  }
  .detail-type-badge {
    width: 8px; height: 8px;
    border-radius: 50%;
    margin-top: 6px;
    flex-shrink: 0;
  }
  .detail-title-wrap {
    flex: 1; min-width: 0;
  }
  .detail-title {
    font-size: 14px;
    font-weight: 600;
    color: var(--text-primary);
    word-break: break-all;
    line-height: 1.3;
    margin: 0;
  }
  .detail-type {
    font-family: var(--font-mono);
    font-size: 10px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }
  .detail-close {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 28px; height: 28px;
    border-radius: 6px;
    color: var(--text-muted);
    flex-shrink: 0;
    transition: all 150ms ease;
  }
  .detail-close:hover {
    color: var(--text-primary);
    background: var(--bg-tertiary);
  }

  .detail-section {
    padding: 12px 16px;
    border-bottom: 1px solid var(--border-subtle);
  }
  .detail-label {
    font-size: 10px;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.08em;
    font-weight: 600;
    display: block;
    margin-bottom: 6px;
  }
  .detail-code {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--text-secondary);
    word-break: break-all;
  }

  .pipeline-tags {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
  }
  .pipeline-tag {
    font-size: 11px;
    padding: 3px 10px;
    border-radius: var(--radius-sm);
    background: var(--accent-glow);
    color: var(--accent-text);
    font-weight: 500;
    text-decoration: none;
    transition: background 150ms ease;
  }
  .pipeline-tag:hover {
    background: var(--accent-glow-strong);
  }

  .dep-list {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .dep-item {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 5px 8px;
    border-radius: 4px;
    cursor: pointer;
    transition: background 150ms ease;
  }
  .dep-item:hover {
    background: var(--bg-tertiary);
  }
  .dep-dot {
    width: 6px; height: 6px;
    border-radius: 50%;
    flex-shrink: 0;
  }
  .dep-name {
    font-size: 12px;
    color: var(--text-secondary);
  }
  .dep-item:hover .dep-name {
    color: var(--text-primary);
  }
</style>
