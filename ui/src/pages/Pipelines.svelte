<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { api } from "../lib/api";
  import { pipelines } from "../lib/stores";
  import { getSodpClient } from "../lib/sodp";
  import { dashboardKey } from "../lib/auth";
  import { icons } from "../lib/icons";
  import { notify } from "../lib/toast";
  import ConfirmDialog from "../components/ConfirmDialog.svelte";
  import DeletePipelineDialog from "../components/DeletePipelineDialog.svelte";
  import Skeleton from "../components/Skeleton.svelte";
  import type { Pipeline, PipelineTemplate, Run } from "../lib/types";
  type SummaryRun = Run & { last_run_status?: string; _total?: number; _success?: number; _failed?: number; _running?: number };

  let confirmDelete = false;
  let deleteTargetId = "";
  let deleteTargetName = "";
  let conflictDialogVisible = false;
  let conflictDependents: { id: string; name: string }[] = [];

  let loading = true;
  let pgPage = 1;
  let pgSize = 25;
  let pipelineRuns: Map<string, SummaryRun[]> = new Map();
  let scheduleInfo: Map<string, { next_run: string; schedule: string }> = new Map();
  let showCreateModal = false;
  let newName = "";
  let newDescription = "";
  let searchQuery = "";
  let statusFilter = "";
  let tagFilter = "";
  let sortBy = "name";
  let density: "compact" | "comfortable" = "compact";
  let openMenuId: string | null = null;
  let actionMenuPosition = { top: 0, left: 0 };

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
        if (p.enabled === false && statusFilter !== "paused") return false;
        if (p.enabled !== false && statusFilter === "paused") return false;
        const runs = pipelineRuns.get(p.id) || [];
        const lastStatus = String(runs[0]?.status || runs[0]?.last_run_status || "");
        if (statusFilter === "failed" && lastStatus !== "failed") return false;
        if (statusFilter === "success" && lastStatus !== "success" && lastStatus !== "completed") return false;
        if (statusFilter === "running" && lastStatus !== "running") return false;
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
  $: totalPages = Math.max(1, Math.ceil(filteredPipelines.length / pgSize));
  $: if (searchQuery || statusFilter || tagFilter) pgPage = 1;
  $: healthCounts = $pipelines.reduce((counts: any, p: any) => {
    counts.all++;
    if (p.enabled === false) { counts.paused++; return counts; }
    counts.enabled++;
    const status = pipelineRuns.get(p.id)?.[0]?.status || p.last_run_status;
    if (status === "success" || status === "completed") counts.success++;
    if (status === "failed") counts.failed++;
    if (status === "running") counts.running++;
    return counts;
  }, { all: 0, enabled: 0, success: 0, failed: 0, running: 0, paused: 0 });
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
    const pageIds = paginatedPipelines.map((p) => p.id);
    if (pageIds.every((id) => selectedIds.has(id))) {
      selectedIds = new Set([...selectedIds].filter((id) => !pageIds.includes(id)));
    } else {
      selectedIds = new Set([...selectedIds, ...pageIds]);
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
      notify.success(`${action}: ${data.succeeded ?? 0} pipelines updated`);
      selectedIds = new Set();
      await loadPipelines();
    } catch {
      notify.error("Bulk operation failed");
    }
  }

  let unsubDashboard: (() => void) | null = null;
  // Debounce timer so back-to-back snapshot updates collapse into a single
  // REST refetch instead of triggering an avalanche of GET /pipelines/summary.
  let summaryReloadTimer: ReturnType<typeof setTimeout> | null = null;

  onMount(async () => {
    try {
      const saved = JSON.parse(localStorage.getItem("brokoli-pipeline-view") || "null");
      if (saved) {
        if (["", "enabled", "paused", "success", "failed", "running", "never"].includes(saved.statusFilter)) statusFilter = saved.statusFilter;
        if (["name", "last_run", "nodes"].includes(saved.sortBy)) sortBy = saved.sortBy;
        if (["compact", "comfortable"].includes(saved.density)) density = saved.density;
        if (typeof saved.tagFilter === "string") tagFilter = saved.tagFilter;
      }
    } catch { /* Ignore invalid saved views. */ }
    await Promise.all([loadPipelines(), loadTemplates()]);

    // Subscribe to the dashboard.{org} state key. Every time a run state
    // changes anywhere in this org, the bridge recomputes the snapshot, the
    // SODP server fans out a delta, and we get a callback. We don't read
    // anything FROM the snapshot here — we use it as a tripwire to refetch
    // /api/pipelines/summary, which has the authoritative per-pipeline
    // counters this page actually displays.
    //
    // The previous event-stream + in-place counter increment was complicated
    // and accumulated bugs (counter clobbering, duplicate increments on
    // reconnect, stale entries). Refetching from REST on every state change
    // is O(pipelines) per change but always correct, and `/api/pipelines/summary`
    // is cheap enough that this is fine for OSS workloads.
    const client = getSodpClient();
    let firstCallback = true;
    unsubDashboard = client.watch(dashboardKey(), () => {
      // Skip the very first callback — that's STATE_INIT, which fires
      // immediately with whatever's currently in the store. We already have
      // the data via loadPipelines() in onMount; refetching for the init
      // would be wasted work.
      if (firstCallback) {
        firstCallback = false;
        return;
      }
      // Debounce: collapse multi-event bursts (e.g. node.* events that don't
      // affect run-level state but might still trigger a snapshot rewrite
      // due to updated_at) into one refetch.
      if (summaryReloadTimer) clearTimeout(summaryReloadTimer);
      summaryReloadTimer = setTimeout(() => {
        // Silent: don't flash a skeleton on every state change.
        loadPipelines({ silent: true });
        summaryReloadTimer = null;
      }, 150);
    });
  });

  onDestroy(() => {
    if (unsubDashboard) unsubDashboard();
    if (summaryReloadTimer) clearTimeout(summaryReloadTimer);
  });

  async function loadPipelines(opts: { silent?: boolean } = {}) {
    // Silent mode: don't flip `loading` to true, so the existing list
    // stays rendered while the refetch happens in the background. Used
    // for SODP tripwire-driven refreshes, where flashing a skeleton
    // every time a run state changes looks like a forced page reload
    // and kills the whole point of realtime updates.
    if (!opts.silent) loading = true;
    try {
      // Single request: pipelines + last run status + run counts
      const [summaryRes, schedRes] = await Promise.all([
        fetch("/api/pipelines/summary", { headers: { ...authHeaders(), "X-Workspace-ID": localStorage.getItem("brokoli-workspace") || "default" } }),
        fetch("/api/scheduler/status", { headers: authHeaders() }),
      ]);

      if (summaryRes.ok) {
        const list = await summaryRes.json();
        pipelines.set(list);
        pipelineRuns = new Map();

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
        scheduleInfo = new Map();
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
      // Don't refetch the runs list here — the WS handler above will receive
      // run.started / run.completed / run.failed events and update the cached
      // counters in place. Refetching with api.runs.listByPipeline() returns
      // raw Run objects WITHOUT the _total/_success/_failed/_running fields,
      // which would clobber the cached summary counts and make getRunCounts()
      // fall back to counting array length.
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

  function handleConflictResolve(e: CustomEvent<{ mode: "abort" | "cascade" | "decouple" }>) {
    if (e.detail.mode === "abort") { conflictDialogVisible = false; conflictDependents = []; return; }
    deletePipeline(deleteTargetId, e.detail.mode);
  }

  function getLastRun(pipelineId: string): Run | undefined {
    return pipelineRuns.get(pipelineId)?.[0];
  }

  function formatSchedule(cron: string): string {
    if (!cron) return "Manual";
    return cron;
  }

  function statusLabel(pipeline: any): { label: string; tone: string } {
    if (pipeline.enabled === false) return { label: "Paused", tone: "paused" };
    const status = getLastRun(pipeline.id)?.status || pipeline.last_run_status;
    if (status === "success" || status === "completed") return { label: "Healthy", tone: "success" };
    if (status === "failed") return { label: "Failed", tone: "failed" };
    if (status === "running") return { label: "Running", tone: "running" };
    return { label: "Never run", tone: "neutral" };
  }

  function relativeTime(value?: string): string {
    if (!value) return "—";
    const seconds = Math.max(0, Math.floor((Date.now() - new Date(value).getTime()) / 1000));
    if (seconds < 60) return "just now";
    if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
    if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`;
    return `${Math.floor(seconds / 86400)}d ago`;
  }

  function saveView() {
    localStorage.setItem("brokoli-pipeline-view", JSON.stringify({ statusFilter, tagFilter, sortBy, density }));
    notify.success("Pipeline view saved");
  }

  function selectStatus(status: string) { statusFilter = statusFilter === status ? "" : status; }
  function handlePageKeydown(event: KeyboardEvent) { if (event.key === "Escape") openMenuId = null; }
  function toggleActionMenu(event: MouseEvent, pipelineId: string) {
    if (openMenuId === pipelineId) { openMenuId = null; return; }
    const rect = (event.currentTarget as HTMLElement).getBoundingClientRect();
    const menuHeight = 158;
    actionMenuPosition = {
      top: window.innerHeight - rect.bottom > menuHeight + 8 ? rect.bottom + 4 : rect.top - menuHeight - 4,
      left: Math.max(8, rect.right - 160),
    };
    openMenuId = pipelineId;
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

  // Pipeline templates — fetched from the backend (pkg/templates.Builtin)
  // rather than hardcoded here. Moved out of the frontend because a
  // hardcoded template's config could silently drift out of sync with
  // whatever shape the engine actually expects, with nothing to catch
  // it — see api/templates_test.go, which runs every one of these
  // through the real engine and would fail CI if that happened again.
  let templates: PipelineTemplate[] = [];
  let selectedTemplate = 0;

  async function loadTemplates() {
    try {
      templates = await api.templates.list();
    } catch {
      notify.error("Failed to load pipeline templates");
    }
  }

  async function createFromTemplate() {
    if (!newName.trim()) return;
    const tmpl = templates[selectedTemplate];
    if (!tmpl) { notify.error("Select a template before creating a pipeline"); return; }
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

<svelte:window on:keydown={handlePageKeydown} />

<div class="pipelines-page animate-in">
  <input type="file" accept=".yaml,.yml,.json" bind:this={fileInput} on:change={handleFileUpload} style="display:none" />

  <header class="page-header">
    <div><p class="eyebrow">Orchestration</p><h1>Pipelines</h1><p class="page-subtitle">Build, schedule, and monitor every data workflow.</p></div>
    <div class="header-actions">
      <button class="btn-secondary" on:click={importYaml}>Import</button>
      <button class="btn-primary" on:click={() => (showCreateModal = true)}>
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none"><path d={icons.plus.d} stroke="currentColor" stroke-width="2" stroke-linecap="round" /></svg>
        New Pipeline
      </button>
    </div>
  </header>

  <section class="health-strip" aria-label="Pipeline health filters">
    <button class:active={!statusFilter} on:click={() => (statusFilter = "")}><i class="metric-icon all">☷</i><span>All pipelines<strong>{healthCounts.all}</strong></span></button>
    <button class:active={statusFilter === "enabled"} on:click={() => selectStatus("enabled")}><i class="metric-icon enabled">✓</i><span>Enabled<strong>{healthCounts.enabled}</strong></span></button>
    <button class:active={statusFilter === "paused"} on:click={() => selectStatus("paused")}><i class="metric-icon paused">Ⅱ</i><span>Paused<strong>{healthCounts.paused}</strong></span></button>
    <button class:active={statusFilter === "failed"} on:click={() => selectStatus("failed")}><i class="metric-icon failed">×</i><span>Needs attention<strong>{healthCounts.failed}</strong></span></button>
  </section>

  <section class="inventory" aria-label="Pipeline inventory">
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
      <button class="toolbar-button" on:click={saveView}>▥ Save view</button>
      <select class="filter-select" bind:value={statusFilter}>
        <option value="">All Status</option>
        <option value="success">Succeeded</option>
        <option value="failed">Failed</option>
        <option value="running">Running</option>
        <option value="paused">Paused</option>
        <option value="never">Never Run</option>
        <option value="enabled">Enabled</option>
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
      <select class="filter-select" bind:value={density}>
        <option value="compact">☷ Compact</option>
        <option value="comfortable">☰ Comfortable</option>
      </select>
      <span class="filter-count">Showing {filteredPipelines.length} of {$pipelines.length}</span>
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
      <span class="empty-kicker">Start with a template</span>
      <h2>Build your first pipeline</h2>
      <p class="empty-hero-sub">Choose a production-ready pattern, name it, then tailor the nodes to your workflow.</p>
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
    <div class="table-scroll">
      <table class:comfortable={density === "comfortable"}>
        <thead><tr><th>Pipeline</th><th>Last result</th><th>Run history ⓘ</th><th>Schedule</th><th>Last run</th><th>Next run</th><th>Nodes</th><th>Actions</th></tr></thead>
        <tbody>
      {#each paginatedPipelines as pipeline}
        {@const lastRun = getLastRun(pipeline.id)}
        {@const si = scheduleInfo.get(pipeline.id)}
        {@const health = statusLabel(pipeline)}
        <tr class:selected={selectedIds.has(pipeline.id)}>
          <td class="pipeline-cell">
            <button class="enable-toggle" class:on={pipeline.enabled} role="switch" aria-checked={pipeline.enabled} aria-label={pipeline.enabled ? `Pause ${pipeline.name}` : `Enable ${pipeline.name}`} on:click={() => toggleEnabled(pipeline)}><i></i></button>
            <span class="pipeline-identity"><a href="#/pipelines/{pipeline.id}/edit">{pipeline.name}</a><small>{pipeline.description || pipeline.tags?.[0] || "Pipeline workflow"}</small></span>
          </td>
          <td><span class="result-badge {health.tone}">{health.tone === "success" ? "✓" : health.tone === "failed" ? "×" : health.tone === "running" ? "◷" : "Ⅱ"} {health.label}</span></td>
          <td><div class="run-bars" aria-label="Recent run history">{#each Array(5) as _, i}<i class={pipeline.run_history?.[i] || "never"}></i>{/each}</div></td>
          <td><span class:cron={pipeline.schedule} class="muted">{formatSchedule(pipeline.schedule)}</span></td>
          <td>{#if lastRun?.started_at}<span class="timestamp"><strong>{relativeTime(lastRun.started_at)}</strong><small>{new Date(lastRun.started_at).toLocaleString("en-US", { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" })}</small></span>{:else}<span class="muted">—<small>Never run</small></span>{/if}</td>
          <td><span class="muted">{si?.next_run ? formatNextRun(si.next_run) : "—"}</span></td>
          <td class="nodes-cell">{pipeline.node_count ?? pipeline.nodes?.length ?? 0}</td>
          <td class="row-actions">
            <button class="act-btn" title="Trigger run" disabled={pipeline.enabled === false || lastRun?.status === "running"} on:click|stopPropagation={() => triggerRun(pipeline.id)}>
              <svg width="14" height="14" viewBox="0 0 24 24" fill="none"><path d={icons.play.d} fill="currentColor" /></svg>
            </button>
            <div class="action-menu-wrap"><button class="act-btn" aria-label="More actions for {pipeline.name}" on:click|stopPropagation={(event) => toggleActionMenu(event, pipeline.id)}>···</button>
              {#if openMenuId === pipeline.id}<div class="action-menu" style:top="{actionMenuPosition.top}px" style:left="{actionMenuPosition.left}px">
                <button on:click={() => { toggleEnabled(pipeline); openMenuId = null; }}>{pipeline.enabled ? "Pause" : "Enable"}</button>
                <button on:click={() => { clonePipeline(pipeline.id); openMenuId = null; }}>Clone</button>
                <button on:click={() => { exportYaml(pipeline.id, pipeline.name); openMenuId = null; }}>Export YAML</button>
                <button class="danger" on:click={() => { deleteTargetId = pipeline.id; deleteTargetName = pipeline.name; confirmDelete = true; openMenuId = null; }}>Delete</button>
              </div>{/if}
            </div>
          </td>
        </tr>
      {/each}
        </tbody>
      </table>
    </div>
    <footer class="inventory-footer"><label>Rows per page <select bind:value={pgSize} on:change={() => (pgPage = 1)}><option value={15}>15</option><option value={25}>25</option><option value={50}>50</option></select></label><span>Showing {filteredPipelines.length} pipelines</span><button disabled={pgPage <= 1} on:click={() => pgPage--}>‹</button><strong>{pgPage}</strong><small>of {totalPages}</small><button disabled={pgPage >= totalPages} on:click={() => pgPage++}>›</button></footer>
  {/if}
  </section>

  {#if showCreateModal}
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <div class="modal-overlay" on:click={() => (showCreateModal = false)} on:keydown={() => {}}>
      <!-- svelte-ignore a11y_no_static_element_interactions -->
      <div class="modal modal-wide" on:click|stopPropagation on:keydown={() => {}}>
        <header class="create-header">
          <div class="create-heading"><span class="create-mark">+</span><div><h2>Create pipeline</h2><p>Choose a starting pattern and make it yours.</p></div></div>
          <button class="modal-close" aria-label="Close create pipeline dialog" on:click={() => (showCreateModal = false)}>×</button>
        </header>

        <div class="create-body">
          <fieldset class="template-picker">
            <legend>Choose a template</legend>
            <div class="template-grid">
              {#each templates as tmpl, i}
                <button type="button" class="template-card" class:active={selectedTemplate === i} aria-pressed={selectedTemplate === i} on:click={() => selectedTemplate = i}>
                  <span class="modal-template-icon">
                    {#if tmpl.icon === "api"}◎{:else if tmpl.icon === "merge"}⌘{:else if tmpl.icon === "code"}‹›{:else}▤{/if}
                  </span>
                  <span class="template-copy"><strong>{tmpl.name}</strong><small>{tmpl.description}</small></span>
                  <span class="template-meta">{tmpl.nodes.length} nodes</span>
                  {#if selectedTemplate === i}<span class="selected-check">✓</span>{/if}
                </button>
              {/each}
            </div>
          </fieldset>

          <div class="pipeline-details">
            <div class="details-heading"><span>Pipeline details</span><small>You can change these later.</small></div>
            <div class="form-group">
              <label for="name">Name</label>
              <input id="name" bind:value={newName} placeholder="e.g. daily-customer-sync" autocomplete="off" />
            </div>
            <div class="form-group">
              <label for="desc">Description <span>Optional</span></label>
              <input id="desc" bind:value={newDescription} placeholder="What does this pipeline do?" />
            </div>
          </div>
        </div>
        <footer class="modal-actions">
          <button class="btn-secondary" on:click={() => (showCreateModal = false)}>Cancel</button>
          <button class="btn-primary" on:click={createFromTemplate} disabled={!newName.trim()}>Create pipeline <span>→</span></button>
        </footer>
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

  .eyebrow { display: none; }
  .page-subtitle { margin-top: 3px; color: var(--text-muted); font-size: 13px; }
  .health-strip { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 12px; margin-bottom: 16px; }
  .health-strip button { display: grid; min-height: 80px; grid-template-columns: 40px 1fr; align-items: center; gap: 12px; padding: 14px 16px; border: 1px solid var(--border-subtle); border-radius: 8px; background: linear-gradient(135deg, var(--bg-secondary), color-mix(in srgb, var(--bg-secondary), black 5%)); color: var(--text-muted); text-align: left; }
  .health-strip button:hover, .health-strip button.active { border-color: color-mix(in srgb, var(--accent), transparent 45%); background: var(--bg-secondary); }
  .health-strip span { display: flex; align-items: flex-start; flex-direction: column; gap: 3px; font-size: 11px; }
  .health-strip strong { color: var(--text-primary); font-size: 20px; font-weight: 650; }
  .metric-icon { display: grid; width: 38px; height: 38px; place-items: center; border: 1px solid color-mix(in srgb, var(--accent), transparent 70%); border-radius: 50%; background: var(--accent-glow); color: var(--accent); font-style: normal; font-size: 18px; }
  .metric-icon.enabled { border-color: color-mix(in srgb, var(--success), transparent 70%); background: var(--success-bg); color: var(--success); }
  .metric-icon.paused { border-color: color-mix(in srgb, var(--warning), transparent 70%); background: var(--warning-bg); color: var(--warning); }
  .metric-icon.failed { border-color: color-mix(in srgb, var(--failed), transparent 70%); background: var(--failed-bg); color: var(--failed); }
  .inventory { display: flex; min-height: 610px; flex-direction: column; overflow: visible; border: 1px solid var(--border-subtle); border-radius: 9px; background: var(--bg-secondary); box-shadow: var(--shadow-card); }
  .inventory .filter-bar { display: grid; grid-template-columns: minmax(280px, 1fr) auto; align-items: center; gap: 10px; margin: 0; padding: 10px 12px; border-bottom: 1px solid var(--border-subtle); }
  .inventory .search-bar { padding: 6px 10px; border-radius: 6px; background: var(--bg-primary); }
  .inventory .filter-controls { flex-wrap: nowrap; }
  .toolbar-button { height: 32px; padding: 0 11px; border: 1px solid var(--border); border-radius: 6px; color: var(--text-secondary); font-size: 11px; white-space: nowrap; }
  .toolbar-button:hover { border-color: var(--accent); color: var(--accent); }
  .inventory .filter-select { height: 32px; padding: 0 28px 0 10px; }
  .inventory .filter-count { display: none; }
  .table-scroll { min-height: 0; overflow: auto; }
  .table-scroll table { width: 100%; min-width: 1120px; border-collapse: collapse; table-layout: fixed; }
  .table-scroll th { height: 42px; padding: 0 12px; border-bottom: 1px solid var(--border-subtle); color: var(--text-muted); font-size: 10px; font-weight: 500; letter-spacing: .04em; text-align: left; text-transform: uppercase; }
  .table-scroll th:nth-child(1) { width: 30%; padding-left: 16px; } .table-scroll th:nth-child(2) { width: 12%; } .table-scroll th:nth-child(3) { width: 16%; } .table-scroll th:nth-child(4) { width: 10%; } .table-scroll th:nth-child(5) { width: 12%; } .table-scroll th:nth-child(6) { width: 9%; } .table-scroll th:nth-child(7) { width: 5%; text-align: center; } .table-scroll th:nth-child(8) { width: 7%; text-align: center; }
  .table-scroll td { height: 44px; padding: 0 12px; border-bottom: 1px solid var(--border-subtle); color: var(--text-secondary); font-size: 11px; }
  .table-scroll table.comfortable td { height: 56px; }
  .table-scroll tr:hover td, .table-scroll tr.selected td { background: var(--bg-card-hover); }
  .pipeline-cell { display: flex; align-items: center; gap: 11px; padding-left: 16px !important; }
  .enable-toggle { position: relative; width: 30px; height: 17px; flex: none; border: 1px solid var(--border); border-radius: 999px; background: var(--bg-tertiary); }
  .enable-toggle i { position: absolute; top: 2px; left: 2px; width: 11px; height: 11px; border-radius: 50%; background: var(--text-dim); transition: transform 150ms ease; }
  .enable-toggle.on { border-color: var(--accent); background: var(--accent-glow-strong); } .enable-toggle.on i { background: var(--accent); transform: translateX(13px); }
  .pipeline-identity { display: flex; min-width: 0; flex-direction: column; gap: 2px; }
  .pipeline-identity a { overflow: hidden; color: var(--text-primary); font-size: 12px; font-weight: 600; text-overflow: ellipsis; white-space: nowrap; } .pipeline-identity a:hover { color: var(--accent); }
  .pipeline-identity small, .timestamp small, .muted small { display: block; overflow: hidden; color: var(--text-dim); font-size: 9px; text-overflow: ellipsis; white-space: nowrap; }
  .result-badge { display: inline-flex; height: 25px; align-items: center; gap: 5px; padding: 0 9px; border: 1px solid var(--border-subtle); border-radius: 5px; font-size: 10px; font-weight: 600; }
  .result-badge.success { border-color: color-mix(in srgb, var(--success), transparent 75%); background: var(--success-bg); color: var(--success); } .result-badge.failed { border-color: color-mix(in srgb, var(--failed), transparent 75%); background: var(--failed-bg); color: var(--failed); } .result-badge.running { border-color: color-mix(in srgb, var(--warning), transparent 75%); background: var(--warning-bg); color: var(--warning); }
  .run-bars { display: flex; gap: 7px; } .run-bars i { width: 26px; height: 9px; border-radius: 3px; background: var(--border); } .run-bars .succeeded, .run-bars .success, .run-bars .completed { background: var(--success); } .run-bars .failed { background: var(--failed); } .run-bars .running { background: var(--warning); }
  .cron { color: var(--text-secondary); font: 10px var(--font-mono); } .muted { color: var(--text-muted); }
  .timestamp { display: flex; flex-direction: column; gap: 2px; } .timestamp strong { color: var(--text-primary); font-weight: 500; }
  .nodes-cell { text-align: center; } .row-actions { display: flex; align-items: center; justify-content: center; gap: 3px; overflow: visible; }
  .act-btn { min-width: 28px; width: auto; padding: 0 7px; } .act-btn:disabled { opacity: .35; cursor: not-allowed; }
  .action-menu-wrap { position: relative; }
  .action-menu { position: fixed; z-index: 1000; width: 160px; padding: 5px; border: 1px solid var(--border); border-radius: 7px; background: var(--bg-secondary); box-shadow: var(--shadow-lg); }
  .action-menu button { display: block; width: 100%; padding: 7px 9px; border-radius: 4px; color: var(--text-secondary); font-size: 11px; text-align: left; }
  .action-menu button:hover { color: var(--text-primary); background: var(--bg-tertiary); } .action-menu button.danger { color: var(--failed); }
  .inventory-footer { display: flex; height: 48px; align-items: center; gap: 10px; margin-top: auto; padding: 0 14px; border-top: 1px solid var(--border-subtle); color: var(--text-muted); font-size: 10px; }
  .inventory-footer label { display: flex; align-items: center; gap: 8px; } .inventory-footer select { height: 29px; padding: 0 22px 0 8px; border: 1px solid var(--border); border-radius: 5px; background: var(--bg-tertiary); color: var(--text-secondary); }
  .inventory-footer > span { margin-left: auto; } .inventory-footer button { width: 28px; height: 28px; color: var(--text-muted); } .inventory-footer button:disabled { opacity: .3; } .inventory-footer strong { display: grid; width: 34px; height: 29px; place-items: center; border-radius: 5px; background: var(--accent); color: #031514; }

  .inventory .empty-hero {
    position: relative;
    display: flex;
    min-height: 480px;
    align-items: center;
    justify-content: flex-start;
    padding: 56px 32px 72px;
    overflow: hidden;
    border-radius: 0 0 9px 9px;
    background:
      radial-gradient(circle at 50% 8%, color-mix(in srgb, var(--accent), transparent 88%), transparent 34%),
      linear-gradient(180deg, color-mix(in srgb, var(--bg-secondary), white 1.5%), var(--bg-secondary));
  }
  .inventory .empty-hero::before {
    content: "";
    position: absolute;
    top: 42px;
    left: 50%;
    width: 440px;
    height: 1px;
    background: linear-gradient(90deg, transparent, color-mix(in srgb, var(--accent), transparent 55%), transparent);
    transform: translateX(-50%);
  }
  .empty-kicker {
    margin-bottom: 10px;
    color: var(--accent);
    font: 650 10px var(--font-mono);
    letter-spacing: .14em;
    text-transform: uppercase;
  }
  .inventory .empty-hero h2 { margin: 0; font-size: 24px; font-weight: 650; letter-spacing: -.035em; }
  .inventory .empty-hero-sub { max-width: 540px; margin: 8px auto 32px; color: var(--text-muted); font-size: 12px; line-height: 1.6; }
  .inventory .template-grid {
    display: grid;
    width: min(920px, 100%);
    max-width: 920px;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    justify-content: center;
    gap: 14px;
    margin: 0 auto;
  }
  .inventory .template-card {
    position: relative;
    display: grid;
    min-height: 164px;
    grid-template-rows: 46px auto 1fr;
    align-items: start;
    gap: 8px;
    padding: 18px;
    overflow: hidden;
    border: 1px solid var(--border-subtle);
    border-radius: 10px;
    background:
      linear-gradient(145deg, color-mix(in srgb, var(--bg-tertiary), transparent 28%), transparent 58%),
      var(--bg-secondary);
    color: inherit;
    text-align: left;
    box-shadow: inset 0 1px 0 rgba(255,255,255,.025), 0 8px 22px rgba(0,0,0,.12);
    transform: none;
    transition: border-color 160ms ease, transform 160ms ease, box-shadow 160ms ease;
  }
  .inventory .template-card::after {
    content: "→";
    position: absolute;
    right: 16px;
    bottom: 14px;
    color: var(--text-dim);
    font-size: 15px;
    transition: color 160ms ease, transform 160ms ease;
  }
  .inventory .template-card:hover {
    border-color: color-mix(in srgb, var(--accent), transparent 38%);
    background:
      linear-gradient(145deg, color-mix(in srgb, var(--accent), transparent 91%), transparent 62%),
      var(--bg-secondary);
    box-shadow: 0 14px 30px rgba(0,0,0,.22), inset 0 0 0 1px color-mix(in srgb, var(--accent), transparent 88%);
    transform: translateY(-3px);
  }
  .inventory .template-card:hover::after { color: var(--accent); transform: translateX(3px); }
  .inventory .tmpl-icon {
    display: grid;
    width: 42px;
    height: 42px;
    place-items: center;
    border: 1px solid color-mix(in srgb, var(--accent), transparent 68%);
    border-radius: 9px;
    background: var(--accent-glow);
    color: var(--accent);
  }
  .inventory .tmpl-name { color: var(--text-primary); font-size: 13px; font-weight: 650; letter-spacing: -.01em; }
  .inventory .tmpl-desc { max-width: calc(100% - 22px); color: var(--text-muted); font-size: 10.5px; line-height: 1.5; }

  .modal-overlay { background: rgba(0, 4, 6, .78); backdrop-filter: blur(8px) saturate(.8); }
  .modal.modal-wide {
    width: min(720px, calc(100vw - 32px));
    max-width: 720px;
    padding: 0;
    overflow: hidden;
    border-color: color-mix(in srgb, var(--border), white 8%);
    border-radius: 12px;
    background: var(--bg-secondary);
    box-shadow: 0 32px 90px rgba(0,0,0,.62), inset 0 1px 0 rgba(255,255,255,.035);
  }
  .create-header { display: flex; align-items: center; justify-content: space-between; padding: 20px 22px; border-bottom: 1px solid var(--border-subtle); }
  .create-heading { display: flex; align-items: center; gap: 12px; }
  .create-mark { display: grid; width: 38px; height: 38px; place-items: center; border: 1px solid color-mix(in srgb, var(--accent), transparent 65%); border-radius: 9px; background: var(--accent-glow); color: var(--accent); font-size: 22px; font-weight: 300; }
  .modal .create-heading h2 { margin: 0; color: var(--text-primary); font-size: 15px; font-weight: 650; letter-spacing: -.015em; }
  .create-heading p { margin-top: 3px; color: var(--text-muted); font-size: 10.5px; }
  .modal-close { display: grid; width: 32px; height: 32px; place-items: center; border-radius: 6px; color: var(--text-muted); font-size: 20px; font-weight: 300; }
  .modal-close:hover { background: var(--bg-tertiary); color: var(--text-primary); }
  .create-body { display: flex; flex-direction: column; gap: 24px; padding: 22px; }
  .template-picker { min-width: 0; border: 0; }
  .template-picker legend { margin-bottom: 10px; color: var(--text-secondary); font-size: 10px; font-weight: 650; letter-spacing: .08em; text-transform: uppercase; }
  .modal .template-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 9px; width: 100%; margin: 0; }
  .modal .template-card {
    position: relative;
    display: grid;
    min-height: 84px;
    grid-template-columns: 38px minmax(0, 1fr) auto;
    align-items: center;
    gap: 11px;
    padding: 13px;
    border: 1px solid var(--border-subtle);
    border-radius: 8px;
    background: linear-gradient(135deg, color-mix(in srgb, var(--bg-tertiary), transparent 48%), transparent), var(--bg-secondary);
    color: inherit;
    text-align: left;
    box-shadow: none;
    transform: none;
  }
  .modal .template-card:hover { border-color: var(--border-hover); background: var(--bg-tertiary); transform: none; box-shadow: none; }
  .modal .template-card.active { border-color: color-mix(in srgb, var(--accent), transparent 25%); background: var(--accent-glow); box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--accent), transparent 82%); }
  .modal-template-icon { display: grid; width: 38px; height: 38px; place-items: center; border: 1px solid var(--border); border-radius: 8px; background: var(--bg-primary); color: var(--text-muted); font: 17px var(--font-mono); }
  .template-card.active .modal-template-icon { border-color: color-mix(in srgb, var(--accent), transparent 55%); background: color-mix(in srgb, var(--accent-glow), var(--bg-primary) 45%); color: var(--accent); }
  .template-copy { display: flex; min-width: 0; flex-direction: column; gap: 3px; }
  .template-copy strong { color: var(--text-primary); font-size: 11.5px; font-weight: 620; }
  .template-copy small { overflow: hidden; color: var(--text-muted); font-size: 9.5px; line-height: 1.35; text-overflow: ellipsis; white-space: nowrap; }
  .modal .template-meta { align-self: end; color: var(--text-dim); font: 8.5px var(--font-mono); white-space: nowrap; }
  .selected-check { position: absolute; top: 7px; right: 8px; color: var(--accent); font-size: 10px; }
  .pipeline-details { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; padding-top: 20px; border-top: 1px solid var(--border-subtle); }
  .details-heading { display: flex; grid-column: 1 / -1; align-items: baseline; justify-content: space-between; }
  .details-heading span { color: var(--text-secondary); font-size: 10px; font-weight: 650; letter-spacing: .08em; text-transform: uppercase; }
  .details-heading small { color: var(--text-dim); font-size: 9px; }
  .modal .form-group { margin: 0; }
  .modal .form-group label { display: flex; align-items: center; justify-content: space-between; margin-bottom: 7px; color: var(--text-secondary); font-size: 10px; font-weight: 550; letter-spacing: 0; text-transform: none; }
  .modal .form-group label span { color: var(--text-dim); font-size: 8.5px; font-weight: 400; }
  .modal .form-group input { height: 38px; padding: 0 11px; border-color: var(--border); border-radius: 6px; background: var(--bg-primary); font-size: 11px; }
  .modal .form-group input:focus { border-color: var(--accent); box-shadow: 0 0 0 2px var(--accent-glow); }
  .modal .modal-actions { display: flex; justify-content: flex-end; gap: 8px; margin: 0; padding: 14px 22px; border-top: 1px solid var(--border-subtle); background: color-mix(in srgb, var(--bg-primary), transparent 45%); }
  .modal .modal-actions button { min-height: 34px; padding-inline: 14px; font-size: 11px; }
  .modal .modal-actions .btn-primary { gap: 9px; }
  .modal .modal-actions .btn-primary:disabled { opacity: .45; cursor: not-allowed; }

  @media (max-width: 768px) {
    .page-header { flex-wrap: wrap; gap: 8px; }
    .search-bar { width: 100%; }
    .table-header { display: none; }
    .table-row { display: flex; flex-wrap: wrap; gap: 4px; padding: 10px; }
    .td-name { flex: 1; min-width: 60%; }
    .td-schedule, .td-nodes, .td-runs, .td-next { font-size: 10px; }
    .health-strip { grid-template-columns: repeat(2, 1fr); }
    .inventory .filter-bar { display: flex; align-items: stretch; } .inventory .filter-controls { overflow-x: auto; }
    .inventory-footer > span { display: none; }
    .inventory .template-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); max-width: 520px; }
    .inventory .empty-hero { padding-inline: 20px; }
    .pipeline-details { grid-template-columns: 1fr; } .details-heading { grid-column: 1; }
  }
  @media (max-width: 520px) {
    .inventory .template-grid { grid-template-columns: minmax(0, 320px); }
    .inventory .template-card { min-height: 145px; }
    .modal .template-grid { grid-template-columns: 1fr; }
    .create-body { max-height: 70vh; overflow-y: auto; }
  }
</style>
