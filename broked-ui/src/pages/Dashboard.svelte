<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { api } from "../lib/api";
  import { notify } from "../lib/toast";
  import { pipelines, events, onWSEvent } from "../lib/stores";
  import StatusBadge from "../components/StatusBadge.svelte";
  import type { Pipeline, Run } from "../lib/types";

  let recentRuns: { pipeline: Pipeline; run: Run }[] = [];
  let stats = { total: 0, today: 0, successRate: 0 };
  let loading = true;
  let unsubWS: (() => void) | null = null;

  async function loadDashboard() {
    try {
      const pipelineList = await api.pipelines.list();
      pipelines.set(pipelineList);
      stats.total = pipelineList.length;

      const allRuns: { pipeline: Pipeline; run: Run }[] = [];
      for (const p of pipelineList.slice(0, 10)) {
        const r = await api.runs.listByPipeline(p.id);
        for (const run of r.slice(0, 3)) {
          allRuns.push({ pipeline: p, run });
        }
      }
      recentRuns = allRuns
        .sort((a, b) => (b.run.started_at || "").localeCompare(a.run.started_at || ""))
        .slice(0, 10);

      const today = new Date().toISOString().split("T")[0];
      const todayRuns = allRuns.filter((r) => r.run.started_at?.startsWith(today));
      stats.today = todayRuns.length;
      const successful = todayRuns.filter((r) => r.run.status === "success").length;
      stats.successRate = todayRuns.length ? Math.round((successful / todayRuns.length) * 100) : 100;
    } catch (e) {
      notify.error("Failed to load dashboard");
    } finally {
      loading = false;
    }
  }

  onMount(() => {
    loadDashboard();

    // Auto-refresh on run events
    unsubWS = onWSEvent((event) => {
      if (event.type === "run.completed" || event.type === "run.failed" || event.type === "run.started") {
        loadDashboard();
      }
    });
  });

  onDestroy(() => { unsubWS?.(); });

  function timeAgo(dateStr: string | null): string {
    if (!dateStr) return "—";
    const seconds = Math.floor(
      (Date.now() - new Date(dateStr).getTime()) / 1000
    );
    if (seconds < 60) return `${seconds}s ago`;
    if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
    if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`;
    return `${Math.floor(seconds / 86400)}d ago`;
  }
</script>

<div class="dashboard animate-in">
  <header class="page-header">
    <h1>Dashboard</h1>
  </header>

  <div class="stats-grid">
    <div class="stat-card">
      <span class="stat-value">{stats.total}</span>
      <span class="stat-label">Pipelines</span>
    </div>
    <div class="stat-card">
      <span class="stat-value">{stats.today}</span>
      <span class="stat-label">Runs Today</span>
    </div>
    <div class="stat-card">
      <span class="stat-value">{stats.successRate}%</span>
      <span class="stat-label">Success Rate</span>
    </div>
  </div>

  <section class="section">
    <h2 class="section-title">Recent Runs</h2>
    {#if loading}
      <div class="empty-state">Loading...</div>
    {:else if recentRuns.length === 0}
      <div class="empty-state">
        <p>No pipeline runs yet.</p>
        <p class="hint">Create a pipeline and trigger a run to get started.</p>
      </div>
    {:else}
      <div class="runs-table">
        <div class="table-header">
          <span class="col-pipeline">Pipeline</span>
          <span class="col-status">Status</span>
          <span class="col-time">Started</span>
        </div>
        {#each recentRuns as { pipeline, run }}
          <div class="table-row">
            <span class="col-pipeline">{pipeline.name}</span>
            <span class="col-status">
              <StatusBadge status={run.status} size="sm" />
            </span>
            <span class="col-time mono">{timeAgo(run.started_at)}</span>
          </div>
        {/each}
      </div>
    {/if}
  </section>

  <section class="section">
    <h2 class="section-title">Activity Feed</h2>
    {#if $events.length === 0}
      <div class="empty-state">
        <p class="hint">Real-time events will appear here.</p>
      </div>
    {:else}
      <div class="activity-feed">
        {#each $events as event}
          <div class="activity-item">
            <span class="activity-type mono">{event.type}</span>
            <span class="activity-msg">{event.message || event.run_id?.slice(0, 8)}</span>
            <span class="activity-time mono">{timeAgo(event.timestamp)}</span>
          </div>
        {/each}
      </div>
    {/if}
  </section>
</div>

<style>
  .page-header {
    margin-bottom: var(--space-xl);
  }
  .page-header h1 {
    font-size: 1.5rem;
    font-weight: 600;
    letter-spacing: -0.02em;
  }

  .stats-grid {
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    gap: var(--space-md);
    margin-bottom: var(--space-xl);
  }
  .stat-card {
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    padding: var(--space-lg);
    display: flex;
    flex-direction: column;
    gap: var(--space-xs);
  }
  .stat-value {
    font-size: 2rem;
    font-weight: 700;
    font-family: var(--font-mono);
    letter-spacing: -0.02em;
  }
  .stat-label {
    font-size: 0.75rem;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.1em;
  }

  .section {
    margin-bottom: var(--space-xl);
  }
  .section-title {
    font-size: 0.875rem;
    font-weight: 600;
    color: var(--text-secondary);
    text-transform: uppercase;
    letter-spacing: 0.08em;
    margin-bottom: var(--space-md);
  }

  .empty-state {
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    padding: var(--space-xl);
    text-align: center;
    color: var(--text-secondary);
  }
  .hint { color: var(--text-muted); font-size: 0.875rem; margin-top: var(--space-xs); }

  .runs-table {
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    overflow: hidden;
  }
  .table-header, .table-row {
    display: grid;
    grid-template-columns: 1fr 120px 100px;
    padding: var(--space-sm) var(--space-md);
    align-items: center;
  }
  .table-header {
    background: var(--bg-tertiary);
    font-size: 0.75rem;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.08em;
    font-weight: 600;
  }
  .table-row {
    border-top: 1px solid var(--border);
    font-size: 0.875rem;
    transition: background var(--transition-fast);
  }
  .table-row:hover {
    background: var(--bg-tertiary);
  }
  .mono {
    font-family: var(--font-mono);
    font-size: 0.75rem;
    color: var(--text-muted);
  }

  .activity-feed {
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    max-height: 300px;
    overflow-y: auto;
  }
  .activity-item {
    display: grid;
    grid-template-columns: 140px 1fr 80px;
    padding: var(--space-sm) var(--space-md);
    border-bottom: 1px solid var(--border);
    font-size: 0.8125rem;
    align-items: center;
  }
  .activity-item:last-child { border-bottom: none; }
  .activity-type {
    font-family: var(--font-mono);
    font-size: 0.6875rem;
    color: var(--accent);
  }
  .activity-msg { color: var(--text-secondary); }
  .activity-time { text-align: right; }
</style>
