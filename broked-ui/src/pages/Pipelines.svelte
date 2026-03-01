<script lang="ts">
  import { onMount } from "svelte";
  import { api } from "../lib/api";
  import { pipelines } from "../lib/stores";
  import { icons } from "../lib/icons";
  import { notify } from "../lib/toast";
  import StatusBadge from "../components/StatusBadge.svelte";
  import ConfirmDialog from "../components/ConfirmDialog.svelte";
  import type { Pipeline, Run } from "../lib/types";

  let confirmDelete = false;
  let deleteTargetId = "";
  let deleteTargetName = "";

  let loading = true;
  let pipelineRuns: Map<string, Run[]> = new Map();
  let scheduleInfo: Map<string, { next_run: string; schedule: string }> = new Map();
  let showCreateModal = false;
  let newName = "";
  let newDescription = "";
  let searchQuery = "";
  let selectedIds: Set<string> = new Set();

  function toggleSelect(id: string) {
    if (selectedIds.has(id)) {
      selectedIds.delete(id);
    } else {
      selectedIds.add(id);
    }
    selectedIds = new Set(selectedIds);
  }

  function selectAll() {
    if (selectedIds.size === $pipelines.length) {
      selectedIds = new Set();
    } else {
      selectedIds = new Set($pipelines.map(p => p.id));
    }
  }

  async function bulkAction(action: string) {
    if (selectedIds.size === 0) return;
    try {
      const res = await fetch("/api/pipelines/bulk", {
        method: "POST",
        headers: { "Content-Type": "application/json", ...authHeaders() },
        body: JSON.stringify({ ids: [...selectedIds], action }),
      });
      const data = await res.json();
      notify.success(`${action}: ${data.affected} pipelines affected`);
      selectedIds = new Set();
      await loadPipelines();
    } catch {
      notify.error("Bulk operation failed");
    }
  }

  onMount(async () => {
    await loadPipelines();
  });

  async function loadPipelines() {
    loading = true;
    try {
      const list = await api.pipelines.list();
      pipelines.set(list);
      for (const p of list) {
        const runs = await api.runs.listByPipeline(p.id);
        pipelineRuns.set(p.id, runs);
      }
      pipelineRuns = pipelineRuns; // trigger reactivity

      // Load scheduler status
      try {
        const schedRes = await fetch("/api/scheduler/status", { headers: authHeaders() });
        if (schedRes.ok) {
          const schedData = await schedRes.json();
          for (const s of schedData) {
            scheduleInfo.set(s.pipeline_id, { next_run: s.next_run, schedule: s.schedule });
          }
          scheduleInfo = new Map(scheduleInfo);
        }
      } catch {}
    } catch (e) {
      notify.error("Failed to load pipelines");
    } finally {
      loading = false;
    }
  }

  async function toggleEnabled(pipeline: Pipeline) {
    try {
      pipeline.enabled = !pipeline.enabled;
      await api.pipelines.update(pipeline.id, pipeline);
      pipelines.update(list => list.map(p => p.id === pipeline.id ? { ...p, enabled: pipeline.enabled } : p));
      notify.success(pipeline.enabled ? `${pipeline.name} enabled` : `${pipeline.name} paused`);
    } catch {
      pipeline.enabled = !pipeline.enabled; // revert
      notify.error("Failed to toggle pipeline");
    }
  }

  async function createPipeline() {
    if (!newName.trim()) return;
    try {
      await api.pipelines.create({
        name: newName,
        description: newDescription,
        enabled: true,
      });
      newName = "";
      newDescription = "";
      showCreateModal = false;
      await loadPipelines();
      notify.success("Pipeline created");
    } catch (e) {
      notify.error("Failed to create pipeline");
    }
  }

  async function triggerRun(pipelineId: string) {
    try {
      await api.runs.trigger(pipelineId);
      await loadPipelines();
    } catch (e) {
      notify.error("Failed to trigger run");
    }
  }

  async function deletePipeline(id: string) {
    try {
      await api.pipelines.delete(id);
      await loadPipelines();
    } catch (e) {
      notify.error("Failed to delete pipeline");
    }
  }

  function getLastRun(pipelineId: string): Run | undefined {
    return pipelineRuns.get(pipelineId)?.[0];
  }

  function formatSchedule(cron: string): string {
    if (!cron) return "Manual";
    return cron;
  }

  function getRunCounts(pipelineId: string): { success: number; failed: number; running: number; total: number } {
    const runs = pipelineRuns.get(pipelineId) || [];
    return {
      success: runs.filter(r => r.status === "success").length,
      failed: runs.filter(r => r.status === "failed").length,
      running: runs.filter(r => r.status === "running").length,
      total: runs.length,
    };
  }

  function formatNextRun(isoStr: string): string {
    const d = new Date(isoStr);
    const now = new Date();
    const diffMs = d.getTime() - now.getTime();
    if (diffMs < 0) return "overdue";
    const mins = Math.floor(diffMs / 60000);
    if (mins < 60) return `in ${mins}m`;
    const hrs = Math.floor(mins / 60);
    if (hrs < 24) return `in ${hrs}h ${mins % 60}m`;
    const days = Math.floor(hrs / 24);
    return `in ${days}d`;
  }

  let fileInput: HTMLInputElement;

  async function importYaml() {
    fileInput.click();
  }

  async function handleFileUpload(e: Event) {
    const input = e.target as HTMLInputElement;
    const file = input.files?.[0];
    if (!file) return;
    try {
      const text = await file.text();
      const res = await fetch("/api/pipelines/import", {
        method: "POST",
        headers: { "Content-Type": "application/x-yaml" },
        body: text,
      });
      if (!res.ok) {
        const err = await res.json();
        throw new Error(err.error || "Import failed");
      }
      await loadPipelines();
    } catch (e: any) {
      notify.error("Import failed: " + e.message);
    } finally {
      input.value = "";
    }
  }

  import { authHeaders } from "../lib/auth";

  // Pipeline templates
  const templates = [
    {
      name: "Blank",
      description: "Start from scratch",
      icon: "plus",
      nodes: [] as any[],
      edges: [] as any[],
    },
    {
      name: "CSV to File",
      description: "Read CSV, transform, write output",
      icon: "file",
      nodes: [
        { id: "s1", type: "source_file", name: "Read CSV", config: { path: "/data/input.csv" }, position: { x: 40, y: 120 } },
        { id: "t1", type: "transform", name: "Transform", config: { rules: [] }, position: { x: 360, y: 120 } },
        { id: "o1", type: "sink_file", name: "Write Output", config: { path: "/data/output.csv" }, position: { x: 680, y: 120 } },
      ],
      edges: [{ from: "s1", to: "t1" }, { from: "t1", to: "o1" }],
    },
    {
      name: "API to File",
      description: "Fetch API data, transform, save",
      icon: "api",
      nodes: [
        { id: "s1", type: "source_api", name: "Fetch API", config: { url: "https://api.example.com/data", method: "GET" }, position: { x: 40, y: 120 } },
        { id: "t1", type: "transform", name: "Transform", config: { rules: [] }, position: { x: 360, y: 120 } },
        { id: "o1", type: "sink_file", name: "Save to File", config: { path: "/data/api-output.json" }, position: { x: 680, y: 120 } },
      ],
      edges: [{ from: "s1", to: "t1" }, { from: "t1", to: "o1" }],
    },
    {
      name: "Join + Aggregate",
      description: "Join two sources, aggregate, output",
      icon: "merge",
      nodes: [
        { id: "s1", type: "source_file", name: "Orders", config: { path: "/data/orders.csv" }, position: { x: 40, y: 60 } },
        { id: "s2", type: "source_file", name: "Customers", config: { path: "/data/customers.csv" }, position: { x: 40, y: 220 } },
        { id: "j1", type: "join", name: "Join", config: { join_type: "inner", left_key: "customer_id", right_key: "id" }, position: { x: 360, y: 140 } },
        { id: "t1", type: "transform", name: "Aggregate", config: { rules: [{ type: "aggregate", group_by: ["customer_id"], aggregations: [{ column: "amount", function: "sum" }] }] }, position: { x: 680, y: 140 } },
        { id: "o1", type: "sink_file", name: "Output", config: { path: "/data/summary.csv" }, position: { x: 1000, y: 140 } },
      ],
      edges: [{ from: "s1", to: "j1" }, { from: "s2", to: "j1" }, { from: "j1", to: "t1" }, { from: "t1", to: "o1" }],
    },
    {
      name: "Python ETL",
      description: "Source → Python code → Quality check → Output",
      icon: "code",
      nodes: [
        { id: "s1", type: "source_file", name: "Input Data", config: { path: "/data/input.csv" }, position: { x: 40, y: 120 } },
        { id: "c1", type: "code", name: "Python Process", config: { script: "# Process data\noutput_data = {\"columns\": columns, \"rows\": rows}" }, position: { x: 360, y: 120 } },
        { id: "q1", type: "quality_check", name: "Quality Gate", config: { rules: [{ column: "id", check: "not_null", policy: "block" }] }, position: { x: 680, y: 120 } },
        { id: "o1", type: "sink_file", name: "Output", config: { path: "/data/processed.csv" }, position: { x: 1000, y: 120 } },
      ],
      edges: [{ from: "s1", to: "c1" }, { from: "c1", to: "q1" }, { from: "q1", to: "o1" }],
    },
  ];

  let selectedTemplate = 0;

  async function createFromTemplate() {
    if (!newName.trim()) return;
    const tmpl = templates[selectedTemplate];
    try {
      const created = await api.pipelines.create({
        name: newName,
        description: newDescription || tmpl.description,
        enabled: true,
        nodes: tmpl.nodes,
        edges: tmpl.edges,
      });
      newName = "";
      newDescription = "";
      selectedTemplate = 0;
      showCreateModal = false;
      await loadPipelines();
      notify.success("Pipeline created");
      // Navigate to editor if template has nodes
      if (tmpl.nodes.length > 0) {
        window.location.hash = `#/pipelines/${created.id}`;
      }
    } catch (e) {
      notify.error("Failed to create pipeline");
    }
  }

  async function clonePipeline(id: string) {
    try {
      const res = await fetch(`/api/pipelines/${id}/clone`, {
        method: "POST",
        headers: authHeaders(),
      });
      if (!res.ok) throw new Error();
      const clone = await res.json();
      notify.success(`Cloned as "${clone.name}"`);
      await loadPipelines();
    } catch {
      notify.error("Failed to clone pipeline");
    }
  }

  async function exportYaml(id: string, name: string) {
    try {
      const res = await fetch(`/api/pipelines/${id}/export`);
      if (!res.ok) throw new Error("Export failed");
      const text = await res.text();
      const blob = new Blob([text], { type: "application/x-yaml" });
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = `${name}.yaml`;
      a.click();
      URL.revokeObjectURL(url);
    } catch (e: any) {
      notify.error("Export failed: " + e.message);
    }
  }
