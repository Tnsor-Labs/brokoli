<script lang="ts">
  import { onMount } from "svelte";
  import type { CalendarDay } from "../../lib/types";
  import { api } from "../../lib/api";
  import Panel from "./Panel.svelte";

  // A full year makes the contribution grid legible at dashboard width and
  // preserves weekly and seasonal patterns.
  export let days = 364;

  let data: CalendarDay[] = [];
  let loaded = false;

  // Fill in the days the API doesn't return. GetRunCalendarByOrg only emits
  // rows for days that had runs, so a gap in the response is a real "nothing
  // ran" day and has to be drawn as one — not skipped, which would silently
  // compress the timeline and misalign every weekday.
  function buildGrid(rows: CalendarDay[]): CalendarDay[] {
    const byDate = new Map(rows.map(r => [r.date, r]));
    const out: CalendarDay[] = [];
    const today = new Date();
    for (let i = days - 1; i >= 0; i--) {
      const d = new Date(today);
      d.setDate(today.getDate() - i);
      const key = d.toISOString().slice(0, 10);
      out.push(byDate.get(key) ?? { date: key, total: 0, success: 0, failed: 0, running: 0 });
    }
    return out;
  }

  $: grid = buildGrid(data);
  $: max = Math.max(...grid.map(d => d.total), 1);
  $: totalRuns = grid.reduce((s, d) => s + d.total, 0);
  $: totalFailed = grid.reduce((s, d) => s + d.failed, 0);
  $: activeDays = grid.filter(d => d.total > 0).length;

  // Columns are weeks, rows are weekdays — the layout everyone already
  // knows how to read from contribution graphs.
  $: weeks = (() => {
    const cols: CalendarDay[][] = [];
    for (let i = 0; i < grid.length; i += 7) cols.push(grid.slice(i, i + 7));
    return cols;
  })();

  // A day with any failure is red regardless of volume: on a triage screen,
  // "500 ran and 1 broke" must not render as a healthy square.
  function cellClass(d: CalendarDay): string {
    if (d.total === 0) return "cell cell-empty";
    if (d.failed > 0) return "cell cell-fail";
    const ratio = d.total / max;
    if (ratio > 0.66) return "cell cell-ok-3";
    if (ratio > 0.33) return "cell cell-ok-2";
    return "cell cell-ok-1";
  }

  function label(d: CalendarDay): string {
    if (d.total === 0) return `${d.date} — no runs`;
    const parts = [`${d.total} run${d.total === 1 ? "" : "s"}`];
    if (d.success) parts.push(`${d.success} ok`);
    if (d.failed) parts.push(`${d.failed} failed`);
    if (d.running) parts.push(`${d.running} running`);
    return `${d.date} — ${parts.join(", ")}`;
  }

  function monthLabel(week: CalendarDay[]): string {
    // Only label a column when its month differs from the week before, so
    // the axis reads "May Jun Jul" rather than repeating on every column.
    const first = week[0];
    if (!first) return "";
    const d = new Date(first.date + "T00:00:00");
    return d.getDate() <= 7 ? d.toLocaleDateString("en-US", { month: "short" }) : "";
  }

  export async function reload() {
    try {
      data = await api.runsCalendar(days);
    } catch {
      // Keep whatever we last drew; an empty grid would read as "nothing
      // ever ran", which is a much stronger claim than "the fetch failed".
    } finally {
      loaded = true;
    }
  }

  onMount(reload);
</script>

<Panel
  title="Activity"
  subtitle={loaded
    ? `${totalRuns} runs over ${days} days · ${activeDays} active ${activeDays === 1 ? "day" : "days"}${totalFailed > 0 ? ` · ${totalFailed} failed` : ""}`
    : ""}
  expandable={false}
  maxHeight="none"
>
  <!-- Fixed height by content: the grid is always a year of whole weeks, so
       there is nothing to cap or expand. -->
  <div class="board">
    <div class="months">
      {#each weeks as w}
        <span class="month">{monthLabel(w)}</span>
      {/each}
    </div>
    <div class="weeks">
      {#each weeks as week}
        <div class="week">
          {#each week as d (d.date)}
            <div class={cellClass(d)} title={label(d)}></div>
          {/each}
        </div>
      {/each}
    </div>
    <div class="legend">
      <span class="legend-label">Less</span>
      <span class="cell cell-empty"></span>
      <span class="cell cell-ok-1"></span>
      <span class="cell cell-ok-2"></span>
      <span class="cell cell-ok-3"></span>
      <span class="legend-label">More</span>
      <span class="legend-sep"></span>
      <span class="cell cell-fail"></span>
      <span class="legend-label">Had failures</span>
    </div>
  </div>
</Panel>

<style>
  .board {
    padding: 10px 14px 8px;
    overflow-x: auto;
  }

  /* Columns flex to use the dashboard width while keeping the cells close to
     square on wide monitors. */
  .months, .weeks { display: flex; gap: 3px; }

  .month {
    flex: 1 1 0;
    min-width: 6px;
    max-width: 26px;
    font-size: 0.5625rem;
    color: var(--text-dim);
    font-family: var(--font-mono);
    white-space: nowrap;
    height: 12px;
  }

  .week {
    display: flex;
    flex-direction: column;
    gap: 3px;
    flex: 1 1 0;
    min-width: 6px;
    max-width: 26px;
  }

  .cell {
    width: 100%;
    aspect-ratio: 1;
    border-radius: 2px;
    background: var(--bg-tertiary);
  }
  .cell-empty { background: var(--bg-tertiary); }
  .cell-ok-1 { background: var(--success); opacity: 0.3; }
  .cell-ok-2 { background: var(--success); opacity: 0.6; }
  .cell-ok-3 { background: var(--success); opacity: 0.95; }
  .cell-fail { background: var(--failed); }

  .legend {
    display: flex;
    align-items: center;
    gap: 3px;
    margin-top: 8px;
    justify-content: flex-end;
  }
  /* Legend swatches are samples, not grid columns — keep them fixed. */
  .legend .cell {
    width: 11px;
    height: 11px;
    aspect-ratio: auto;
    flex: 0 0 auto;
  }
  .legend-label {
    font-size: 0.5625rem;
    color: var(--text-dim);
    margin: 0 3px;
  }
  .legend-sep { width: 10px; }
</style>
