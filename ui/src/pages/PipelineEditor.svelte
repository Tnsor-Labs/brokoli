<script lang="ts">
  import { onMount } from "svelte";
  import { api } from "../lib/api";
  import { newNodeId, nodeTypeConfig, autoLayout } from "../lib/dag";
  import { icons } from "../lib/icons";
  import PipelineCanvas from "../components/PipelineCanvas.svelte";
  import NodePalette from "../components/NodePalette.svelte";
  import NodeConfigPanel from "../components/NodeConfigPanel.svelte";
  import type { Pipeline, PipelineVersion, Node, Edge, NodeType } from "../lib/types";
  import { notify } from "../lib/toast";
  import Skeleton from "../components/Skeleton.svelte";
  import Breadcrumb from "../components/Breadcrumb.svelte";
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

  // ── Version History ─────────────────────────────────────────
  let showHistory = false;
  let versions: PipelineVersion[] = [];
  let loadingVersions = false;
  let rollingBack = false;

  async function loadVersions() {
    if (!pipeline?.id) return;
    loadingVersions = true;
    try {
      versions = await api.pipelines.versions(pipeline.id);
    } catch {
      versions = [];
    }
    loadingVersions = false;
  }

  function toggleHistory() {
    showHistory = !showHistory;
    if (showHistory) loadVersions();
  }

  async function rollbackTo(version: number) {
    if (!pipeline?.id) return;
    rollingBack = true;
    try {
      const restored = await api.pipelines.rollback(pipeline.id, version);
      pipeline = restored;
      nodes = restored.nodes || [];
      edges = restored.edges || [];
      undoStack = [snapshot()];
      redoStack = [];
      notify.success(`Rolled back to v${version}`);
      showHistory = false;
    } catch {
      notify.error("Rollback failed");
    }
    rollingBack = false;
  }

  function timeAgo(dateStr: string): string {
    const d = new Date(dateStr);
    const now = Date.now();
    const diffMs = now - d.getTime();
    const mins = Math.floor(diffMs / 60000);
    if (mins < 1) return "just now";
    if (mins < 60) return `${mins}m ago`;
    const hours = Math.floor(mins / 60);
    if (hours < 24) return `${hours}h ago`;
    const days = Math.floor(hours / 24);
    return `${days}d ago`;
  }

  // ── Pipeline Settings Panel (consolidated) ──────────────────
  let showSLA = false;
  let showWebhook = false;
  let showPipelineSettings = false;
  let tagInput = "";
  let generatingToken = false;

  async function generateWebhookToken() {
    if (!pipeline) return;
    generatingToken = true;
    // Generate a token client-side (matches engine/webhook.go format)
    const bytes = new Uint8Array(24);
    crypto.getRandomValues(bytes);
    const hex = Array.from(bytes).map(b => b.toString(16).padStart(2, "0")).join("");
    pipeline.webhook_token = "whk_" + hex;
    generatingToken = false;
  }

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

  function duplicateNode(nodeId: string) {
    const source = nodes.find(n => n.id === nodeId);
    if (!source) return;
    pushUndo();
    const clone: Node = {
      id: newNodeId(),
      type: source.type,
      name: source.name + " (copy)",
      config: JSON.parse(JSON.stringify(source.config)),
      position: { x: source.position.x + 40, y: source.position.y + 40 },
    };
    nodes = [...nodes, clone];
    selectedNodeId = clone.id;
    notify.success("Node duplicated");
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
    } catch (e: any) {
      error = "Failed to save: " + (e.message || e);
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
    } catch (e: any) {
      error = "Failed to trigger run: " + (e.message || e);
      notify.error(error);
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
    } catch (e: any) {
      error = "Failed to run preview: " + (e.message || e);
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
    // D = duplicate selected node
    if (e.key === "d" && selectedNodeId) {
      e.preventDefault();
      duplicateNode(selectedNodeId);
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
    <div style="display:flex;flex-direction:column;gap:8px;padding:24px">
      <Skeleton height="40px" /><Skeleton height="calc(100vh - 160px)" />
    </div>
  {:else}
    <div class="toolbar">
      <div class="toolbar-left">
        <Breadcrumb items={[
          { label: "Pipelines", href: "#/pipelines" },
          { label: pipeline?.name || "Pipeline", href: `#/pipelines/${params?.id}/runs` },
          { label: "Editor" }
        ]} />
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
        <button class="btn-sm" class:active-toggle={showHistory} on:click={toggleHistory} title="Version history">
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none">
            <path d={icons.history.d} stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" />
          </svg>
          History
        </button>
        <button class="btn-sm" class:active-toggle={showPipelineSettings} on:click={() => showPipelineSettings = !showPipelineSettings} title="Pipeline settings">
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none">
            <path d={icons.settings.d} stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" />
          </svg>
          Settings
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

    <!-- Unified Pipeline Settings Panel -->
    {#if showPipelineSettings && pipeline}
      <div class="settings-panel">
        <div class="settings-grid">
          <!-- Description -->
          <div class="setting-item full">
            <label>Description</label>
            <input
              value={pipeline.description || ""}
              on:input={(e) => { if (pipeline) pipeline.description = e.currentTarget.value; }}
              placeholder="What does this pipeline do?"
            />
          </div>

          <!-- Tags -->
          <div class="setting-item full">
            <label>Tags</label>
            <div class="tag-editor">
              {#each pipeline.tags || [] as tag, i}
                <span class="tag-chip">
                  {tag}
                  <button class="tag-remove" on:click={() => { if (pipeline) pipeline.tags = (pipeline.tags || []).filter((_, j) => j !== i); }}>x</button>
                </span>
              {/each}
              <input
                class="tag-input"
                bind:value={tagInput}
                placeholder="Add tag..."
                on:keydown={(e) => {
                  if (e.key === "Enter" && tagInput.trim() && pipeline) {
                    pipeline.tags = [...(pipeline.tags || []), tagInput.trim()];
                    tagInput = "";
                    e.preventDefault();
                  }
                }}
              />
            </div>
          </div>

          <!-- SLA -->
          <div class="setting-item">
            <label>SLA Deadline</label>
            <input
              type="time"
              value={pipeline.sla_deadline || ""}
              on:input={(e) => { if (pipeline) pipeline.sla_deadline = e.currentTarget.value; }}
            />
          </div>
          <div class="setting-item">
            <label>SLA Timezone</label>
            <select
              value={pipeline.sla_timezone || "UTC"}
              on:change={(e) => { if (pipeline) pipeline.sla_timezone = e.currentTarget.value; }}
            >
              <option value="UTC">UTC</option>
              <option value="America/New_York">US Eastern</option>
              <option value="America/Chicago">US Central</option>
              <option value="America/Los_Angeles">US Pacific</option>
              <option value="Europe/London">London</option>
              <option value="Europe/Berlin">Berlin</option>
              <option value="Asia/Tokyo">Tokyo</option>
              <option value="Australia/Sydney">Sydney</option>
            </select>
          </div>

          <!-- Webhook -->
          <div class="setting-item full">
            <label>Webhook Token</label>
            <div class="webhook-row">
              {#if pipeline.webhook_token}
                <code class="webhook-token-display">{pipeline.webhook_token.slice(0, 20)}...</code>
                <button class="setting-btn danger" on:click={() => { if (pipeline) pipeline.webhook_token = ""; }}>Revoke</button>
              {:else}
                <button class="setting-btn" on:click={generateWebhookToken}>Generate Token</button>
              {/if}
            </div>
          </div>
        </div>
      </div>
    {/if}

    <!-- SLA Settings panel (legacy, hidden — now in unified settings) -->
    {#if showSLA && pipeline}
      <div class="sla-bar">
        <div class="sla-title">
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none">
            <path d={icons.shield.d} stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" />
          </svg>
          SLA Deadline
        </div>
        <div class="sla-fields">
          <div class="sla-field">
            <label>Must complete by</label>
            <input
              type="time"
              class="sla-input"
              value={pipeline.sla_deadline || ""}
              on:input={(e) => { if (pipeline) pipeline.sla_deadline = e.currentTarget.value; }}
              placeholder="HH:MM"
            />
          </div>
          <div class="sla-field">
            <label>Timezone</label>
            <select
              class="sla-input"
              value={pipeline.sla_timezone || "UTC"}
              on:change={(e) => { if (pipeline) pipeline.sla_timezone = e.currentTarget.value; }}
            >
              <option value="UTC">UTC</option>
              <option value="America/New_York">US Eastern</option>
              <option value="America/Chicago">US Central</option>
              <option value="America/Denver">US Mountain</option>
              <option value="America/Los_Angeles">US Pacific</option>
              <option value="Europe/London">London</option>
              <option value="Europe/Berlin">Berlin</option>
              <option value="Europe/Paris">Paris</option>
              <option value="Asia/Tokyo">Tokyo</option>
              <option value="Asia/Shanghai">Shanghai</option>
              <option value="Australia/Sydney">Sydney</option>
            </select>
          </div>
          {#if pipeline.sla_deadline}
            <button class="sla-clear" on:click={() => { if (pipeline) { pipeline.sla_deadline = ""; pipeline.sla_timezone = ""; } }}>
              Clear SLA
            </button>
          {/if}
        </div>
        <span class="sla-hint">Pipeline must finish by this time. Alerts fire on breach.</span>
      </div>
    {/if}

    <!-- Webhook Config panel -->
    {#if showWebhook && pipeline}
      <div class="sla-bar">
        <div class="sla-title">
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none">
            <path d={icons.api.d} stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" />
          </svg>
          Webhook Trigger
        </div>
        <div class="sla-fields">
          {#if pipeline.webhook_token}
            <code class="webhook-token-display">{pipeline.webhook_token.slice(0, 16)}...</code>
            <span class="sla-hint" style="margin-left: 0">
              POST /api/pipelines/{pipeline.id}/webhook?token=...
            </span>
          {:else}
            <button class="sla-clear" style="color: var(--accent)" on:click={generateWebhookToken}>
              {generatingToken ? "Generating..." : "Generate Token"}
            </button>
          {/if}
        </div>
        {#if pipeline.webhook_token}
          <button class="sla-clear" on:click={() => { if (pipeline) pipeline.webhook_token = ""; }}>
            Revoke
          </button>
        {/if}
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
        {#if showHistory}
          <!-- Version History Panel -->
          <div class="version-panel">
            <div class="version-header">
              <span class="version-title">Version History</span>
              <button class="btn-close" on:click={() => showHistory = false}>Close</button>
            </div>
            {#if loadingVersions}
              <div class="version-loading">Loading...</div>
            {:else if versions.length === 0}
              <div class="version-empty">No versions yet. Save to create the first snapshot.</div>
            {:else}
              <div class="version-list">
                {#each versions as v, i}
                  <div class="version-item" class:latest={i === 0}>
                    <div class="version-meta">
                      <span class="version-num">v{v.version}</span>
                      <span class="version-time">{timeAgo(v.created_at)}</span>
                    </div>
                    {#if v.message}
                      <div class="version-msg">{v.message}</div>
                    {/if}
                    {#if i > 0}
                      <button
                        class="version-restore"
                        on:click={() => rollbackTo(v.version)}
                        disabled={rollingBack}
                      >
                        {rollingBack ? "Restoring..." : "Restore"}
                      </button>
                    {:else}
                      <span class="version-current">Current</span>
                    {/if}
                  </div>
                {/each}
              </div>
            {/if}
          </div>
        {:else}
          <NodeConfigPanel
            node={selectedNode}
            on:update={updateNode}
            on:delete={deleteNode}
            on:duplicate={(e) => duplicateNode(e.detail)}
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

  /* ── Active toggle for toolbar buttons ── */
  .btn-sm.active-toggle {
    border-color: var(--accent);
    color: var(--accent-text);
    background: var(--accent-glow);
  }

  /* ── SLA Settings Bar ── */
  .sla-bar {
    display: flex;
    align-items: center;
    gap: 16px;
    padding: 10px 14px;
    background: var(--bg-secondary);
    border: 1px solid var(--border-subtle);
    border-radius: 6px;
    margin-bottom: 8px;
    font-size: 12px;
  }
  .sla-title {
    display: flex;
    align-items: center;
    gap: 6px;
    font-weight: 600;
    color: var(--text-primary);
    white-space: nowrap;
    font-size: 12px;
  }
  .sla-fields {
    display: flex;
    align-items: center;
    gap: 12px;
  }
  .sla-field {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .sla-field label {
    font-size: 11px;
    color: var(--text-muted);
    white-space: nowrap;
  }
  .sla-input {
    font-family: 'JetBrains Mono', monospace;
    font-size: 11px;
    padding: 4px 8px;
    background: var(--bg-sidebar);
    border: 1px solid var(--border-subtle);
    border-radius: 4px;
    color: var(--text-secondary);
  }
  .sla-input:focus {
    border-color: var(--accent);
    color: var(--text-primary);
    outline: none;
  }
  .sla-clear {
    font-size: 11px;
    color: var(--text-dim);
    padding: 4px 8px;
    border-radius: 4px;
    transition: all 150ms ease;
  }
  .sla-clear:hover {
    color: var(--failed);
    background: var(--failed-bg);
  }
  .sla-hint {
    font-size: 10px;
    color: var(--text-ghost);
    margin-left: auto;
  }

  /* ── Unified Settings Panel ── */
  .settings-panel {
    background: var(--bg-secondary); border: 1px solid var(--border);
    border-radius: 8px; padding: 14px 16px; margin-bottom: 8px;
  }
  .settings-grid {
    display: grid; grid-template-columns: 1fr 1fr; gap: 10px;
  }
  .setting-item { display: flex; flex-direction: column; gap: 4px; }
  .setting-item.full { grid-column: 1 / -1; }
  .setting-item label {
    font-size: 10px; font-weight: 600; text-transform: uppercase;
    letter-spacing: 0.06em; color: var(--text-muted);
  }
  .setting-item input, .setting-item select {
    padding: 6px 10px; font-size: 12px;
    background: var(--bg-primary); border: 1px solid var(--border-subtle);
    border-radius: 5px; color: var(--text-primary); font-family: var(--font-ui);
  }
  .setting-item input:focus, .setting-item select:focus {
    border-color: var(--accent); outline: none;
  }
  .setting-btn {
    padding: 5px 12px; border-radius: 5px; font-size: 11px; font-weight: 500;
    background: var(--accent-glow); color: var(--accent-text);
    border: 1px solid rgba(13,148,136,0.2);
  }
  .setting-btn.danger {
    background: var(--failed-bg); color: var(--failed);
    border-color: rgba(239,68,68,0.2);
  }
  .webhook-row { display: flex; align-items: center; gap: 8px; }
  .tag-editor {
    display: flex; flex-wrap: wrap; gap: 4px; align-items: center;
    padding: 4px 8px; background: var(--bg-primary);
    border: 1px solid var(--border-subtle); border-radius: 5px;
    min-height: 32px;
  }
  .tag-chip {
    display: inline-flex; align-items: center; gap: 3px;
    font-size: 11px; font-weight: 500; padding: 2px 8px;
    background: var(--accent-glow); color: var(--accent);
    border-radius: 4px;
  }
  .tag-remove {
    font-size: 10px; color: var(--accent); cursor: pointer;
    line-height: 1; padding: 0 2px; opacity: 0.6;
  }
  .tag-remove:hover { opacity: 1; }
  .tag-input {
    border: none !important; background: none !important;
    padding: 2px 4px !important; font-size: 11px;
    min-width: 80px; flex: 1; outline: none;
    color: var(--text-primary);
  }

  .webhook-token-display {
    font-family: 'JetBrains Mono', monospace;
    font-size: 11px; color: var(--accent);
    background: var(--bg-code); padding: 4px 8px;
    border-radius: 4px; border: 1px solid var(--border-subtle);
  }

  /* ── Version History Panel ── */
  .version-panel {
    display: flex;
    flex-direction: column;
    height: 100%;
  }
  .version-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 12px 14px;
    border-bottom: 1px solid var(--border-sidebar);
  }
  .version-title {
    font-size: 11px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--text-secondary);
  }
  .version-loading, .version-empty {
    padding: 20px 14px;
    font-size: 12px;
    color: var(--text-dim);
    text-align: center;
  }
  .version-list {
    flex: 1;
    overflow-y: auto;
    padding: 8px;
  }
  .version-item {
    padding: 10px;
    border-radius: 6px;
    margin-bottom: 4px;
    transition: background 150ms ease;
    border: 1px solid transparent;
  }
  .version-item:hover {
    background: var(--bg-tertiary);
  }
  .version-item.latest {
    border-color: var(--accent);
    background: var(--accent-glow);
  }
  .version-meta {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 4px;
  }
  .version-num {
    font-family: 'JetBrains Mono', monospace;
    font-size: 12px;
    font-weight: 600;
    color: var(--text-primary);
  }
  .version-time {
    font-size: 10px;
    color: var(--text-dim);
  }
  .version-msg {
    font-size: 11px;
    color: var(--text-muted);
    margin-bottom: 6px;
  }
  .version-restore {
    font-size: 10px;
    font-weight: 500;
    padding: 3px 10px;
    border-radius: 4px;
    background: var(--bg-secondary);
    border: 1px solid var(--border-subtle);
    color: var(--text-secondary);
    transition: all 150ms ease;
  }
  .version-restore:hover:not(:disabled) {
    border-color: var(--accent);
    color: var(--accent-text);
    background: var(--accent-glow);
  }
  .version-restore:disabled {
    opacity: 0.5;
    cursor: wait;
  }
  .version-current {
    font-size: 10px;
    font-weight: 600;
    color: var(--accent-text);
    text-transform: uppercase;
    letter-spacing: 0.08em;
  }

  @media (max-width: 768px) {
    .toolbar { flex-wrap: wrap; gap: 6px; }
    .toolbar-left { flex: 1; min-width: 0; }
    .toolbar-right { flex-wrap: wrap; }
    .toolbar-sep { display: none; }
    .schedule-input { display: none; }
    .editor-body { flex-direction: column; }
    .palette-sidebar { width: 100%; height: auto; max-height: 120px; flex-direction: row; overflow-x: auto; }
    .config-sidebar { width: 100%; max-height: 300px; }
    .canvas-area { min-height: 300px; }
    .btn-sm span { display: none; }
  }

  @media (max-width: 1024px) and (min-width: 769px) {
    .palette-sidebar { width: 150px; }
    .config-sidebar { width: 230px; }
  }
</style>
