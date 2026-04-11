<script lang="ts">
  import { onMount } from "svelte";
  import { api } from "../lib/api";
  import { notify } from "../lib/toast";
  import type { DependencyGraph, DependencyMode, DependencyState } from "../lib/types";
  import Skeleton from "../components/Skeleton.svelte";

  interface Pos { x: number; y: number; }

  let graph: DependencyGraph = { nodes: [], edges: [] };
  let loading = true;
  let positions = new Map<string, Pos>();
  let svgEl: SVGSVGElement;
  let viewBox = { x: 0, y: 0, w: 1400, h: 800 };
  let hoveredNode: string | null = null;
  let selectedNode: string | null = null;

  const NW = 220;
  const NH = 54;
  const H_GAP = 120;
  const V_GAP = 28;

  onMount(async () => {
    try {
      graph = await api.pipelines.dependencyGraph();
    } catch (e: any) {
      notify.error("Failed to load dependency graph: " + (e.message || e));
      graph = { nodes: [], edges: [] };
    }
    layout();
    loading = false;
  });

  function layout() {
    if (graph.nodes.length === 0) return;

    // Build adjacency for topological levels (longest path from root)
    const incoming = new Map<string, string[]>();
    const outgoing = new Map<string, string[]>();
    graph.nodes.forEach(n => {
      incoming.set(n.id, []);
      outgoing.set(n.id, []);
    });
    graph.edges.forEach(e => {
      if (incoming.has(e.to)) incoming.get(e.to)!.push(e.from);
      if (outgoing.has(e.from)) outgoing.get(e.from)!.push(e.to);
    });

    // Assign levels via DFS (longest-path layering)
    const level = new Map<string, number>();
    function assign(id: string, visiting = new Set<string>()): number {
      if (level.has(id)) return level.get(id)!;
      if (visiting.has(id)) return 0; // cycle safety (backend should prevent)
      visiting.add(id);
      const parents = incoming.get(id) || [];
      if (parents.length === 0) {
        level.set(id, 0);
        return 0;
      }
      const lv = 1 + Math.max(...parents.map(p => assign(p, visiting)));
      level.set(id, lv);
      return lv;
    }
    graph.nodes.forEach(n => assign(n.id));

    // Group nodes by level, assign y positions within each column
    const byLevel = new Map<number, string[]>();
    for (const [id, lv] of level) {
      if (!byLevel.has(lv)) byLevel.set(lv, []);
      byLevel.get(lv)!.push(id);
    }
    const sortedLevels = Array.from(byLevel.keys()).sort((a, b) => a - b);

    positions = new Map();
    let maxY = 0;
    sortedLevels.forEach(lv => {
      const ids = byLevel.get(lv)!;
      // Sort by name within level for determinism
      ids.sort((a, b) => {
        const na = graph.nodes.find(n => n.id === a)?.name || "";
        const nb = graph.nodes.find(n => n.id === b)?.name || "";
        return na.localeCompare(nb);
      });
      ids.forEach((id, i) => {
        positions.set(id, {
          x: 40 + lv * (NW + H_GAP),
          y: 40 + i * (NH + V_GAP),
        });
        if (40 + i * (NH + V_GAP) + NH > maxY) maxY = 40 + i * (NH + V_GAP) + NH;
      });
    });

    const maxLevel = sortedLevels.length > 0 ? sortedLevels[sortedLevels.length - 1] : 0;
    viewBox = {
      x: 0, y: 0,
      w: Math.max(1200, 40 + (maxLevel + 1) * (NW + H_GAP)),
      h: Math.max(600, maxY + 80),
    };
  }

  function edgePath(from: Pos, to: Pos): string {
    const x1 = from.x + NW;
    const y1 = from.y + NH / 2;
    const x2 = to.x;
    const y2 = to.y + NH / 2;
    const cx = (x1 + x2) / 2;
    return `M ${x1} ${y1} C ${cx} ${y1}, ${cx} ${y2}, ${x2} ${y2}`;
  }

  function edgeColor(mode: DependencyMode): string {
    return mode === "trigger" ? "#a855f7" : "#3b82f6";
  }

  function stateLabel(state: DependencyState): string {
    if (state === "succeeded") return "✓";
    if (state === "completed") return "●";
    return "✗";
  }

  // Selection highlights: incoming + outgoing paths for selected node.
  $: highlightedEdges = new Set(
    selectedNode
      ? graph.edges
          .map((e, i) => ({ e, i }))
          .filter(({ e }) => e.from === selectedNode || e.to === selectedNode)
          .map(({ i }) => i)
      : [],
  );
  $: highlightedNodes = new Set(
    selectedNode
      ? [
          selectedNode,
          ...graph.edges
            .filter(e => e.from === selectedNode || e.to === selectedNode)
            .flatMap(e => [e.from, e.to]),
        ]
      : [],
  );

  function handleNodeClick(id: string) {
    selectedNode = selectedNode === id ? null : id;
  }

  function reset() {
    selectedNode = null;
    layout();
  }
