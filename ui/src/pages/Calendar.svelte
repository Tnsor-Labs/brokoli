<script lang="ts">
  import { onMount } from "svelte";
  import { notify } from "../lib/toast";
  import { authHeaders } from "../lib/auth";
  import Skeleton from "../components/Skeleton.svelte";

  interface CalendarDay {
    date: string;
    total: number;
    success: number;
    failed: number;
    running: number;
  }

  let days: CalendarDay[] = [];
  let loading = true;
  // Fixed 1-year window. At 365 days the cell grid auto-sizes to a compact
  // GitHub-style heatmap that fits any modern viewport without scrolling.
  const rangeDays = 365;
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

  // Build full grid from today - rangeDays + 1 to today
  function buildGrid(): { date: string; data: CalendarDay | null; isToday: boolean }[] {
    const map = new Map(days.map(d => [d.date, d]));
    const grid: { date: string; data: CalendarDay | null; isToday: boolean }[] = [];
    const today = new Date();
    today.setHours(0, 0, 0, 0);

    for (let i = rangeDays - 1; i >= 0; i--) {
      const d = new Date(today);
      d.setDate(d.getDate() - i);
      const key = `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
      grid.push({
        date: key,
        data: map.get(key) || null,
        isToday: i === 0,
      });
    }
    return grid;
  }

  // Build columns (weeks). Each column has 7 cells (Sun..Sat). The first
  // column is padded at the top until it reaches the weekday of the first
  // grid date. The last column is padded at the bottom past the latest date.
  type Cell = { date: string; data: CalendarDay | null; isToday: boolean } | null;
  function buildColumns(grid: ReturnType<typeof buildGrid>): { weeks: Cell[][]; firstDateInWeek: (string | null)[] } {
    const weeks: Cell[][] = [];
    const firstDateInWeek: (string | null)[] = [];
    if (grid.length === 0) return { weeks, firstDateInWeek };

    let week: Cell[] = [];
    const firstDay = new Date(grid[0].date + "T00:00:00").getDay();
    for (let i = 0; i < firstDay; i++) week.push(null);

    for (const cell of grid) {
      week.push(cell);
      if (week.length === 7) {
        const firstReal = week.find(c => c !== null);
        firstDateInWeek.push(firstReal ? firstReal.date : null);
        weeks.push(week);
        week = [];
      }
    }
    if (week.length > 0) {
      while (week.length < 7) week.push(null);
      const firstReal = week.find(c => c !== null);
      firstDateInWeek.push(firstReal ? firstReal.date : null);
      weeks.push(week);
    }
    return { weeks, firstDateInWeek };
  }

  function monthLabel(dateStr: string): string {
    const d = new Date(dateStr + "T00:00:00");
    return d.toLocaleDateString("en-US", { month: "short" });
  }

  function formatFullDate(dateStr: string): string {
    const d = new Date(dateStr + "T00:00:00");
    return d.toLocaleDateString("en-US", { weekday: "long", month: "long", day: "numeric", year: "numeric" });
  }

  function intensity(total: number, max: number): number {
    if (total === 0) return 0;
    return Math.min(0.92, 0.42 + (Math.log(total + 1) / Math.log(max + 1)) * 0.5);
  }

  // Compute month labels: emit a label for the column where the month changes.
  function computeMonthLabels(firstDateInWeek: (string | null)[]): { col: number; label: string }[] {
    const labels: { col: number; label: string }[] = [];
    let lastMonth = "";
    firstDateInWeek.forEach((date, col) => {
      if (!date) return;
      const m = monthLabel(date);
      if (m !== lastMonth) {
        labels.push({ col, label: m });
        lastMonth = m;
      }
    });
    return labels;
  }

  $: grid = (() => { days; return buildGrid(); })();
  $: ({ weeks, firstDateInWeek } = buildColumns(grid));
  $: monthLabels = computeMonthLabels(firstDateInWeek);
  $: maxDayTotal = Math.max(1, ...days.map(d => d.total));
  $: totalRuns = days.reduce((s, d) => s + d.total, 0);
  $: totalFailed = days.reduce((s, d) => s + d.failed, 0);
  $: totalSuccess = days.reduce((s, d) => s + d.success, 0);
  $: activeDays = days.filter(d => d.total > 0).length;
  $: successRate = totalRuns > 0 ? (totalSuccess / totalRuns) * 100 : 0;

  // Sparkline path: total runs per day across the range (oldest → newest)
  $: sparkline = buildSparkline(grid);

  function buildSparkline(g: ReturnType<typeof buildGrid>): { path: string; areaPath: string; max: number } {
    if (g.length === 0) return { path: "", areaPath: "", max: 0 };
    const w = 100, h = 100;
    const max = Math.max(1, ...g.map(c => c.data?.total ?? 0));
    const stepX = w / Math.max(1, g.length - 1);
    const points: [number, number][] = g.map((c, i) => {
      const v = c.data?.total ?? 0;
      const y = h - (v / max) * h;
      return [i * stepX, y];
    });
    const path = points.map((p, i) => `${i === 0 ? "M" : "L"}${p[0].toFixed(2)},${p[1].toFixed(2)}`).join(" ");
    const areaPath = `${path} L${w},${h} L0,${h} Z`;
    return { path, areaPath, max };
  }
</script>

<div class="calendar-page animate-in">
  <header class="page-header">
    <div class="header-left">
      <h1>Run Calendar</h1>
      <span class="meta">{totalRuns} runs · {activeDays} active days · last 12 months</span>
    </div>
  </header>

  <!-- Stats bar -->
  <div class="stats-bar">
    <div class="stat">
      <span class="stat-value">{totalRuns}</span>
      <span class="stat-label">Total Runs</span>
    </div>
    <div class="stat">
      <span class="stat-value" style="color: var(--cal-ok)">{totalSuccess}</span>
      <span class="stat-label">Succeeded</span>
    </div>
    <div class="stat">
      <span class="stat-value" style="color: var(--cal-fail)">{totalFailed}</span>
      <span class="stat-label">Failed</span>
    </div>
    <div class="stat">
      <span class="stat-value">{activeDays}</span>
      <span class="stat-label">Active Days</span>
    </div>
    <div class="stat">
      <span class="stat-value" style="color: {successRate >= 95 ? 'var(--cal-ok)' : successRate >= 70 ? '#c79a52' : successRate > 0 ? 'var(--cal-fail)' : 'var(--text-dim)'}">
        {totalRuns > 0 ? successRate.toFixed(1) : "—"}%
      </span>
      <span class="stat-label">Success Rate</span>
    </div>
  </div>

  {#if loading}
    <div style="display:flex;flex-direction:column;gap:8px">
      <Skeleton height="40px" /><Skeleton height="220px" />
    </div>
  {:else if totalRuns === 0}
    <div class="empty-card">
      <p>No runs yet in the selected range.</p>
    </div>
  {:else}
    <!-- Sparkline trend strip -->
    <div class="trend-card">
      <div class="trend-head">
        <span class="trend-title">Activity trend</span>
        <span class="trend-meta">peak {sparkline.max} runs/day</span>
      </div>
      <svg class="sparkline" viewBox="0 0 100 100" preserveAspectRatio="none">
        <defs>
          <linearGradient id="spark-grad" x1="0" x2="0" y1="0" y2="1">
            <stop offset="0%" stop-color="var(--accent)" stop-opacity="0.45" />
            <stop offset="100%" stop-color="var(--accent)" stop-opacity="0" />
          </linearGradient>
        </defs>
        <path d={sparkline.areaPath} fill="url(#spark-grad)" />
        <path d={sparkline.path} fill="none" stroke="var(--accent)" stroke-width="1.2" vector-effect="non-scaling-stroke" stroke-linejoin="round" stroke-linecap="round" />
      </svg>
    </div>

    <!-- Compact heatmap: 7-row weekday grid, weeks as columns -->
    <div class="heatmap-card">
      <div class="heatmap-scroller">
        <!-- Month labels row -->
        <div class="month-row" style:grid-template-columns="20px repeat({weeks.length}, 1fr)">
          <span></span>
          {#each weeks as _, col}
            {@const lbl = monthLabels.find(m => m.col === col)}
            <span class="month-label">{lbl?.label ?? ""}</span>
          {/each}
        </div>

        <!-- Heatmap grid: each row is a weekday, each column is a week -->
        <div class="grid-row" style:grid-template-columns="20px repeat({weeks.length}, 1fr)">
          <div class="weekday-col">
            <span class="weekday">Mon</span>
            <span class="weekday">Wed</span>
            <span class="weekday">Fri</span>
          </div>
          {#each weeks as week}
            <div class="week-col">
              {#each week as cell}
                {#if !cell}
                  <div class="day-cell empty"></div>
                {:else if cell.data && cell.data.total > 0}
                  {@const d = cell.data}
                  {@const denom = d.success + d.failed}
                  {@const okPct = denom > 0 ? (d.success / denom) * 100 : 0}
                  {@const failPct = denom > 0 ? (d.failed / denom) * 100 : 0}
                  {@const runPct = d.total > 0 ? (d.running / d.total) * 100 : 0}
                  {@const alpha = intensity(d.total, maxDayTotal)}
                  <!-- svelte-ignore a11y_no_static_element_interactions -->
                  <div
                    class="day-cell has-runs"
                    class:today={cell.isToday}
                    class:selected={selectedDay?.date === d.date}
                    style:--alpha={alpha}
                    style:--ok-pct="{okPct}%"
                    style:--fail-pct="{failPct}%"
                    style:--run-pct="{runPct}%"
                    on:click={() => selectedDay = d}
                    on:keydown={() => {}}
                    title="{cell.date}: {d.total} run{d.total === 1 ? '' : 's'} · {d.success} ok · {d.failed} failed{d.running > 0 ? ' · ' + d.running + ' running' : ''}"
                  ></div>
                {:else}
                  <div class="day-cell zero" class:today={cell.isToday} title={cell.date}></div>
                {/if}
              {/each}
            </div>
          {/each}
        </div>
      </div>

      <!-- Legend strip -->
      <div class="legend">
        <span class="legend-section">
          <span class="legend-label">Outcome split</span>
          <span class="legend-cell legend-cell--ok"></span><span class="legend-name">All success</span>
          <span class="legend-cell legend-cell--mix"></span><span class="legend-name">Mixed</span>
          <span class="legend-cell legend-cell--fail"></span><span class="legend-name">All failed</span>
        </span>
        <span class="legend-divider"></span>
        <span class="legend-section">
          <span class="legend-label">Activity</span>
          <span class="legend-cell legend-cell--lo"></span>
          <span class="legend-cell legend-cell--md"></span>
          <span class="legend-cell legend-cell--hi"></span>
        </span>
      </div>
    </div>

    <!-- Day detail card -->
    {#if selectedDay}
      {@const d = selectedDay}
      {@const sr = d.total > 0 ? (d.success / d.total) * 100 : 0}
      <div class="day-detail">
        <div class="detail-head">
          <h3>{formatFullDate(d.date)}</h3>
          <button class="detail-close" on:click={() => selectedDay = null} aria-label="Close detail">×</button>
        </div>
        <div class="detail-bar">
          {#if d.success > 0}<span class="detail-seg seg-ok" style:flex={d.success}>{d.success}</span>{/if}
          {#if d.running > 0}<span class="detail-seg seg-run" style:flex={d.running}>{d.running}</span>{/if}
          {#if d.failed > 0}<span class="detail-seg seg-fail" style:flex={d.failed}>{d.failed}</span>{/if}
        </div>
        <div class="detail-stats">
          <span class="detail-stat"><strong>{d.total}</strong> total</span>
          <span class="detail-stat success"><strong>{d.success}</strong> succeeded</span>
          <span class="detail-stat failed"><strong>{d.failed}</strong> failed</span>
          {#if d.running > 0}
            <span class="detail-stat running"><strong>{d.running}</strong> running</span>
          {/if}
          <span class="detail-stat rate" style:color={sr >= 95 ? 'var(--cal-ok)' : sr >= 70 ? '#c79a52' : 'var(--cal-fail)'}>
            <strong>{sr.toFixed(0)}%</strong> success rate
          </span>
        </div>
      </div>
    {/if}
  {/if}
</div>

<style>
  .calendar-page {
    display: flex; flex-direction: column; gap: var(--space-md);

    /*
     * Local muted palette for data-viz blocks on this page only.
     * The global --success / --failed are tuned for status badges and small
     * indicators where high saturation reads well; at the size of a heatmap
     * row or a 100%-wide proportional bar those same colors look "hot" and
     * fight for attention. These muted variants are desaturated forest /
     * terracotta / steel — same hue, much easier on the eyes when filling
     * a large area.
     */
    --cal-ok:   #4f9d6c;
    --cal-fail: #c4736e;
    --cal-run:  #6c8ec4;
  }

  .page-header {
    display: flex; justify-content: space-between; align-items: center;
  }
  .header-left { display: flex; align-items: baseline; gap: 12px; }
  .page-header h1 { font-size: 1.5rem; font-weight: 600; letter-spacing: -0.02em; }
  .meta { font-size: 0.8125rem; color: var(--text-muted); font-family: var(--font-mono); }

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

  .empty-card {
    background: var(--bg-secondary); border: 1px solid var(--border);
    border-radius: var(--radius-lg); padding: 32px;
    text-align: center; color: var(--text-muted); font-size: 13px;
  }

  /* ── Sparkline trend strip ───────────────────────────────────── */
  .trend-card {
    background: var(--bg-secondary); border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    padding: 12px 16px 8px;
  }
  .trend-head {
    display: flex; justify-content: space-between; align-items: baseline;
    margin-bottom: 4px;
  }
  .trend-title {
    font-size: 11px; font-weight: 600; color: var(--text-muted);
    text-transform: uppercase; letter-spacing: 0.06em;
  }
  .trend-meta {
    font-size: 11px; color: var(--text-dim); font-family: var(--font-mono);
  }
  .sparkline {
    width: 100%; height: 44px; display: block;
  }

  /* ── Heatmap card ────────────────────────────────────────────── */
  .heatmap-card {
    background: var(--bg-secondary); border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    padding: 16px 20px;
  }
  .heatmap-scroller {
    display: flex; flex-direction: column; gap: 4px;
  }

  .month-row {
    display: grid; gap: 3px;
    height: 14px;
    align-items: end;
  }
  .month-label {
    font-size: 10px; font-weight: 600; color: var(--text-muted);
    text-transform: uppercase; letter-spacing: 0.05em;
    white-space: nowrap;
    line-height: 1;
  }

  .grid-row {
    display: grid; gap: 3px;
  }
  .weekday-col {
    display: grid; grid-template-rows: repeat(7, 1fr);
    align-items: center; gap: 3px;
    font-size: 9px; color: var(--text-dim);
    text-transform: uppercase; letter-spacing: 0.04em;
  }
  .weekday-col .weekday:nth-child(1) { grid-row: 2; }
  .weekday-col .weekday:nth-child(2) { grid-row: 4; }
  .weekday-col .weekday:nth-child(3) { grid-row: 6; }

  .week-col {
    display: grid; grid-template-rows: repeat(7, 1fr); gap: 3px;
  }

  .day-cell {
    aspect-ratio: 1;
    border-radius: 4px;
    background: var(--bg-tertiary);
    border: 1px solid transparent;
    transition: transform 160ms cubic-bezier(0.16, 1, 0.3, 1),
                box-shadow 160ms ease,
                border-color 160ms ease;
    position: relative;
    cursor: default;
  }
  .day-cell.empty {
    background: transparent;
    border-color: transparent;
  }
  .day-cell.zero {
    background: color-mix(in oklab, var(--bg-tertiary) 70%, var(--text-dim) 6%);
  }

  /*
   * Mixed-outcome cell:
   *   • A vertical split via linear-gradient: success on the left, running in
   *     the middle, failed on the right, sized by their actual proportions.
   *   • An alpha channel modulated by activity intensity (log-scaled vs the
   *     busiest day in the range).
   *
   * This is the part the user explicitly asked for: a day with 4 success and
   * 8 failed shows ~33% green on the left and ~67% red on the right, instead
   * of being a uniform red block with a hidden ratio.
   */
  .day-cell.has-runs {
    cursor: pointer;
    background:
      linear-gradient(
        to right,
        color-mix(in oklab, var(--cal-ok)   calc(var(--alpha) * 100%), transparent) 0%,
        color-mix(in oklab, var(--cal-ok)   calc(var(--alpha) * 100%), transparent) var(--ok-pct),
        color-mix(in oklab, var(--cal-run)  calc(var(--alpha) * 100%), transparent) var(--ok-pct),
        color-mix(in oklab, var(--cal-run)  calc(var(--alpha) * 100%), transparent) calc(var(--ok-pct) + var(--run-pct)),
        color-mix(in oklab, var(--cal-fail) calc(var(--alpha) * 100%), transparent) calc(var(--ok-pct) + var(--run-pct)),
        color-mix(in oklab, var(--cal-fail) calc(var(--alpha) * 100%), transparent) 100%
      ),
      var(--bg-tertiary);
    border-color: rgba(255, 255, 255, 0.04);
  }
  .day-cell.has-runs:hover {
    transform: scale(1.4);
    z-index: 5;
    border-color: rgba(255, 255, 255, 0.18);
    box-shadow:
      0 6px 18px rgba(0, 0, 0, 0.5),
      0 0 0 1px rgba(255, 255, 255, 0.08);
  }
  .day-cell.has-runs.selected {
    border-color: var(--accent);
    box-shadow: 0 0 0 1px var(--accent);
  }
  .day-cell.today {
    border-color: var(--accent);
    box-shadow: 0 0 0 1px var(--accent);
  }
  .day-cell.has-runs.today {
    box-shadow: 0 0 0 1px var(--accent), 0 0 8px rgba(99, 102, 241, 0.3);
  }

  /* ── Legend ──────────────────────────────────────────────────── */
  .legend {
    display: flex; align-items: center; gap: 14px;
    font-size: 11px; color: var(--text-muted);
    justify-content: flex-end;
    flex-wrap: wrap;
    margin-top: 12px;
    padding-top: 12px;
    border-top: 1px solid var(--border-subtle);
  }
  .legend-section {
    display: flex; align-items: center; gap: 6px;
  }
  .legend-divider {
    width: 1px; height: 14px; background: var(--border-subtle);
  }
  .legend-label {
    font-size: 10px; font-weight: 600; text-transform: uppercase;
    letter-spacing: 0.06em; color: var(--text-dim);
    margin-right: 2px;
  }
  .legend-cell {
    width: 14px; height: 14px; border-radius: 3px;
    border: 1px solid rgba(255,255,255,0.04);
  }
  .legend-cell--ok   { background: var(--cal-ok); }
  .legend-cell--fail { background: var(--cal-fail); }
  .legend-cell--mix  {
    background: linear-gradient(to right, var(--cal-ok) 0% 33%, var(--cal-fail) 33% 100%);
  }
  .legend-cell--lo   { background: color-mix(in oklab, var(--cal-ok) 35%, transparent); }
  .legend-cell--md   { background: color-mix(in oklab, var(--cal-ok) 65%, transparent); }
  .legend-cell--hi   { background: var(--cal-ok); }
  .legend-name {
    font-size: 11px; color: var(--text-muted); margin-right: 4px;
  }

  /* ── Day detail card ─────────────────────────────────────────── */
  .day-detail {
    background: var(--bg-secondary); border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    padding: var(--space-md) var(--space-lg);
    display: flex; flex-direction: column; gap: 12px;
  }
  .detail-head {
    display: flex; justify-content: space-between; align-items: center;
  }
  .detail-head h3 { font-size: 14px; font-weight: 600; }
  .detail-close {
    background: transparent; border: none;
    color: var(--text-dim); font-size: 20px; line-height: 1;
    padding: 0 6px; border-radius: 4px; cursor: pointer;
  }
  .detail-close:hover { color: var(--text-primary); background: var(--bg-tertiary); }

  .detail-bar {
    display: flex; height: 28px; border-radius: 6px;
    overflow: hidden;
    border: 1px solid var(--border-subtle);
  }
  .detail-seg {
    display: flex; align-items: center; justify-content: center;
    color: white; font-family: var(--font-mono);
    font-size: 12px; font-weight: 700;
    text-shadow: 0 1px 2px rgba(0, 0, 0, 0.4);
  }
  .seg-ok   { background: var(--cal-ok); }
  .seg-run  { background: var(--cal-run); }
  .seg-fail { background: var(--cal-fail); }

  .detail-stats {
    display: flex; gap: var(--space-md); font-size: 13px;
    flex-wrap: wrap;
  }
  .detail-stat { color: var(--text-secondary); }
  .detail-stat strong { font-family: var(--font-mono); }
  .detail-stat.success strong { color: var(--cal-ok); }
  .detail-stat.failed strong { color: var(--cal-fail); }
  .detail-stat.running strong { color: var(--cal-run); }
</style>
