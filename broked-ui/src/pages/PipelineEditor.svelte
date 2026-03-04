<script lang="ts">
  import { onMount } from "svelte";
  import { api } from "../lib/api";
  import { newNodeId, nodeTypeConfig, autoLayout } from "../lib/dag";
  import { icons } from "../lib/icons";
  import PipelineCanvas from "../components/PipelineCanvas.svelte";
  import NodePalette from "../components/NodePalette.svelte";
  import NodeConfigPanel from "../components/NodeConfigPanel.svelte";
  import type { Pipeline, Node, Edge, NodeType } from "../lib/types";
  import { notify } from "../lib/toast";
  import { authHeaders } from "../lib/auth";

  export let params: { id?: string } = {};

  let pipeline: Pipeline | null = null;
  let nodes: Node[] = [];
  let edges: Edge[] = [];
  let selectedNodeId: string | null = null;
  let loading = true;
  let saving = false;
  let showYaml = false;
  let yamlText = "";
  let error = "";
  let previewing = false;
  let previewResults: Record<string, { columns: string[]; rows: Record<string, unknown>[]; status: string; error?: string }> = {};
  let previewNodeId: string | null = null;

  // ── Node validation ─────────────────────────────────────────
  interface NodeIssue {
    node_id: string;
    node_name: string;
    errors: string[];
    warnings: string[];
  }
  let nodeIssues: Record<string, NodeIssue> = {};

  function nodeHasError(nodeId: string): boolean {
    return (nodeIssues[nodeId]?.errors?.length || 0) > 0;
  }
  function nodeHasWarning(nodeId: string): boolean {
    return !nodeHasError(nodeId) && (nodeIssues[nodeId]?.warnings?.length || 0) > 0;
  }

  async function validateNodes() {
    if (!pipeline?.id) return;
    try {
      // Save first so backend has latest nodes
      pipeline.nodes = nodes;
      pipeline.edges = edges;
      await api.pipelines.update(pipeline.id, pipeline);
      const res = await fetch(`/api/pipelines/${pipeline.id}/validate-nodes`, {
        method: "POST",
        headers: authHeaders(),
      });
      const data = await res.json();
      const map: Record<string, NodeIssue> = {};
      for (const issue of data.issues || []) {
        map[issue.node_id] = issue;
      }
      nodeIssues = map;
    } catch {
      // silent fail
    }
  }

  // ── Undo / Redo ─────────────────────────────────────────────
  interface EditorState {
    nodes: Node[];
    edges: Edge[];
  }

  let undoStack: EditorState[] = [];
  let redoStack: EditorState[] = [];
  const MAX_HISTORY = 50;

  function snapshot(): EditorState {
    return {
      nodes: JSON.parse(JSON.stringify(nodes)),
      edges: JSON.parse(JSON.stringify(edges)),
    };
  }

  function pushUndo() {
    undoStack = [...undoStack.slice(-MAX_HISTORY + 1), snapshot()];
    redoStack = []; // new action clears redo
  }

  function undo() {
    if (undoStack.length === 0) return;
    redoStack = [...redoStack, snapshot()];
    const prev = undoStack[undoStack.length - 1];
    undoStack = undoStack.slice(0, -1);
    nodes = prev.nodes;
    edges = prev.edges;
  }

  function redo() {
    if (redoStack.length === 0) return;
    undoStack = [...undoStack, snapshot()];
    const next = redoStack[redoStack.length - 1];
    redoStack = redoStack.slice(0, -1);
    nodes = next.nodes;
    edges = next.edges;
  }

  $: canUndo = undoStack.length > 0;
  $: canRedo = redoStack.length > 0;

  // ── Lifecycle ───────────────────────────────────────────────
  onMount(async () => {
    if (params.id) {
      try {
        pipeline = await api.pipelines.get(params.id);
        nodes = pipeline.nodes || [];
        edges = pipeline.edges || [];
        // Initial snapshot for undo
        undoStack = [snapshot()];
      } catch (e) {
        error = "Failed to load pipeline";
      }
    }
    loading = false;
  });

  // ── Node operations (push undo before each mutation) ────────
  function addNode(e: CustomEvent<{ type: string; x: number; y: number }>) {
    pushUndo();
    const { type, x, y } = e.detail;
    const config = nodeTypeConfig[type];
    const node: Node = {
      id: newNodeId(),
      type: type as NodeType,
      name: config?.label || type,
      config: {},
      position: { x, y },
    };
    nodes = [...nodes, node];
  }

  function selectNode(e: CustomEvent<string | null>) {
    selectedNodeId = e.detail;
  }

  function updateNode(e: CustomEvent<Node>) {
    pushUndo();
    const updated = e.detail;
    nodes = nodes.map((n) => (n.id === updated.id ? updated : n));
  }

  function deleteNode(e: CustomEvent<string>) {
    pushUndo();
    const id = e.detail;
    nodes = nodes.filter((n) => n.id !== id);
    edges = edges.filter((e) => e.from !== id && e.to !== id);
    if (selectedNodeId === id) selectedNodeId = null;
  }

  function onEdgeAdded() {
    // Edge was already added to the array by PipelineCanvas, just save undo point
    // We push before the change, so we need a slight workaround:
    // PipelineCanvas binds edges, so the push happens in handleGlobalKeydown or we do it on the event
    pushUndo();
  }

  // ── Save / Run ──────────────────────────────────────────────
  async function save() {
    if (!pipeline) return;
    saving = true;
    error = "";
    try {
      pipeline.nodes = nodes;
      pipeline.edges = edges;
      await api.pipelines.update(pipeline.id, pipeline);
      notify.success("Pipeline saved");
    } catch (e) {
      error = "Failed to save";
    } finally {
      saving = false;
    }
  }

  async function triggerRun() {
    if (!pipeline) return;
    try {
      await save();
      await api.runs.trigger(pipeline.id);
      window.location.hash = `#/pipelines/${pipeline.id}/runs`;
    } catch (e) {
      error = "Failed to trigger run";
    }
  }

  async function dryRun() {
    if (!pipeline) return;
    previewing = true;
    previewResults = {};
    previewNodeId = null;
    error = "";
    try {
      await save();
      const res = await fetch(`/api/pipelines/${pipeline.id}/dry-run`, { method: "POST", headers: authHeaders() });
      const data = await res.json();
      if (data.results) {
        previewResults = data.results;
        const firstKey = Object.keys(data.results).find(k => data.results[k].rows?.length > 0);
        if (firstKey) previewNodeId = firstKey;
      }
      if (data.error) error = `Preview partial: ${data.error}`;
    } catch (e) {
      error = "Failed to run preview";
    } finally {
      previewing = false;
    }
  }

  async function clonePipeline() {
    if (!pipeline) return;
    try {
      const res = await fetch(`/api/pipelines/${pipeline.id}/clone`, { method: "POST", headers: authHeaders() });
      if (!res.ok) throw new Error();
      const clone = await res.json();
      notify.success(`Cloned as "${clone.name}"`);
      window.location.hash = `#/pipelines/${clone.id}`;
    } catch {
      notify.error("Failed to clone pipeline");
    }
  }

  function doAutoLayout() {
    pushUndo();
    nodes = autoLayout(nodes, edges);
  }

  function toYaml(): string {
    const p = {
      name: pipeline?.name || "untitled",
      schedule: pipeline?.schedule || "",
      nodes: nodes.map((n) => ({
        id: n.id, type: n.type, name: n.name, config: n.config,
      })),
      edges: edges.map((e) => ({ from: e.from, to: e.to })),
    };
    return JSON.stringify(p, null, 2);
  }

  function toggleYaml() {
    showYaml = !showYaml;
    if (showYaml) yamlText = toYaml();
  }

  $: selectedNode = nodes.find((n) => n.id === selectedNodeId) || null;

  function handleGlobalKeydown(e: KeyboardEvent) {
    const tag = (e.target as HTMLElement)?.tagName;
    if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT") return;

    // Ctrl+Z = undo
    if ((e.ctrlKey || e.metaKey) && !e.shiftKey && e.key === "z") {
      e.preventDefault();
      undo();
      return;
    }
    // Ctrl+Shift+Z or Ctrl+Y = redo
    if ((e.ctrlKey || e.metaKey) && (e.shiftKey && e.key === "z" || e.key === "y")) {
      e.preventDefault();
      redo();
      return;
    }
    // Ctrl+S = save
    if ((e.ctrlKey || e.metaKey) && e.key === "s") {
      e.preventDefault();
      save();
      return;
    }
    // Delete/Backspace = delete selected node
    if ((e.key === "Delete" || e.key === "Backspace") && selectedNodeId) {
      e.preventDefault();
      pushUndo();
      nodes = nodes.filter((n) => n.id !== selectedNodeId);
      edges = edges.filter((e) => e.from !== selectedNodeId && e.to !== selectedNodeId);
      selectedNodeId = null;
      return;
    }
    // Escape = deselect
    if (e.key === "Escape") {
      selectedNodeId = null;
    }
  }
