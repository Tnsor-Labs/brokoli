<script lang="ts">
  import { onMount } from "svelte";
  import { notify } from "../lib/toast";
  import { authHeaders } from "../lib/auth";

  interface CalendarDay {
    date: string;
    total: number;
    success: number;
    failed: number;
    running: number;
  }

  let days: CalendarDay[] = [];
  let loading = true;
  let rangeDays = 90;
  let selectedDay: CalendarDay | null = null;

  onMount(() => loadCalendar());

  async function loadCalendar() {
    loading = true;
    try {
      const res = await fetch(`/api/runs/calendar?days=${rangeDays}`, { headers: authHeaders() });
      days = await res.json();
    } catch {
      notify.error("Failed to load calendar");
    } finally {
      loading = false;
    }
  }

  // Build a full grid from today - rangeDays to today
  function buildGrid(): { date: string; data: CalendarDay | null; isToday: boolean }[] {
    const map = new Map(days.map(d => [d.date, d]));
    const grid: { date: string; data: CalendarDay | null; isToday: boolean }[] = [];
    const today = new Date();
    today.setHours(0, 0, 0, 0);

    for (let i = rangeDays - 1; i >= 0; i--) {
      const d = new Date(today);
      d.setDate(d.getDate() - i);
      const key = d.toISOString().slice(0, 10);
      grid.push({
        date: key,
        data: map.get(key) || null,
        isToday: i === 0,
      });
    }
    return grid;
  }

  function cellColor(d: CalendarDay | null): string {
    if (!d || d.total === 0) return "var(--bg-tertiary)";
    if (d.failed > 0) return "var(--failed)";
    if (d.running > 0) return "var(--running)";
    return "var(--success)";
  }

  function cellOpacity(d: CalendarDay | null): number {
    if (!d || d.total === 0) return 0.3;
    // Scale opacity by number of runs (min 0.4, max 1.0)
    return Math.min(1.0, 0.4 + d.total * 0.1);
  }

  function formatDate(dateStr: string): string {
    const d = new Date(dateStr + "T00:00:00");
    return d.toLocaleDateString("en-US", { weekday: "short", month: "short", day: "numeric" });
  }

  function dayOfWeek(dateStr: string): number {
    return new Date(dateStr + "T00:00:00").getDay();
  }

  // Group grid by weeks
  function buildWeeks(grid: ReturnType<typeof buildGrid>): ReturnType<typeof buildGrid>[] {
    const weeks: ReturnType<typeof buildGrid>[] = [];
    let week: ReturnType<typeof buildGrid> = [];

    // Pad first week with empty cells
    if (grid.length > 0) {
      const firstDay = dayOfWeek(grid[0].date);
      for (let i = 0; i < firstDay; i++) {
        week.push({ date: "", data: null, isToday: false });
      }
    }

    for (const cell of grid) {
      week.push(cell);
      if (week.length === 7) {
        weeks.push(week);
        week = [];
      }
    }
    if (week.length > 0) {
      weeks.push(week);
    }
    return weeks;
  }

  $: grid = buildGrid();
  $: weeks = buildWeeks(grid);
  $: totalRuns = days.reduce((s, d) => s + d.total, 0);
  $: totalFailed = days.reduce((s, d) => s + d.failed, 0);
  $: totalSuccess = days.reduce((s, d) => s + d.success, 0);
  $: activeDays = days.filter(d => d.total > 0).length;
</script>