</script>

<div class="pipelines-page animate-in">
  <input type="file" accept=".yaml,.yml" bind:this={fileInput} on:change={handleFileUpload} style="display:none" />

  <header class="page-header">
    <h1>Pipelines</h1>
    <div class="header-actions">
      <button class="btn-secondary" on:click={importYaml}>Import YAML</button>
      <button class="btn-primary" on:click={() => (showCreateModal = true)}>
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none"><path d={icons.plus.d} stroke="currentColor" stroke-width="2" stroke-linecap="round" /></svg>
        New Pipeline
      </button>
    </div>
  </header>

  <div class="search-bar">
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none">
      <path d={icons.search.d} stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" />
    </svg>
    <input
      type="text"
      class="search-input"
      bind:value={searchQuery}
      placeholder="Search pipelines..."
    />
  </div>

  {#if selectedIds.size > 0}
    <div class="bulk-bar">
      <span class="bulk-count">{selectedIds.size} selected</span>
      <button class="btn-bulk" on:click={() => bulkAction("enable")}>Enable</button>
      <button class="btn-bulk" on:click={() => bulkAction("disable")}>Disable</button>
      <button class="btn-bulk danger" on:click={() => bulkAction("delete")}>Delete</button>
      <button class="btn-bulk-cancel" on:click={() => selectedIds = new Set()}>Cancel</button>
    </div>
  {/if}

  {#if loading}
    <div class="empty-state">Loading...</div>
  {:else if $pipelines.length === 0}
    <div class="empty-state">
      <p>No pipelines yet.</p>
      <p class="hint">Create your first pipeline to get started.</p>
    </div>
  {:else}
    <div class="table">
      <div class="table-header">
        <span class="th-toggle"></span>
        <span class="th-name">Pipeline</span>
        <span class="th-runs">Runs</span>
        <span class="th-schedule">Schedule</span>
        <span class="th-lastrun">Last Run</span>
        <span class="th-nextrun">Next Run</span>
        <span class="th-nodes">Nodes</span>
        <span class="th-actions">Actions</span>
      </div>
      {#each $pipelines.filter(p => !searchQuery || p.name.toLowerCase().includes(searchQuery.toLowerCase()) || p.description.toLowerCase().includes(searchQuery.toLowerCase())) as pipeline}
        {@const lastRun = getLastRun(pipeline.id)}
        {@const counts = getRunCounts(pipeline.id)}
        {@const si = scheduleInfo.get(pipeline.id)}
        <div class="table-row" class:selected={selectedIds.has(pipeline.id)}>
          <!-- Toggle / enabled switch -->
          <span class="td-toggle">
            <label class="switch" title={pipeline.enabled ? "Click to pause" : "Click to enable"}>
              <input type="checkbox" checked={pipeline.enabled} on:change|stopPropagation={() => toggleEnabled(pipeline)} />
              <span class="slider" class:on={pipeline.enabled}></span>
            </label>
          </span>

          <!-- Name -->
          <span class="td-name">
            <a href="#/pipelines/{pipeline.id}" class="pipe-link">{pipeline.name}</a>
            {#if pipeline.tags?.length > 0}
              <span class="tag-list">
                {#each pipeline.tags as tag}
                  <span class="tag">{tag}</span>
                {/each}
              </span>
            {/if}
          </span>

          <!-- Run status circles with hover info -->
          <span class="td-runs">
            <span class="status-circles">
              <span class="circle circle-ok" class:has={counts.success > 0}>
                {counts.success || ""}
                <span class="circle-tip">{counts.success} succeeded</span>
              </span>
              <span class="circle circle-fail" class:has={counts.failed > 0}>
                {counts.failed || ""}
                <span class="circle-tip">{counts.failed} failed</span>
              </span>
              <span class="circle circle-run" class:has={counts.running > 0}>
                {counts.running || ""}
                <span class="circle-tip">{counts.running} running</span>
              </span>
            </span>
            {#if counts.total > 0}
              <a href="#/pipelines/{pipeline.id}/runs" class="runs-link" title="View all {counts.total} runs">{counts.total} total</a>
            {/if}
          </span>

          <!-- Schedule -->
          <span class="td-schedule">
            <span class="mono">{formatSchedule(pipeline.schedule)}</span>
          </span>

          <!-- Last Run timestamp -->
          <span class="td-lastrun">
            {#if lastRun?.started_at}
              <span class="ts" class:ts-ok={lastRun.status === "success"} class:ts-fail={lastRun.status === "failed"}>
                {new Date(lastRun.started_at).toLocaleDateString("en-US", { month: "short", day: "numeric" })},
                {new Date(lastRun.started_at).toLocaleTimeString("en-US", { hour: "2-digit", minute: "2-digit", hour12: false })}
              </span>
            {:else}
              <span class="ts-none">—</span>
            {/if}
          </span>

          <!-- Next Run -->
          <span class="td-nextrun">
            {#if si?.next_run}
              <span class="ts">
                {new Date(si.next_run).toLocaleDateString("en-US", { month: "short", day: "numeric" })},
                {new Date(si.next_run).toLocaleTimeString("en-US", { hour: "2-digit", minute: "2-digit", hour12: false })}
              </span>
            {:else}
              <span class="ts-none">—</span>
            {/if}
          </span>

          <!-- Node count -->
          <span class="td-nodes mono">{pipeline.nodes.length}</span>

          <!-- Actions -->
          <span class="td-actions">
            <button class="act-btn" title="Trigger run" on:click|stopPropagation={() => triggerRun(pipeline.id)}>
              <svg width="14" height="14" viewBox="0 0 24 24" fill="none"><path d={icons.play.d} fill="currentColor" /></svg>
            </button>
            <button class="act-btn act-danger" title="Delete" on:click|stopPropagation={() => { deleteTargetId = pipeline.id; deleteTargetName = pipeline.name; confirmDelete = true; }}>
              <svg width="14" height="14" viewBox="0 0 24 24" fill="none"><path d={icons.trash.d} stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" /></svg>
            </button>
            <button class="act-btn" title="More" on:click|stopPropagation={() => clonePipeline(pipeline.id)}>···</button>
          </span>
        </div>
      {/each}
    </div>
  {/if}

  {#if showCreateModal}
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <div class="modal-overlay" on:click={() => (showCreateModal = false)} on:keydown={() => {}}>
      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <div class="modal modal-wide" on:click|stopPropagation on:keydown={() => {}}>
        <h2>New Pipeline</h2>

        <div class="template-grid">
          {#each templates as tmpl, i}
            <!-- svelte-ignore a11y_no_static_element_interactions -->
            <div
              class="template-card"
              class:active={selectedTemplate === i}
              on:click={() => selectedTemplate = i}
              on:keydown={() => {}}
            >
              <span class="template-name">{tmpl.name}</span>
              <span class="template-desc">{tmpl.description}</span>
              <span class="template-meta">{tmpl.nodes.length} nodes</span>
            </div>
          {/each}
        </div>

        <div class="form-group">
          <label for="name">Name</label>
          <input id="name" bind:value={newName} placeholder="my-pipeline" />
        </div>
        <div class="form-group">
          <label for="desc">Description</label>
          <input id="desc" bind:value={newDescription} placeholder="Optional description" />
        </div>
        <div class="modal-actions">
          <button class="btn-secondary" on:click={() => (showCreateModal = false)}>Cancel</button>
          <button class="btn-primary" on:click={createFromTemplate}>Create</button>
        </div>
      </div>
    </div>
  {/if}
</div>

<ConfirmDialog
  bind:visible={confirmDelete}
  title="Delete Pipeline"
  message="Are you sure you want to delete '{deleteTargetName}'? This will also delete all runs, logs, and previews."
  confirmLabel="Delete"
  destructive={true}
  on:confirm={() => deletePipeline(deleteTargetId)}
/>

<style>
  .page-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: var(--space-xl);
  }
  .page-header h1 {
    font-size: 1.5rem;
    font-weight: 600;
    letter-spacing: -0.02em;
  }
  .header-actions {
    display: flex;
    gap: var(--space-sm);
    align-items: center;
  }

  .search-bar {
    display: flex;
    align-items: center;
    gap: var(--space-sm);
    padding: var(--space-sm) var(--space-md);
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    margin-bottom: var(--space-md);
    color: var(--text-muted);
  }
  .search-input {
    flex: 1;
    border: none;
    background: transparent;
    font-size: 0.875rem;
    padding: var(--space-xs) 0;
    outline: none;
  }

  .btn-primary {
    background: var(--accent);
    color: white;
    padding: var(--space-sm) var(--space-md);
    border-radius: var(--radius-md);
    font-weight: 500;
    transition: background var(--transition-fast);
  }
  .btn-primary:hover { background: var(--accent-hover); }

  .btn-secondary {
    background: var(--bg-tertiary);
    color: var(--text-secondary);
    padding: var(--space-sm) var(--space-md);
    border-radius: var(--radius-md);
    font-weight: 500;
    transition: background var(--transition-fast);
  }
  .btn-secondary:hover { background: var(--border); }

  .btn-icon {
    padding: 4px 8px;
    border-radius: var(--radius-sm);
    font-size: 0.75rem;
    transition: background var(--transition-fast);
  }
  .btn-icon:hover { background: var(--bg-tertiary); }
  .btn-icon.danger:hover { background: var(--failed-bg); color: var(--failed); }

  /* ── Airflow-style pipeline table ── */
  .table {
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    overflow: hidden;
  }
  .table-header, .table-row {
    display: grid;
    grid-template-columns: 42px 1fr 160px 100px 130px 130px 50px 90px;
    align-items: center;
    padding: 0 12px;
    min-height: 42px;
  }
  .table-header {
    background: var(--bg-tertiary);
    font-size: 10px; color: var(--text-muted);
    text-transform: uppercase; letter-spacing: 0.08em; font-weight: 600;
    border-bottom: 1px solid var(--border);
    min-height: 36px;
  }
  .table-row {
    border-bottom: 1px solid var(--border-subtle);
    transition: background 150ms ease;
  }
  .table-row:last-child { border-bottom: none; }
  .table-row:hover { background: var(--bg-secondary); }
  .table-row.selected { background: var(--accent-glow); }

  /* Toggle switch */
  .td-toggle, .th-toggle { display: flex; align-items: center; justify-content: center; }
  .switch { position: relative; width: 28px; height: 16px; cursor: pointer; }
  .switch input { opacity: 0; width: 0; height: 0; }
  .slider {
    position: absolute; inset: 0;
    background: var(--bg-tertiary); border-radius: 8px;
    border: 1px solid var(--border);
    transition: all 200ms ease;
  }
  .slider::after {
    content: ""; position: absolute;
    width: 10px; height: 10px; border-radius: 50%;
    background: var(--text-dim);
    top: 2px; left: 2px;
    transition: all 200ms ease;
  }
  .slider.on { background: rgba(59, 130, 246, 0.15); border-color: #3b82f6; }
  .slider.on::after { transform: translateX(12px); background: #3b82f6; }

  /* Name */
  .td-name { min-width: 0; padding: 6px 0; }
  .pipe-link {
    font-weight: 600; font-size: 13px; display: block;
    white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
    color: var(--accent-text);
  }
  .pipe-link:hover { text-decoration: underline; }
  .tag-list { display: inline-flex; gap: 3px; margin-left: 6px; vertical-align: middle; }
  .tag {
    font-size: 9px; padding: 1px 5px; border-radius: 3px;
    background: var(--accent-glow); color: var(--accent-text);
    font-family: var(--font-mono);
  }

  /* Run status circles (Airflow-style) */
  .td-runs { display: flex; align-items: center; }
  .status-circles { display: flex; gap: 6px; }
  .circle {
    width: 24px; height: 24px; border-radius: 50%;
    display: flex; align-items: center; justify-content: center;
    font-family: var(--font-mono); font-size: 10px; font-weight: 700;
    border: 1.5px solid var(--border);
    color: var(--text-ghost);
    transition: all 150ms ease;
  }
  .circle.has { border-width: 2px; }
  .circle.circle-ok.has { border-color: var(--success); color: var(--success); background: var(--success-bg); cursor: pointer; }
  .circle.circle-fail.has { border-color: var(--failed); color: var(--failed); background: var(--failed-bg); cursor: pointer; }
  .circle.circle-run.has { border-color: var(--running); color: var(--running); background: var(--running-bg); cursor: pointer; }

  /* Circle tooltip */
  .circle { position: relative; }
  .circle-tip {
    display: none;
    position: absolute; bottom: calc(100% + 6px); left: 50%;
    transform: translateX(-50%);
    background: var(--bg-primary); border: 1px solid var(--border);
    color: var(--text-primary);
    padding: 4px 8px; border-radius: 4px;
    font-size: 10px; font-weight: 500; white-space: nowrap;
    box-shadow: 0 2px 8px rgba(0,0,0,0.15);
    z-index: 10;
    pointer-events: none;
  }
  .circle-tip::after {
    content: ""; position: absolute;
    top: 100%; left: 50%; transform: translateX(-50%);
    border: 4px solid transparent;
    border-top-color: var(--border);
  }
  .circle:hover .circle-tip { display: block; }

  .td-runs { display: flex; align-items: center; gap: 8px; }
  .runs-link {
    font-size: 9px; color: var(--text-dim); text-decoration: none;
    font-family: var(--font-mono);
    transition: color 150ms ease;
  }
  .runs-link:hover { color: var(--accent-text); text-decoration: underline; }

  /* Schedule, timestamps */
  .td-schedule, .td-lastrun, .td-nextrun { font-size: 12px; }
  .mono { font-family: var(--font-mono); font-size: 11px; color: var(--text-muted); }
  .ts { font-family: var(--font-mono); font-size: 11px; color: var(--text-secondary); }
  .ts-ok { color: var(--success); }
  .ts-fail { color: var(--failed); }
  .ts-none { color: var(--text-ghost); font-size: 11px; }

  /* Node count */
  .td-nodes { text-align: center; font-size: 12px; }

  /* Actions */
  .td-actions, .th-actions { display: flex; gap: 4px; justify-content: flex-end; }
  .act-btn {
    width: 28px; height: 28px; display: flex; align-items: center; justify-content: center;
    border-radius: 4px; color: var(--text-muted); font-size: 12px;
    transition: all 150ms ease;
  }
  .act-btn:hover { color: var(--text-primary); background: var(--bg-tertiary); }
  .act-danger:hover { color: var(--failed); background: var(--failed-bg); }

  .empty-state {
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    padding: var(--space-xl) var(--space-xl) var(--space-lg);
    text-align: center;
    color: var(--text-secondary);
  }
  .hint { color: var(--text-muted); font-size: 0.875rem; margin-top: var(--space-xs); }

  .modal-overlay {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.6);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 100;
  }
  .modal {
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    padding: var(--space-xl);
    width: 400px;
    max-width: 90vw;
  }
  .modal h2 {
    font-size: 1.125rem;
    margin-bottom: var(--space-lg);
  }
  .form-group {
    margin-bottom: var(--space-md);
  }
  .form-group label {
    display: block;
    font-size: 0.75rem;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.08em;
    margin-bottom: var(--space-xs);
  }
  .form-group input {
    width: 100%;
  }
  .modal-actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--space-sm);
    margin-top: var(--space-lg);
  }
  .next-run {
    display: block;
    font-size: 9px; color: var(--accent-text);
    margin-top: 1px;
  }

  .col-check { flex: 0 0 28px; display: flex; align-items: center; }
  .col-check input[type="checkbox"] { width: 14px; height: 14px; accent-color: var(--accent); }
  .table-row.selected { background: var(--accent-glow); }

  .bulk-bar {
    display: flex; align-items: center; gap: 8px;
    padding: 8px 14px; background: var(--accent-glow);
    border: 1px solid rgba(99,102,241,0.2); border-radius: var(--radius-md);
    margin-bottom: var(--space-sm);
  }
  .bulk-count { font-size: 12px; font-weight: 600; color: var(--accent-text); margin-right: 4px; }
  .bulk-bar .btn-bulk {
    padding: 4px 10px; border-radius: 4px; font-size: 11px; font-weight: 500;
    background: var(--bg-secondary); border: 1px solid var(--border);
    color: var(--text-secondary); transition: all 150ms ease;
  }
  .bulk-bar .btn-bulk:hover { background: var(--bg-tertiary); color: var(--text-primary); }
  .bulk-bar .btn-bulk.danger { color: var(--failed); border-color: rgba(239,68,68,0.3); }
  .bulk-bar .btn-bulk.danger:hover { background: var(--failed-bg); }
  .btn-bulk-cancel {
    margin-left: auto; font-size: 11px; color: var(--text-muted);
    padding: 4px 8px; border-radius: 4px; transition: all 150ms ease;
  }
  .btn-bulk-cancel:hover { color: var(--text-primary); background: var(--bg-tertiary); }

  .tag-list { display: flex; gap: 3px; margin-top: 2px; flex-wrap: wrap; }
  .tag {
    font-size: 9px; padding: 1px 6px; border-radius: 3px;
    background: var(--accent-glow); color: var(--accent-text);
    font-family: var(--font-mono); letter-spacing: 0.02em;
  }

  .modal-wide {
    width: 560px;
  }
  .template-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(140px, 1fr));
    gap: 8px;
    margin-bottom: var(--space-lg);
  }
  .template-card {
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding: 10px 12px;
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    cursor: pointer;
    transition: all 150ms ease;
  }
  .template-card:hover {
    border-color: var(--border-hover);
    background: var(--bg-tertiary);
  }
  .template-card.active {
    border-color: var(--accent);
    background: var(--accent-glow);
  }
  .template-name {
    font-size: 12px;
    font-weight: 600;
    color: var(--text-primary);
  }
  .template-desc {
    font-size: 10px;
    color: var(--text-muted);
    line-height: 1.3;
  }
  .template-meta {
    font-family: var(--font-mono);
    font-size: 9px;
    color: var(--text-dim);
    margin-top: 2px;
  }
</style>
