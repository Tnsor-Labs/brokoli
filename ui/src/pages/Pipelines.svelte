<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { api } from "../lib/api";
  import { pipelines, onWSEvent } from "../lib/stores";
  import { icons } from "../lib/icons";
  import { notify } from "../lib/toast";
  import StatusBadge from "../components/StatusBadge.svelte";
  import ConfirmDialog from "../components/ConfirmDialog.svelte";
  import DeletePipelineDialog from "../components/DeletePipelineDialog.svelte";
  import Pagination from "../components/Pagination.svelte";
  import Skeleton from "../components/Skeleton.svelte";
  import type { Pipeline, Run } from "../lib/types";

  let confirmDelete = false;
  let deleteTargetId = "";
  let deleteTargetName = "";
  let conflictDialogVisible = false;
  let conflictDependents: { id: string; name: string }[] = [];

  let loading = true;
  let pgPage = 1;
  let pgSize = 25;
  let pipelineRuns: Map<string, Run[]> = new Map();
  let scheduleInfo: Map<string, { next_run: string; schedule: string }> = new Map();
  let showCreateModal = false;
  let newName = "";
  let newDescription = "";
  let searchQuery = "";
  let statusFilter = "";
  let tagFilter = "";
  let sortBy = "name";

  // Collect all unique tags
  $: allTags = [...new Set($pipelines.flatMap((p: any) => p.tags || []))].sort();

  $: filteredPipelines = $pipelines
    .filter((p: any) => {
      // Text search
      if (searchQuery) {
        const s = searchQuery.toLowerCase();
        if (!p.name.toLowerCase().includes(s) && !(p.description || "").toLowerCase().includes(s) && !(p.tags || []).some((t: string) => t.toLowerCase().includes(s)))
          return false;
      }
      // Status filter
      if (statusFilter) {
        const runs = pipelineRuns.get(p.id) || [];
        const lastStatus = runs[0]?.status || runs[0]?.last_run_status || "";
        if (statusFilter === "failed" && lastStatus !== "failed") return false;
        if (statusFilter === "success" && lastStatus !== "success" && lastStatus !== "completed") return false;
        if (statusFilter === "running" && lastStatus !== "running") return false;
        if (statusFilter === "paused" && p.enabled !== false) return false;
        if (statusFilter === "never" && lastStatus) return false;
      }
      // Tag filter
      if (tagFilter && !(p.tags || []).includes(tagFilter)) return false;
      return true;
    })
    .sort((a: any, b: any) => {
      if (sortBy === "name") return a.name.localeCompare(b.name);
      if (sortBy === "last_run") return (b.last_run_at || "").localeCompare(a.last_run_at || "");
      if (sortBy === "nodes") return (b.node_count || 0) - (a.node_count || 0);
      return 0;
    });

  $: paginatedPipelines = filteredPipelines.slice((pgPage - 1) * pgSize, pgPage * pgSize);
  $: if (searchQuery || statusFilter || tagFilter) pgPage = 1;
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

  let unsubWS: (() => void) | null = null;

  onMount(async () => {
    await loadPipelines();

    // Listen for real-time run events — update status inline (no API call)
    unsubWS = onWSEvent((event) => {
      if ((event.type === "run.completed" || event.type === "run.failed" || event.type === "run.started") && event.pipeline_id) {
        const status = event.status || (event.type === "run.completed" ? "success" : event.type === "run.failed" ? "failed" : "running");
        pipelineRuns.set(event.pipeline_id, [{
          id: event.run_id, pipeline_id: event.pipeline_id, status,
          started_at: event.timestamp, finished_at: null, node_runs: [],
        }]);
        pipelineRuns = new Map(pipelineRuns);
      }
    });
  });

  onDestroy(() => {
    if (unsubWS) unsubWS();
  });

  async function loadPipelines() {
    loading = true;
    try {
      // Single request: pipelines + last run status + run counts
      const [summaryRes, schedRes] = await Promise.all([
        fetch("/api/pipelines/summary", { headers: { ...authHeaders(), "X-Workspace-ID": localStorage.getItem("brokoli-workspace") || "default" } }),
        fetch("/api/scheduler/status", { headers: authHeaders() }),
      ]);

      if (summaryRes.ok) {
        const list = await summaryRes.json();
        pipelines.set(list);

        // Build run map from embedded data (no extra requests)
        for (const p of list) {
          if (p.last_run_status) {
            pipelineRuns.set(p.id, [{
              id: "", pipeline_id: p.id, status: p.last_run_status,
              started_at: p.last_run_at, finished_at: null, node_runs: [],
              _total: p.runs_total, _success: p.runs_success,
              _failed: p.runs_failed, _running: p.runs_running,
            }]);
          }
        }
        pipelineRuns = new Map(pipelineRuns);
      }

      if (schedRes.ok) {
        const schedData = await schedRes.json();
        for (const s of schedData) {
          scheduleInfo.set(s.pipeline_id, { next_run: s.next_run, schedule: s.schedule });
        }
        scheduleInfo = new Map(scheduleInfo);
      }
    } catch (e) {
      notify.error("Failed to load pipelines");
    } finally {
      loading = false;
    }
  }

  async function toggleEnabled(pipeline: any) {
    const newEnabled = !pipeline.enabled;
    try {
      // Fetch full pipeline first to avoid overwriting nodes/edges with empty data
      const full = await api.pipelines.get(pipeline.id);
      full.enabled = newEnabled;
      await api.pipelines.update(pipeline.id, full);
      pipelines.update(list => list.map(p => p.id === pipeline.id ? { ...p, enabled: newEnabled } : p));
      notify.success(newEnabled ? `${pipeline.name} enabled` : `${pipeline.name} paused`);
    } catch {
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
      notify.success("Run triggered");
      // Update just this pipeline's runs without reloading everything
      const runs = await api.runs.listByPipeline(pipelineId);
      pipelineRuns.set(pipelineId, runs);
      pipelineRuns = new Map(pipelineRuns);
    } catch (e: any) {
      notify.error("Failed to trigger run: " + (e.message || e));
    }
  }

  async function deletePipeline(id: string, resolve?: "cascade" | "decouple") {
    try {
      await api.pipelines.delete(id, resolve);
      if (resolve === "cascade") {
        // Also drop any cascaded dependents from the local list.
        const cascadedIds = new Set([id, ...conflictDependents.map(d => d.id)]);
        pipelines.update(list => list.filter(p => !cascadedIds.has(p.id)));
        notify.success(`Deleted pipeline and ${conflictDependents.length} dependent(s)`);
      } else {
        pipelines.update(list => list.filter(p => p.id !== id));
        notify.success("Pipeline deleted");
      }
      conflictDependents = [];
    } catch (e: any) {
      if (e.status === 409 && e.body?.dependents) {
        conflictDependents = e.body.dependents;
        conflictDialogVisible = true;
        return;
      }
      notify.error("Failed to delete pipeline: " + (e.message || e));
    }
  }

  function handleConflictResolve(e: CustomEvent<{ mode: "cascade" | "decouple" }>) {
    deletePipeline(deleteTargetId, e.detail.mode);
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
    // Use pre-computed counts from summary endpoint if available
    if (runs.length > 0 && runs[0]._total !== undefined) {
      return {
        success: runs[0]._success || 0,
        failed: runs[0]._failed || 0,
        running: runs[0]._running || 0,
        total: runs[0]._total || 0,
      };
    }
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
      const isJSON = file.name.endsWith(".json");
      const res = await fetch("/api/pipelines/import", {
        method: "POST",
        headers: {
          "Content-Type": isJSON ? "application/json" : "application/x-yaml",
          ...authHeaders(),
        },
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

  // Pipeline templates — use sample data so they work out of the box
  const templates = [
    {
      name: "Blank",
      description: "Start from scratch",
      icon: "plus",
      nodes: [] as any[],
      edges: [] as any[],
    },
    {
      name: "Hello World",
      description: "Minimal: fetch, transform, save",
      icon: "file",
      nodes: [
        { id: "s1", type: "source_api", name: "Fetch Employees", config: { url: "/api/samples/data/employees.csv", method: "GET" }, position: { x: 40, y: 120 } },
        { id: "t1", type: "transform", name: "Add Column", config: { rules: [{ type: "add_column", name: "greeting", expression: "'Hello, ' + name" }] }, position: { x: 360, y: 120 } },
        { id: "o1", type: "sink_file", name: "Save Result", config: { path: "/tmp/hello-output.csv" }, position: { x: 680, y: 120 } },
      ],
      edges: [{ from: "s1", to: "t1" }, { from: "t1", to: "o1" }],
    },
    {
      name: "API Fetch",
      description: "Fetch, filter, save",
      icon: "api",
      nodes: [
        { id: "s1", type: "source_api", name: "Fetch Orders", config: { url: "/api/samples/data/orders.csv", method: "GET" }, position: { x: 40, y: 120 } },
        { id: "t1", type: "transform", name: "Filter Completed", config: { rules: [{ type: "filter", column: "status", operator: "equals", value: "completed" }] }, position: { x: 360, y: 120 } },
        { id: "o1", type: "sink_file", name: "Save Orders", config: { path: "/tmp/completed-orders.csv" }, position: { x: 680, y: 120 } },
      ],
      edges: [{ from: "s1", to: "t1" }, { from: "t1", to: "o1" }],
    },
    {
      name: "Join + Aggregate",
      description: "Join two sources, aggregate results",
      icon: "merge",
      nodes: [
        { id: "s1", type: "source_api", name: "Orders", config: { url: "/api/samples/data/orders.csv", method: "GET" }, position: { x: 40, y: 60 } },
        { id: "s2", type: "source_api", name: "Products", config: { url: "/api/samples/data/products.csv", method: "GET" }, position: { x: 40, y: 220 } },
        { id: "j1", type: "join", name: "Join", config: { join_type: "inner", left_key: "product", right_key: "name" }, position: { x: 360, y: 140 } },
        { id: "t1", type: "transform", name: "Aggregate", config: { rules: [{ type: "aggregate", group_by: ["product"], agg_fields: [{ column: "total", function: "sum", alias: "total_revenue" }] }] }, position: { x: 680, y: 140 } },
        { id: "o1", type: "sink_file", name: "Summary", config: { path: "/tmp/product-summary.csv" }, position: { x: 1000, y: 140 } },
      ],
      edges: [{ from: "s1", to: "j1" }, { from: "s2", to: "j1" }, { from: "j1", to: "t1" }, { from: "t1", to: "o1" }],
    },
    {
      name: "Data Quality",
      description: "Validate data with quality gates",
      icon: "code",
      nodes: [
        { id: "s1", type: "source_api", name: "Fetch Employees", config: { url: "/api/samples/data/employees.csv", method: "GET" }, position: { x: 40, y: 120 } },
        { id: "q1", type: "quality_check", name: "Quality Gate", config: { rules: [{ column: "email", check: "not_null", policy: "block" }, { column: "salary", check: "positive", policy: "warn" }] }, position: { x: 360, y: 120 } },
        { id: "t1", type: "transform", name: "Clean Data", config: { rules: [{ type: "rename", old_name: "hire_date", new_name: "start_date" }] }, position: { x: 680, y: 120 } },
        { id: "o1", type: "sink_file", name: "Output", config: { path: "/tmp/clean-employees.csv" }, position: { x: 1000, y: 120 } },
      ],
      edges: [{ from: "s1", to: "q1" }, { from: "q1", to: "t1" }, { from: "t1", to: "o1" }],
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
      pipelines.update(list => [clone, ...list]);
      notify.success(`Cloned as "${clone.name}"`);
    } catch {
      notify.error("Failed to clone pipeline");
    }
  }

  async function exportYaml(id: string, name: string) {
    try {
      const res = await fetch(`/api/pipelines/${id}/export`, { headers: authHeaders() });
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
  <input type="file" accept=".yaml,.yml,.json" bind:this={fileInput} on:change={handleFileUpload} style="display:none" />

  <header class="page-header">
    <h1>Pipelines</h1>
    <div class="header-actions">
      <button class="btn-secondary" on:click={importYaml}>Import</button>
      <button class="btn-primary" on:click={() => (showCreateModal = true)}>
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none"><path d={icons.plus.d} stroke="currentColor" stroke-width="2" stroke-linecap="round" /></svg>
        New Pipeline
      </button>
    </div>
  </header>

  <div class="filter-bar">
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
      <span class="search-hint">Ctrl+K</span>
    </div>
    <div class="filter-controls">
      <select class="filter-select" bind:value={statusFilter}>
        <option value="">All Status</option>
        <option value="success">Succeeded</option>
        <option value="failed">Failed</option>
        <option value="running">Running</option>
        <option value="paused">Paused</option>
        <option value="never">Never Run</option>
      </select>
      {#if allTags.length > 0}
        <select class="filter-select" bind:value={tagFilter}>
          <option value="">All Tags</option>
          {#each allTags as tag}
            <option value={tag}>{tag}</option>
          {/each}
        </select>
      {/if}
      <select class="filter-select" bind:value={sortBy}>
        <option value="name">Sort: Name</option>
        <option value="last_run">Sort: Last Run</option>
        <option value="nodes">Sort: Nodes</option>
      </select>
      <span class="filter-count">{filteredPipelines.length} pipeline{filteredPipelines.length !== 1 ? "s" : ""}</span>
    </div>
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
    <div class="skeleton-rows">
      {#each Array(5) as _}
        <Skeleton height="48px" width="100%" />
      {/each}
    </div>
  {:else if $pipelines.length === 0}
    <div class="empty-hero">
      <h2>Build your first pipeline</h2>
      <p class="empty-hero-sub">Choose a template to get started quickly, or start from scratch.</p>
      <div class="template-grid">
        {#each templates as tmpl, i}
          <button class="template-card" on:click={() => { selectedTemplate = i; showCreateModal = true; }}>
            <div class="tmpl-icon">
              {#if tmpl.icon === "plus"}
                <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5"><line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/></svg>
              {:else if tmpl.icon === "file"}
                <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><polyline points="14 2 14 8 20 8"/></svg>
              {:else if tmpl.icon === "api"}
                <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5"><circle cx="12" cy="12" r="10"/><line x1="2" y1="12" x2="22" y2="12"/><path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"/></svg>
              {:else if tmpl.icon === "merge"}
                <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5"><circle cx="18" cy="18" r="3"/><circle cx="6" cy="6" r="3"/><circle cx="6" cy="18" r="3"/><path d="M6 21v-4a6 6 0 0 1 12 0v4"/></svg>
              {:else}
                <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5"><polyline points="16 18 22 12 16 6"/><polyline points="8 6 2 12 8 18"/></svg>
              {/if}
            </div>
            <span class="tmpl-name">{tmpl.name}</span>
            <span class="tmpl-desc">{tmpl.description}</span>
          </button>
        {/each}
      </div>
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
      {#each paginatedPipelines as pipeline}
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
          <span class="td-nodes mono">{pipeline.node_count ?? pipeline.nodes?.length ?? 0}</span>

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
    <Pagination total={filteredPipelines.length} page={pgPage} pageSize={pgSize}
      on:page={(e) => pgPage = e.detail} on:pagesize={(e) => { pgSize = e.detail; pgPage = 1; }} />
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

<DeletePipelineDialog
  bind:visible={conflictDialogVisible}
  pipelineName={deleteTargetName}
  dependents={conflictDependents}
  on:resolve={handleConflictResolve}
  on:cancel={() => { conflictDependents = []; }}
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

  .filter-bar {
    display: flex; flex-direction: column; gap: 8px;
    margin-bottom: var(--space-md);
  }
  .search-bar {
    display: flex;
    align-items: center;
    gap: var(--space-sm);
    padding: var(--space-sm) var(--space-md);
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    color: var(--text-muted);
  }
  .search-hint {
    font-size: 10px; color: var(--text-ghost); font-family: var(--font-mono);
    padding: 2px 6px; border-radius: 4px;
    background: var(--bg-tertiary); border: 1px solid var(--border-subtle);
    flex-shrink: 0;
  }
  .filter-controls {
    display: flex; align-items: center; gap: 8px; flex-wrap: wrap;
  }
  .filter-select {
    padding: 5px 10px; border-radius: 6px; font-size: 12px;
    background: var(--bg-secondary); border: 1px solid var(--border);
    color: var(--text-secondary); font-family: var(--font-ui);
    cursor: pointer;
  }
  .filter-select:focus { border-color: var(--accent); outline: none; }
  .filter-count {
    font-size: 11px; color: var(--text-dim); font-family: var(--font-mono);
    margin-left: auto;
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
    border: 1px solid var(--border-subtle);
    border-radius: var(--radius-xl, 14px);
    overflow: hidden;
    box-shadow: var(--shadow-card);
  }
  .table-header, .table-row {
    display: grid;
    grid-template-columns: 42px 1fr 160px 100px 130px 130px 50px 90px;
    align-items: center;
    padding: 0 14px;
    min-height: 42px;
  }
  .table-header {
    background: transparent;
    font-size: 11px; color: var(--text-muted);
    text-transform: uppercase; letter-spacing: 0.06em; font-weight: 600;
    border-bottom: 2px solid var(--border-subtle);
    min-height: 38px;
  }
  .table-row {
    border-bottom: 1px solid var(--border-subtle);
    transition: background 150ms ease;
  }
  .table-row:last-child { border-bottom: none; }
  .table-row:hover { background: rgba(255, 255, 255, 0.02); }
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
  .slider.on { background: var(--accent-glow); border-color: var(--accent); }
  .slider.on::after { transform: translateX(12px); background: var(--accent); }

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
    width: 22px; height: 22px; border-radius: 50%;
    display: flex; align-items: center; justify-content: center;
    font-family: var(--font-mono); font-size: 10px; font-weight: 600;
    border: 1.5px solid var(--border-subtle);
    color: var(--text-ghost); background: none;
    transition: all 150ms ease;
  }
  .circle.has { cursor: pointer; }
  .circle.circle-ok.has { border-color: var(--success); color: var(--success); }
  .circle.circle-fail.has { border-color: var(--failed); color: var(--failed); }
  .circle.circle-run.has { border-color: var(--running); color: var(--running); }

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

  .skeleton-rows { display: flex; flex-direction: column; gap: 8px; }
  .empty-state {
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    padding: var(--space-xl) var(--space-xl) var(--space-lg);
    text-align: center;
    color: var(--text-secondary);
  }
  .hint { color: var(--text-muted); font-size: 0.875rem; margin-top: var(--space-xs); }

  /* Empty hero with template picker */
  .empty-hero {
    display: flex; flex-direction: column; align-items: center;
    text-align: center; padding: 48px 24px 40px;
    background: radial-gradient(ellipse at 50% 0%, rgba(13, 148, 136, 0.08) 0%, transparent 60%);
    border-radius: var(--radius-xl, 14px);
    margin: -8px -8px 0;
  }
  .empty-hero h2 { font-size: 1.5rem; font-weight: 700; margin-bottom: 8px; letter-spacing: -0.03em; }
  .empty-hero-sub { font-size: 14px; color: var(--text-muted); margin-bottom: 36px; }
  .template-grid {
    display: grid; grid-template-columns: repeat(3, 1fr);
    gap: 16px; width: 100%; max-width: 900px;
  }
  .template-card {
    display: flex; flex-direction: column; align-items: center; gap: 10px;
    padding: 40px 24px 32px;
    background: var(--bg-secondary); border: 1px solid var(--border-subtle);
    border-radius: var(--radius-xl, 14px); cursor: pointer; color: inherit;
    transition: all 250ms cubic-bezier(0.16, 1, 0.3, 1);
    box-shadow: var(--shadow-card);
  }
  .template-card:hover {
    border-color: var(--accent);
    transform: translateY(-3px);
    box-shadow: var(--shadow-card-hover), 0 0 20px var(--accent-glow);
  }
  .template-card.active {
    border-color: var(--accent); background: var(--accent-glow);
  }
  .tmpl-icon { color: var(--accent); }
  .tmpl-name { font-size: 14px; font-weight: 600; }
  .tmpl-desc { font-size: 11.5px; color: var(--text-muted); line-height: 1.5; }
  @media (max-width: 768px) {
    .template-grid { grid-template-columns: repeat(2, 1fr); }
  }

  .modal-overlay {
    position: fixed; inset: 0;
    background: rgba(0, 0, 0, 0.7); backdrop-filter: blur(4px);
    display: flex; align-items: center; justify-content: center; z-index: 100;
    animation: overlay-in 150ms ease;
  }
  @keyframes overlay-in { from { opacity: 0; } to { opacity: 1; } }
  .modal {
    background: var(--bg-secondary); border: 1px solid var(--border);
    border-radius: var(--radius-xl, 14px); padding: 28px 32px;
    width: 480px; max-width: 90vw;
    box-shadow: 0 16px 48px rgba(0, 0, 0, 0.4);
    animation: modal-in 200ms cubic-bezier(0.16, 1, 0.3, 1);
  }
  .modal-wide { width: 580px; }
  @keyframes modal-in {
    from { opacity: 0; transform: scale(0.96) translateY(8px); }
    to { opacity: 1; transform: scale(1) translateY(0); }
  }
  .modal h2 { font-size: 1.2rem; font-weight: 600; margin-bottom: 20px; letter-spacing: -0.01em; }
  .form-group { margin-bottom: 16px; }
  .form-group label {
    display: block; font-size: 11px; color: var(--text-muted);
    text-transform: uppercase; letter-spacing: 0.06em; margin-bottom: 6px; font-weight: 500;
  }
  .form-group input { width: 100%; }
  .modal-actions {
    display: flex; justify-content: flex-end; gap: var(--space-sm); margin-top: 20px;
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

  @media (max-width: 768px) {
    .page-header { flex-wrap: wrap; gap: 8px; }
    .search-bar { width: 100%; }
    .table-header { display: none; }
    .table-row { display: flex; flex-wrap: wrap; gap: 4px; padding: 10px; }
    .td-name { flex: 1; min-width: 60%; }
    .td-schedule, .td-nodes, .td-runs, .td-next { font-size: 10px; }
  }
</style>