</script>

<div class="page">
  <div class="header">
    <div>
      <h1>Pipeline Dependencies</h1>
      <p class="subtitle">
        {graph.nodes.length} pipelines · {graph.edges.length} dependencies
      </p>
    </div>
    <div class="header-actions">
      <div class="legend">
        <span class="legend-item"><span class="line gate"></span>Gate</span>
        <span class="legend-item"><span class="line trigger"></span>Trigger</span>
      </div>
      <button class="btn" on:click={reset}>Reset view</button>
    </div>
  </div>

  {#if loading}
    <Skeleton height="600px" />
  {:else if graph.nodes.length === 0}
    <div class="empty">
      <h2>No pipelines yet</h2>
      <p>Create some pipelines and wire dependencies to see the graph.</p>
    </div>
  {:else}
    <div class="canvas">
      <svg
        bind:this={svgEl}
        viewBox="{viewBox.x} {viewBox.y} {viewBox.w} {viewBox.h}"
        preserveAspectRatio="xMidYMid meet"
      >
        <defs>
          <marker id="arrow-gate" viewBox="0 0 10 10" refX="9" refY="5"
                  markerWidth="6" markerHeight="6" orient="auto">
            <path d="M 0 0 L 10 5 L 0 10 z" fill="#3b82f6" />
          </marker>
          <marker id="arrow-trigger" viewBox="0 0 10 10" refX="9" refY="5"
                  markerWidth="6" markerHeight="6" orient="auto">
            <path d="M 0 0 L 10 5 L 0 10 z" fill="#a855f7" />
          </marker>
        </defs>

        <!-- Edges -->
        {#each graph.edges as edge, i}
          {@const from = positions.get(edge.from)}
          {@const to = positions.get(edge.to)}
          {#if from && to}
            {@const active = selectedNode === null || highlightedEdges.has(i)}
            <path
              d={edgePath(from, to)}
              fill="none"
              stroke={edgeColor(edge.mode)}
              stroke-width={highlightedEdges.has(i) ? 2.5 : 1.5}
              stroke-dasharray={edge.mode === "trigger" ? "6 4" : "none"}
              opacity={active ? 1 : 0.15}
              marker-end="url(#arrow-{edge.mode})"
            />
            <!-- state glyph midpoint -->
            {#if active}
              <text
                x={(from.x + NW + to.x) / 2}
                y={(from.y + NH / 2 + to.y + NH / 2) / 2 - 6}
                text-anchor="middle"
                font-size="13"
                fill={edgeColor(edge.mode)}
                font-family="monospace"
              >
                {stateLabel(edge.state)}
              </text>
            {/if}
          {/if}
        {/each}

        <!-- Nodes -->
        {#each graph.nodes as node}
          {@const p = positions.get(node.id)}
          {#if p}
            {@const dimmed = selectedNode !== null && !highlightedNodes.has(node.id)}
            <!-- svelte-ignore a11y_click_events_have_key_events -->
            <!-- svelte-ignore a11y_no_static_element_interactions -->
            <g
              transform="translate({p.x} {p.y})"
              class="node"
              class:selected={selectedNode === node.id}
              class:dimmed
              on:click={() => handleNodeClick(node.id)}
              on:mouseenter={() => (hoveredNode = node.id)}
              on:mouseleave={() => (hoveredNode = null)}
            >
              <rect
                width={NW}
                height={NH}
                rx="8"
                class="node-rect"
              />
              <text x="14" y="22" class="node-name">{node.name}</text>
              <text x="14" y="40" class="node-id">{node.id.slice(0, 8)}</text>
            </g>
          {/if}
        {/each}
      </svg>
    </div>

    {#if selectedNode}
      {@const sel = graph.nodes.find(n => n.id === selectedNode)}
      {#if sel}
        <div class="detail">
          <div class="detail-row">
            <strong>{sel.name}</strong>
            <button class="detail-close" on:click={() => (selectedNode = null)}>×</button>
          </div>
          <div class="detail-meta">
            <a href={`#/pipelines/${sel.id}/edit`} class="detail-link">Edit pipeline →</a>
            <a href={`#/pipelines/${sel.id}`} class="detail-link">View runs →</a>
          </div>
        </div>
      {/if}
    {/if}
  {/if}
</div>

<style>
  .page {
    padding: var(--space-xl);
    max-width: 100%;
  }
  .header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    margin-bottom: 20px;
  }
  h1 {
    font-size: 1.5rem;
    font-weight: 600;
    letter-spacing: -0.02em;
    margin: 0 0 4px;
  }
  .subtitle { font-size: 13px; color: var(--text-muted); margin: 0; }
  .header-actions { display: flex; gap: 14px; align-items: center; }
  .legend { display: flex; gap: 14px; font-size: 12px; color: var(--text-secondary); }
  .legend-item { display: flex; align-items: center; gap: 6px; }
  .line { display: inline-block; width: 22px; height: 2px; }
  .line.gate { background: #3b82f6; }
  .line.trigger { background: #a855f7; border-top: 2px dashed #a855f7; height: 0; }
  .btn {
    padding: 6px 12px;
    background: var(--bg-tertiary);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    color: var(--text-secondary);
    font-size: 12px;
    cursor: pointer;
  }
  .btn:hover { background: var(--border); color: var(--text-primary); }
  .empty {
    text-align: center;
    padding: 80px 20px;
    color: var(--text-muted);
  }
  .empty h2 { font-size: 18px; margin: 0 0 8px; color: var(--text-secondary); }
  .canvas {
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    overflow: auto;
    height: calc(100vh - 200px);
    min-height: 500px;
  }
  svg { display: block; min-width: 100%; min-height: 100%; }
  .node { cursor: pointer; transition: opacity 150ms ease; }
  .node.dimmed { opacity: 0.25; }
  .node-rect {
    fill: var(--bg-tertiary);
    stroke: var(--border);
    stroke-width: 1;
    transition: all 150ms ease;
  }
  .node:hover .node-rect { stroke: var(--accent); stroke-width: 2; }
  .node.selected .node-rect {
    fill: var(--accent);
    fill-opacity: 0.1;
    stroke: var(--accent);
    stroke-width: 2;
  }
  .node-name {
    font-size: 13px;
    font-weight: 600;
    fill: var(--text-primary);
  }
  .node-id {
    font-size: 10px;
    fill: var(--text-muted);
    font-family: var(--font-mono);
  }
  .detail {
    position: fixed;
    right: 30px;
    bottom: 30px;
    background: var(--bg-secondary);
    border: 1px solid var(--accent);
    border-radius: var(--radius-lg);
    padding: 14px 18px;
    min-width: 260px;
    box-shadow: 0 8px 24px rgba(0, 0, 0, 0.3);
  }
  .detail-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 8px;
    font-size: 14px;
  }
  .detail-close {
    background: transparent;
    border: none;
    color: var(--text-muted);
    font-size: 18px;
    cursor: pointer;
  }
  .detail-meta { display: flex; gap: 14px; }
  .detail-link {
    font-size: 12px;
    color: var(--accent);
    text-decoration: none;
  }
  .detail-link:hover { text-decoration: underline; }
</style>