</script>

<svelte:window on:keydown={handleGlobalKeydown} />

<div class="editor animate-in">
  {#if loading}
    <div class="loading">Loading...</div>
  {:else}
    <div class="toolbar">
      <div class="toolbar-left">
        <a href="#/pipelines" class="back-link">
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none">
            <path d={icons.arrowLeft.d} stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" />
          </svg>
          Pipelines
        </a>
        <span class="separator">/</span>
        <span class="pipeline-name">{pipeline?.name || "New Pipeline"}</span>
        <span class="separator">|</span>
        <div class="schedule-input">
          <svg width="13" height="13" viewBox="0 0 24 24" fill="none">
            <circle cx="12" cy="12" r="10" stroke="currentColor" stroke-width="1.5" />
            <path d="M12 6v6l4 2" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" />
          </svg>
          <input
            class="schedule-field"
            value={pipeline?.schedule || ""}
            on:input={(e) => { if (pipeline) pipeline.schedule = e.currentTarget.value; }}
            placeholder="No schedule (manual)"
            title="Cron expression, e.g. 0 2 * * * (daily at 2am)"
          />
        </div>
      </div>
      <div class="toolbar-right">
        <!-- Undo/Redo -->
        <button class="btn-icon-sm" on:click={undo} disabled={!canUndo} title="Undo (Ctrl+Z)">
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none">
            <path d="M3 10h13a4 4 0 0 1 0 8H9" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
            <path d="M7 6L3 10l4 4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
          </svg>
        </button>
        <button class="btn-icon-sm" on:click={redo} disabled={!canRedo} title="Redo (Ctrl+Shift+Z)">
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none">
            <path d="M21 10H8a4 4 0 0 0 0 8h7" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
            <path d="M17 6l4 4-4 4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
          </svg>
        </button>

        <span class="toolbar-sep"></span>

        <button class="btn-sm" on:click={doAutoLayout}>
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none">
            <path d={icons.layout.d} stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" />
          </svg>
          Layout
        </button>
        <button class="btn-sm" on:click={toggleYaml}>
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none">
            <path d={icons.code.d} stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" />
          </svg>
          {showYaml ? "Canvas" : "JSON"}
        </button>
        <button class="btn-sm" on:click={validateNodes} title="Check node configs">
          Validate
        </button>
        <button class="btn-sm" on:click={clonePipeline} title="Duplicate this pipeline">
          Clone
        </button>

        <span class="toolbar-sep"></span>

        <button class="btn-sm btn-preview" on:click={dryRun} disabled={previewing}>
          {previewing ? "Previewing..." : "Preview"}
        </button>
        <button class="btn-sm btn-save" on:click={save} disabled={saving}>
          {saving ? "Saving..." : "Save"}
        </button>
        <button class="btn-sm btn-run" on:click={triggerRun}>
          <svg width="12" height="12" viewBox="0 0 24 24" fill="none">
            <path d={icons.play.d} fill="currentColor" />
          </svg>
          Run
        </button>
      </div>
    </div>

    {#if error}
      <div class="error-bar">{error}</div>
    {/if}

    <!-- Validation issues bar -->
    {#if Object.keys(nodeIssues).length > 0}
      <div class="validation-bar">
        {#each Object.values(nodeIssues) as issue}
          <!-- svelte-ignore a11y_no_static_element_interactions -->
          <span
            class="validation-item"
            class:is-error={issue.errors.length > 0}
            class:is-warning={issue.errors.length === 0}
            on:click={() => selectedNodeId = issue.node_id}
            on:keydown={() => {}}
          >
            <span class="val-dot"></span>
            <strong>{issue.node_name}</strong>:
            {issue.errors.length > 0 ? issue.errors.join(", ") : issue.warnings.join(", ")}
          </span>
        {/each}
      </div>
    {/if}

    <div class="editor-body">
      <div class="palette-sidebar">
        <NodePalette />
      </div>

      <div class="canvas-area">
        {#if showYaml}
          <pre class="yaml-view">{yamlText}</pre>
        {:else}
          <PipelineCanvas
            bind:nodes
            bind:edges
            bind:selectedNodeId
            nodeStatuses={{}}
            on:selectNode={selectNode}
            on:addNode={addNode}
            on:edgeAdded={onEdgeAdded}
          />
        {/if}
      </div>

      <div class="config-sidebar">
        <NodeConfigPanel
          node={selectedNode}
          on:update={updateNode}
          on:delete={deleteNode}
        />
        <!-- Show validation issues for selected node -->
        {#if selectedNode && nodeIssues[selectedNode.id]}
          <div class="node-issues">
            {#each nodeIssues[selectedNode.id].errors as err}
              <div class="issue-row issue-error">{err}</div>
            {/each}
            {#each nodeIssues[selectedNode.id].warnings as warn}
              <div class="issue-row issue-warning">{warn}</div>
            {/each}
          </div>
        {/if}
      </div>
    </div>

    <!-- Dry-run preview panel -->
    {#if Object.keys(previewResults).length > 0}
      <div class="preview-panel">
        <div class="preview-panel-header">
          <span class="preview-panel-title">Preview (first 10 rows)</span>
          <button class="btn-close" on:click={() => { previewResults = {}; previewNodeId = null; }}>Close</button>
        </div>
        <div class="preview-tabs">
          {#each Object.entries(previewResults) as [nid, result]}
            <button
              class="preview-tab"
              class:active={previewNodeId === nid}
              on:click={() => previewNodeId = previewNodeId === nid ? null : nid}
            >
              {result.name || nid}
              <span class="tab-count">{result.rows?.length || 0}</span>
            </button>
          {/each}
        </div>
        {#if previewNodeId && previewResults[previewNodeId]}
          {@const pr = previewResults[previewNodeId]}
          <div class="preview-table-scroll">
            <table class="preview-table">
              <thead>
                <tr>
                  <th class="row-num">#</th>
                  {#each pr.columns || [] as col}
                    <th>{col}</th>
                  {/each}
                </tr>
              </thead>
              <tbody>
                {#each pr.rows || [] as row, i}
                  <tr>
                    <td class="row-num">{i + 1}</td>
                    {#each pr.columns || [] as col}
                      <td>{row[col] ?? ""}</td>
                    {/each}
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {/if}
      </div>
    {/if}
  {/if}
</div>

<style>
  .editor {
    display: flex;
    flex-direction: column;
    height: calc(100vh - var(--space-xl) * 2);
  }

  .loading {
    display: flex;
    align-items: center;
    justify-content: center;
    height: 100%;
    color: var(--text-muted);
  }

  .toolbar {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding-bottom: 12px;
    margin-bottom: 12px;
    border-bottom: 1px solid var(--border-sidebar);
    flex-shrink: 0;
  }
  .toolbar-left {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .back-link {
    display: flex;
    align-items: center;
    gap: 4px;
    font-size: 13px;
    color: var(--text-muted);
  }
  .back-link:hover { color: var(--text-primary); }
  .separator { color: var(--text-ghost); }
  .pipeline-name { font-weight: 600; font-size: 14px; }

  .schedule-input {
    display: flex;
    align-items: center;
    gap: 5px;
    color: var(--text-muted);
  }
  .schedule-field {
    font-family: 'JetBrains Mono', monospace;
    font-size: 11px;
    padding: 3px 8px;
    width: 160px;
    background: var(--bg-sidebar);
    border: 1px solid var(--border-subtle);
    border-radius: 4px;
    color: var(--text-secondary);
  }
  .schedule-field:focus { border-color: var(--accent); color: var(--text-primary); }

  .toolbar-right {
    display: flex;
    align-items: center;
    gap: 6px;
  }

  .toolbar-sep {
    width: 1px;
    height: 20px;
    background: var(--border-subtle);
    margin: 0 2px;
  }

  .btn-icon-sm {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 30px; height: 30px;
    border-radius: 6px;
    color: var(--text-muted);
    background: var(--bg-secondary);
    border: 1px solid var(--border-subtle);
    transition: all 150ms ease;
  }
  .btn-icon-sm:hover:not(:disabled) {
    color: var(--text-primary);
    background: var(--bg-tertiary);
    border-color: var(--border-hover);
  }
  .btn-icon-sm:disabled {
    opacity: 0.3;
    cursor: default;
  }

  .btn-sm {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    padding: 6px 12px;
    border-radius: 6px;
    font-size: 12.5px;
    font-weight: 500;
    background: var(--bg-secondary);
    border: 1px solid var(--border-subtle);
    color: var(--text-secondary);
    transition: all 150ms ease;
  }
  .btn-sm:hover { background: var(--border-subtle); color: var(--text-primary); border-color: var(--text-ghost); }
  .btn-sm.btn-save {
    background: var(--accent);
    border-color: var(--accent);
    color: white;
  }
  .btn-sm.btn-save:hover { background: var(--accent-hover); }
  .btn-sm.btn-run {
    background: var(--success-bg);
    border-color: rgba(34, 197, 94, 0.3);
    color: #22c55e;
  }
  .btn-sm.btn-run:hover { background: rgba(34, 197, 94, 0.15); }
  .btn-sm.btn-preview {
    background: var(--accent-glow);
    border-color: rgba(99, 102, 241, 0.3);
    color: var(--accent-text);
  }
  .btn-sm.btn-preview:hover { background: rgba(99, 102, 241, 0.15); }

  .error-bar {
    background: var(--failed-bg);
    border: 1px solid rgba(239, 68, 68, 0.2);
    color: var(--failed);
    padding: 8px 14px;
    border-radius: 6px;
    font-size: 13px;
    margin-bottom: 8px;
  }

  .validation-bar {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    padding: 8px 12px;
    background: var(--bg-secondary);
    border: 1px solid var(--border-subtle);
    border-radius: 6px;
    margin-bottom: 8px;
    font-size: 12px;
  }
  .validation-item {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    cursor: pointer;
    padding: 2px 8px;
    border-radius: 4px;
    transition: background 150ms ease;
  }
  .validation-item:hover { background: var(--bg-tertiary); }
  .validation-item.is-error { color: var(--failed); }
  .validation-item.is-warning { color: var(--warning); }
  .val-dot {
    width: 6px; height: 6px; border-radius: 50%;
    flex-shrink: 0;
  }
  .is-error .val-dot { background: var(--failed); }
  .is-warning .val-dot { background: var(--warning); }

  .editor-body {
    display: flex;
    flex: 1;
    gap: 10px;
    min-height: 0;
  }

  .palette-sidebar {
    width: 190px;
    flex-shrink: 0;
    background: var(--bg-sidebar);
    border: 1px solid var(--border-sidebar);
    border-radius: 8px;
    overflow-y: auto;
  }

  .canvas-area {
    flex: 1;
    min-width: 0;
  }

  .config-sidebar {
    width: 270px;
    flex-shrink: 0;
    background: var(--bg-sidebar);
    border: 1px solid var(--border-sidebar);
    border-radius: 8px;
    overflow: hidden;
    display: flex;
    flex-direction: column;
  }

  .node-issues {
    padding: 8px 12px;
    border-top: 1px solid var(--border-subtle);
    font-size: 11px;
  }
  .issue-row {
    padding: 3px 0;
  }
  .issue-error { color: var(--failed); }
  .issue-warning { color: var(--warning); }

  .yaml-view {
    width: 100%;
    height: 100%;
    background: var(--bg-code);
    border: 1px solid var(--border-sidebar);
    border-radius: 8px;
    padding: 14px;
    font-family: 'JetBrains Mono', monospace;
    font-size: 12px;
    color: var(--text-secondary);
    overflow: auto;
    white-space: pre-wrap;
    margin: 0;
  }

  .preview-panel {
    flex-shrink: 0;
    max-height: 260px;
    background: var(--bg-code);
    border: 1px solid var(--border-sidebar);
    border-radius: 8px;
    margin-top: 10px;
    overflow: hidden;
    display: flex;
    flex-direction: column;
  }
  .preview-panel-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 8px 14px;
    border-bottom: 1px solid var(--border-sidebar);
    background: var(--bg-sidebar);
  }
  .preview-panel-title {
    font-size: 11px;
    font-weight: 600;
    color: var(--text-secondary);
    text-transform: uppercase;
    letter-spacing: 0.08em;
  }
  .btn-close {
    font-size: 11px;
    color: var(--text-dim);
    padding: 2px 8px;
    border-radius: 4px;
    transition: all 150ms ease;
  }
  .btn-close:hover { color: var(--text-primary); background: var(--border-subtle); }

  .preview-tabs {
    display: flex;
    gap: 4px;
    padding: 8px 14px 4px;
    flex-wrap: wrap;
  }
  .preview-tab {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    padding: 4px 8px;
    border-radius: 4px;
    font-size: 11px;
    font-weight: 500;
    background: var(--bg-secondary);
    border: 1px solid var(--border-subtle);
    color: var(--text-secondary);
    transition: all 150ms ease;
  }
  .preview-tab:hover { border-color: var(--text-ghost); color: var(--text-primary); }
  .preview-tab.active { border-color: var(--accent); color: var(--accent-text); background: var(--accent-glow); }
  .tab-count {
    font-family: 'JetBrains Mono', monospace;
    font-size: 9px;
    color: var(--text-dim);
    background: var(--bg-code);
    padding: 0 4px;
    border-radius: 3px;
  }

  .preview-table-scroll {
    overflow: auto;
    flex: 1;
  }
  .preview-table {
    width: 100%;
    border-collapse: collapse;
    font-family: 'JetBrains Mono', monospace;
    font-size: 11px;
  }
  .preview-table th {
    background: var(--bg-card-hover);
    color: var(--text-muted);
    font-size: 10px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    padding: 5px 10px;
    text-align: left;
    white-space: nowrap;
    position: sticky;
    top: 0;
    border-bottom: 1px solid var(--border-sidebar);
  }
  .preview-table td {
    padding: 3px 10px;
    color: var(--text-primary);
    border-bottom: 1px solid var(--border-subtle);
    white-space: nowrap;
    max-width: 250px;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .preview-table .row-num {
    color: var(--text-ghost);
    text-align: right;
    width: 30px;
  }
</style>
