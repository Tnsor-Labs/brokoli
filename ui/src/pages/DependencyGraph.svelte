<script lang="ts">
  import { onMount } from "svelte";
  import { api } from "../lib/api";
  import { notify } from "../lib/toast";
  import type { DependencyGraph, DependencyMode, DependencyState } from "../lib/types";
  import Skeleton from "../components/Skeleton.svelte";

  interface Pos {
    x: number;
    y: number;
  }

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
    graph.nodes.forEach((n) => {
      incoming.set(n.id, []);
      outgoing.set(n.id, []);
    });
    graph.edges.forEach((e) => {
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
      const lv = 1 + Math.max(...parents.map((p) => assign(p, visiting)));
      level.set(id, lv);
      return lv;
    }
    graph.nodes.forEach((n) => assign(n.id));

    // Group nodes by level, assign y positions within each column
    const byLevel = new Map<number, string[]>();
    for (const [id, lv] of level) {
      if (!byLevel.has(lv)) byLevel.set(lv, []);
      byLevel.get(lv)!.push(id);
    }
    const sortedLevels = Array.from(byLevel.keys()).sort((a, b) => a - b);

    positions = new Map();
    let maxY = 0;
    sortedLevels.forEach((lv) => {
      const ids = byLevel.get(lv)!;
      // Sort by name within level for determinism
      ids.sort((a, b) => {
        const na = graph.nodes.find((n) => n.id === a)?.name || "";
        const nb = graph.nodes.find((n) => n.id === b)?.name || "";
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
      x: 0,
      y: 0,
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
    return mode === "trigger" ? "var(--node-source-api)" : "var(--running)";
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
            .filter((e) => e.from === selectedNode || e.to === selectedNode)
            .flatMap((e) => [e.from, e.to]),
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

<div class="page animate-in">
  <header class="page-header">
    <div>
      <p class="eyebrow">Orchestration</p>
      <h1>Pipeline Dependencies</h1>
      <p class="subtitle">Understand gate and trigger relationships across your workspace.</p>
    </div>
    <div class="header-actions">
      <button class="btn" on:click={reset}>Reset view</button>
    </div>
  </header>

  {#if loading}
    <div class="state-card loading-state" aria-label="Loading pipeline dependencies">
      <div class="state-heading">
        <span class="state-icon">⌘</span>
        <div>
          <strong>Mapping dependencies</strong><small>Resolving pipeline relationships...</small>
        </div>
      </div>
      <Skeleton height="420px" />
    </div>
  {:else if graph.nodes.length === 0}
    <div class="state-card empty">
      <span class="empty-icon">⌘</span>
      <h2>No dependencies to map</h2>
      <p>Create some pipelines and wire dependencies to see the graph.</p>
      <a href="#/pipelines">View pipelines</a>
    </div>
  {:else}
    <section class="workspace" aria-label="Pipeline dependency graph">
      <div class="workspace-toolbar">
        <div>
          <span class="workspace-title">Dependency map</span><span class="workspace-count"
            >{graph.nodes.length} pipelines · {graph.edges.length} dependencies</span
          >
        </div>
        <div class="legend" aria-label="Dependency types">
          <span class="legend-item"><span class="line gate"></span>Gate</span>
          <span class="legend-item"><span class="line trigger"></span>Trigger</span>
        </div>
      </div>
      <div class="canvas">
        <svg
          bind:this={svgEl}
          viewBox="{viewBox.x} {viewBox.y} {viewBox.w} {viewBox.h}"
          preserveAspectRatio="xMidYMid meet"
        >
          <defs>
            <marker
              id="arrow-gate"
              viewBox="0 0 10 10"
              refX="9"
              refY="5"
              markerWidth="6"
              markerHeight="6"
              orient="auto"
            >
              <path d="M 0 0 L 10 5 L 0 10 z" fill="var(--running)" />
            </marker>
            <marker
              id="arrow-trigger"
              viewBox="0 0 10 10"
              refX="9"
              refY="5"
              markerWidth="6"
              markerHeight="6"
              orient="auto"
            >
              <path d="M 0 0 L 10 5 L 0 10 z" fill="var(--node-source-api)" />
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
                <rect width={NW} height={NH} rx="8" class="node-rect" />
                <text x="14" y="22" class="node-name">{node.name}</text>
                <text x="14" y="40" class="node-id">{node.id.slice(0, 8)}</text>
              </g>
            {/if}
          {/each}
        </svg>
      </div>

      {#if selectedNode}
        {@const sel = graph.nodes.find((n) => n.id === selectedNode)}
        {#if sel}
          <div class="detail">
            <div class="detail-row">
              <div>
                <span class="detail-label">Selected pipeline</span><strong>{sel.name}</strong>
              </div>
              <button
                class="detail-close"
                aria-label="Close details"
                on:click={() => (selectedNode = null)}>×</button
              >
            </div>
            <div class="detail-meta">
              <a href={`#/pipelines/${sel.id}/edit`} class="detail-link">Edit pipeline</a>
              <a href={`#/pipelines/${sel.id}`} class="detail-link primary">View runs →</a>
            </div>
          </div>
        {/if}
      {/if}
    </section>
  {/if}
</div>

<style>
  .page {
    display: flex;
    height: 100%;
    min-height: 0;
    flex-direction: column;
    max-width: 100%;
  }
  .page-header {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
    gap: var(--space-lg);
    margin-bottom: 18px;
  }
  .eyebrow {
    margin-bottom: 5px;
    color: var(--accent);
    font: 650 9px var(--font-mono);
    letter-spacing: 0.14em;
    text-transform: uppercase;
  }
  h1 {
    font-size: 24px;
    font-weight: 650;
    letter-spacing: -0.035em;
  }
  .subtitle {
    margin-top: 4px;
    font-size: 12px;
    color: var(--text-muted);
  }
  .header-actions {
    display: flex;
    align-items: center;
  }
  .legend {
    display: flex;
    gap: 14px;
    font-size: 12px;
    color: var(--text-secondary);
  }
  .legend-item {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .line {
    display: inline-block;
    width: 22px;
    height: 2px;
  }
  .line.gate {
    background: var(--running);
  }
  .line.trigger {
    background: var(--node-source-api);
    border-top: 2px dashed var(--node-source-api);
    height: 0;
  }
  .btn {
    min-height: 34px;
    padding: 0 12px;
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    color: var(--text-secondary);
    font-size: 12px;
    cursor: pointer;
  }
  .btn:hover {
    border-color: var(--border-hover);
    background: var(--bg-tertiary);
    color: var(--text-primary);
  }
  .state-card {
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius-lg);
    background: var(--bg-secondary);
    box-shadow: var(--shadow-card);
  }
  .loading-state {
    display: flex;
    flex-direction: column;
    gap: var(--space-md);
    padding: var(--space-md);
  }
  .state-heading {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 2px;
  }
  .state-heading div {
    display: flex;
    flex-direction: column;
  }
  .state-heading strong {
    font-size: 12px;
    font-weight: 600;
  }
  .state-heading small {
    color: var(--text-muted);
    font-size: 10px;
  }
  .state-icon,
  .empty-icon {
    color: var(--accent);
    font: 18px var(--font-mono);
  }
  .empty {
    text-align: center;
    padding: 80px 20px;
    color: var(--text-muted);
  }
  .empty h2 {
    margin: 8px 0 4px;
    font-size: 16px;
    font-weight: 620;
    color: var(--text-primary);
  }
  .empty a {
    display: inline-block;
    margin-top: var(--space-md);
    font-size: 12px;
    font-weight: 600;
  }
  .workspace {
    position: relative;
    display: flex;
    min-height: 0;
    flex: 1;
    flex-direction: column;
    overflow: hidden;
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius-lg);
    background: var(--bg-secondary);
    box-shadow: var(--shadow-card);
  }
  .workspace-toolbar {
    display: flex;
    min-height: 42px;
    flex: none;
    align-items: center;
    justify-content: space-between;
    gap: 16px;
    padding: 0 14px;
    border-bottom: 1px solid var(--border-subtle);
  }
  .workspace-toolbar > div {
    display: flex;
    align-items: center;
    gap: 12px;
  }
  .workspace-title {
    color: var(--text-secondary);
    font-size: 11px;
    font-weight: 620;
  }
  .workspace-count {
    color: var(--text-dim);
    font: 9px var(--font-mono);
  }
  .canvas {
    flex: 1;
    min-height: 0;
    background: var(--bg-canvas);
    overflow: auto;
  }
  svg {
    display: block;
    min-width: 100%;
    min-height: 100%;
  }
  .node {
    cursor: pointer;
    transition: opacity 150ms ease;
  }
  .node.dimmed {
    opacity: 0.25;
  }
  .node-rect {
    fill: var(--bg-tertiary);
    stroke: var(--border);
    stroke-width: 1;
    transition: all 150ms ease;
  }
  .node:hover .node-rect {
    stroke: var(--accent);
    stroke-width: 2;
  }
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
    position: absolute;
    right: 12px;
    bottom: 12px;
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    padding: 14px 18px;
    min-width: 260px;
    box-shadow: var(--shadow-lg);
  }
  .detail-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    font-size: 14px;
  }
  .detail-row > div {
    display: flex;
    min-width: 0;
    flex-direction: column;
  }
  .detail-row strong {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .detail-label {
    margin-bottom: 2px;
    color: var(--text-muted);
    font-size: 8px;
    font-weight: 650;
    letter-spacing: 0.08em;
    text-transform: uppercase;
  }
  .detail-close {
    background: transparent;
    border: none;
    color: var(--text-muted);
    font-size: 18px;
    cursor: pointer;
  }
  .detail-meta {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    margin-top: 12px;
    padding-top: 10px;
    border-top: 1px solid var(--border-subtle);
  }
  .detail-link {
    font-size: 12px;
    padding: 5px 9px;
    border-radius: var(--radius-md);
    color: var(--text-secondary);
    background: var(--bg-tertiary);
    text-decoration: none;
  }
  .detail-link.primary {
    color: var(--accent-text);
    background: var(--accent-glow);
  }
  .detail-link:hover {
    color: var(--text-primary);
    background: var(--border);
  }

  @media (max-width: 768px) {
    .page {
      height: auto;
      min-height: calc(100dvh - 102px);
    }
    .page-header {
      align-items: stretch;
      flex-direction: column;
      gap: 12px;
    }
    .btn {
      width: 100%;
    }
    .workspace {
      min-height: 580px;
      flex: none;
    }
    .workspace-toolbar {
      align-items: flex-start;
      flex-direction: column;
      gap: 7px;
      padding: 9px 12px;
    }
    .workspace-toolbar > div {
      flex-wrap: wrap;
      gap: 7px;
    }
    .legend {
      font-size: 10px;
    }
    .detail {
      right: 8px;
      bottom: 8px;
      left: 8px;
      min-width: 0;
    }
  }
</style>