<div class="calendar-page animate-in">
  <header class="page-header">
    <div class="header-left">
      <h1>Run Calendar</h1>
      <span class="meta">{totalRuns} runs over {rangeDays} days</span>
    </div>
    <div class="header-right">
      <select value={rangeDays} on:change={(e) => { rangeDays = Number(e.currentTarget.value); loadCalendar(); }}>
        <option value={30}>30 days</option>
        <option value={90}>90 days</option>
        <option value={180}>180 days</option>
        <option value={365}>365 days</option>
      </select>
    </div>
  </header>

  <!-- Stats bar -->
  <div class="stats-bar">
    <div class="stat">
      <span class="stat-value">{totalRuns}</span>
      <span class="stat-label">Total Runs</span>
    </div>
    <div class="stat">
      <span class="stat-value" style="color: var(--success)">{totalSuccess}</span>
      <span class="stat-label">Succeeded</span>
    </div>
    <div class="stat">
      <span class="stat-value" style="color: var(--failed)">{totalFailed}</span>
      <span class="stat-label">Failed</span>
    </div>
    <div class="stat">
      <span class="stat-value">{activeDays}</span>
      <span class="stat-label">Active Days</span>
    </div>
    <div class="stat">
      <span class="stat-value">{totalRuns > 0 ? ((totalSuccess / totalRuns) * 100).toFixed(1) : "—"}%</span>
      <span class="stat-label">Success Rate</span>
    </div>
  </div>

  {#if loading}
    <div class="empty-state">Loading calendar...</div>
  {:else}
    <div class="calendar-grid">
      <div class="weekday-header">
        {#each ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"] as day}
          <span class="weekday">{day}</span>
        {/each}
      </div>

      {#each weeks as week}
        <div class="week-row">
          {#each week as cell}
            {#if cell.date}
              <!-- svelte-ignore a11y_no_static_element_interactions -->
              <div
                class="day-cell"
                class:today={cell.isToday}
                class:has-runs={cell.data && cell.data.total > 0}
                class:has-failures={cell.data && cell.data.failed > 0}
                on:click={() => selectedDay = cell.data && cell.data.total > 0 ? cell.data : null}
                on:keydown={() => {}}
                title={cell.date + (cell.data ? `: ${cell.data.total} runs (${cell.data.success} ok, ${cell.data.failed} failed)` : ": no runs")}
              >
                <span class="day-num">{cell.date.slice(8)}</span>
                <div
                  class="day-indicator"
                  style="background: {cellColor(cell.data)}; opacity: {cellOpacity(cell.data)}"
                ></div>
              </div>
            {:else}
              <div class="day-cell empty"></div>
            {/if}
          {/each}
        </div>
      {/each}
    </div>

    <!-- Legend -->
    <div class="legend">
      <span class="legend-label">Less</span>
      <span class="legend-box" style="background: var(--bg-tertiary); opacity: 0.3"></span>
      <span class="legend-box" style="background: var(--success); opacity: 0.4"></span>
      <span class="legend-box" style="background: var(--success); opacity: 0.7"></span>
      <span class="legend-box" style="background: var(--success); opacity: 1"></span>
      <span class="legend-label">More</span>
      <span class="legend-sep"></span>
      <span class="legend-box" style="background: var(--failed); opacity: 0.8"></span>
      <span class="legend-label">Has failures</span>
    </div>

    <!-- Day detail -->
    {#if selectedDay}
      <div class="day-detail">
        <h3>{formatDate(selectedDay.date)}</h3>
        <div class="detail-stats">
          <span class="detail-stat"><strong>{selectedDay.total}</strong> total</span>
          <span class="detail-stat success"><strong>{selectedDay.success}</strong> succeeded</span>
          <span class="detail-stat failed"><strong>{selectedDay.failed}</strong> failed</span>
          {#if selectedDay.running > 0}
            <span class="detail-stat running"><strong>{selectedDay.running}</strong> running</span>
          {/if}
        </div>
      </div>
    {/if}
  {/if}
</div>

<style>
  .calendar-page {
    display: flex; flex-direction: column; gap: var(--space-md);
  }

  .page-header {
    display: flex; justify-content: space-between; align-items: center;
  }
  .header-left { display: flex; align-items: baseline; gap: 12px; }
  .page-header h1 { font-size: 1.5rem; font-weight: 600; letter-spacing: -0.02em; }
  .meta { font-size: 0.8125rem; color: var(--text-muted); font-family: var(--font-mono); }
  .header-right select {
    padding: 6px 12px; border-radius: 6px; font-size: 12px;
    background: var(--bg-secondary); border: 1px solid var(--border);
    color: var(--text-secondary); font-family: var(--font-ui);
  }

  .stats-bar {
    display: flex; gap: var(--space-md);
    padding: var(--space-md) var(--space-lg);
    background: var(--bg-secondary); border: 1px solid var(--border);
    border-radius: var(--radius-lg);
  }
  .stat { display: flex; flex-direction: column; gap: 2px; flex: 1; }
  .stat-value {
    font-family: var(--font-mono); font-size: 20px; font-weight: 700;
    color: var(--text-primary);
  }
  .stat-label {
    font-size: 10px; color: var(--text-muted); text-transform: uppercase;
    letter-spacing: 0.08em; font-weight: 600;
  }

  .empty-state {
    background: var(--bg-secondary); border: 1px solid var(--border);
    border-radius: var(--radius-lg); padding: var(--space-xl);
    text-align: center; color: var(--text-secondary);
  }

  .calendar-grid {
    background: var(--bg-secondary); border: 1px solid var(--border);
    border-radius: var(--radius-lg); padding: var(--space-md);
  }

  .weekday-header {
    display: grid; grid-template-columns: repeat(7, 1fr);
    gap: 4px; margin-bottom: 4px;
  }
  .weekday {
    text-align: center; font-size: 10px; font-weight: 600;
    color: var(--text-dim); text-transform: uppercase; letter-spacing: 0.06em;
    padding: 4px 0;
  }

  .week-row {
    display: grid; grid-template-columns: repeat(7, 1fr);
    gap: 4px; margin-bottom: 4px;
  }

  .day-cell {
    position: relative;
    aspect-ratio: 1;
    min-height: 28px;
    display: flex; flex-direction: column;
    align-items: center; justify-content: center;
    border-radius: 4px;
    cursor: pointer;
    transition: all 150ms ease;
    border: 1px solid transparent;
  }
  .day-cell:not(.empty):hover { border-color: var(--border-hover); }
  .day-cell.today { border-color: var(--accent); }
  .day-cell.empty { cursor: default; }

  .day-num {
    font-size: 10px; font-weight: 500; color: var(--text-muted);
    z-index: 1; position: relative;
  }
  .day-cell.has-runs .day-num { color: var(--text-primary); font-weight: 600; }

  .day-indicator {
    position: absolute; inset: 2px;
    border-radius: 3px;
    z-index: 0;
  }

  .legend {
    display: flex; align-items: center; gap: 4px;
    font-size: 10px; color: var(--text-muted);
    justify-content: flex-end;
  }
  .legend-box {
    width: 12px; height: 12px; border-radius: 2px;
  }
  .legend-label { margin: 0 2px; }
  .legend-sep { width: 12px; }

  .day-detail {
    background: var(--bg-secondary); border: 1px solid var(--border);
    border-radius: var(--radius-lg); padding: var(--space-md) var(--space-lg);
  }
  .day-detail h3 { font-size: 14px; margin-bottom: var(--space-sm); }
  .detail-stats {
    display: flex; gap: var(--space-md); font-size: 13px;
  }
  .detail-stat { color: var(--text-secondary); }
  .detail-stat strong { font-family: var(--font-mono); }
  .detail-stat.success strong { color: var(--success); }
  .detail-stat.failed strong { color: var(--failed); }
  .detail-stat.running strong { color: var(--running); }
</style>
