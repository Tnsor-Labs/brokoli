import type { CalendarDay } from '@brokoli/api'

/*
 * Run calendar model.
 *
 * Days are UTC dates. The server groups runs by the UTC date of started_at
 * (SQLite stores UTC timestamps; Postgres uses the session time zone, UTC by
 * default), so the grid is keyed the same way. The previous page keyed its
 * grid by local date, which moved runs near midnight to the wrong day or
 * dropped them while still counting them in the totals.
 */

export const DAY_MS = 86_400_000

export const utcKey = (time: number) => new Date(time).toISOString().slice(0, 10)

export type HeatLevel = 0 | 1 | 2 | 3 | 4

export interface HeatCell {
  index: number
  date: string
  time: number
  total: number
  success: number
  failed: number
  running: number
  /** Pending, cancelled, blocked and skipped runs: counted in total, with no column of their own. */
  other: number
  level: HeatLevel
  isToday: boolean
}

export interface HeatTotals {
  total: number
  success: number
  failed: number
  running: number
  other: number
  activeDays: number
}

export interface Heatmap {
  cells: HeatCell[]
  /** Sunday-first columns; slots before the first day and after today are null. */
  weeks: (HeatCell | null)[][]
  months: { column: number; label: string }[]
  max: number
  totals: HeatTotals
}

/** Log scale, so one very busy day does not flatten every other day to the lowest shade. */
export function levelFor(total: number, max: number): HeatLevel {
  if (total <= 0 || max <= 0) return 0
  const r = Math.log(total + 1) / Math.log(max + 1)
  return r > 0.75 ? 4 : r > 0.5 ? 3 : r > 0.25 ? 2 : 1
}

const monthLabel = new Intl.DateTimeFormat(undefined, { month: 'short', timeZone: 'UTC' })
const longDay = new Intl.DateTimeFormat(undefined, { weekday: 'long', year: 'numeric', month: 'long', day: 'numeric', timeZone: 'UTC' })

export const formatUtcDay = (time: number) => longDay.format(time)

export function buildHeatmap(data: CalendarDay[], days: number, now: number): Heatmap {
  const n = new Date(now)
  const today = Date.UTC(n.getUTCFullYear(), n.getUTCMonth(), n.getUTCDate())
  const byDate = new Map(data.map((d) => [d.date, d]))
  const cells: HeatCell[] = []
  for (let i = days - 1; i >= 0; i--) {
    const time = today - i * DAY_MS
    const date = utcKey(time)
    const d = byDate.get(date)
    const total = d?.total ?? 0
    const success = d?.success ?? 0
    const failed = d?.failed ?? 0
    const running = d?.running ?? 0
    cells.push({
      index: cells.length,
      date,
      time,
      total,
      success,
      failed,
      running,
      other: Math.max(0, total - success - failed - running),
      level: 0,
      isToday: i === 0,
    })
  }
  const max = cells.reduce((m, c) => Math.max(m, c.total), 0)
  const totals: HeatTotals = { total: 0, success: 0, failed: 0, running: 0, other: 0, activeDays: 0 }
  for (const c of cells) {
    c.level = levelFor(c.total, max)
    totals.total += c.total
    totals.success += c.success
    totals.failed += c.failed
    totals.running += c.running
    totals.other += c.other
    if (c.total > 0) totals.activeDays++
  }

  const slots: (HeatCell | null)[] = [...Array<null>(cells.length ? new Date(cells[0].time).getUTCDay() : 0).fill(null), ...cells]
  while (slots.length % 7) slots.push(null)
  const weeks: (HeatCell | null)[][] = []
  for (let i = 0; i < slots.length; i += 7) weeks.push(slots.slice(i, i + 7))

  // A label where the month changes, skipped when it would crowd the previous one.
  const months: Heatmap['months'] = []
  let previousMonth = -1
  weeks.forEach((week, column) => {
    const first = week.find((c) => c !== null)
    if (!first) return
    const month = new Date(first.time).getUTCMonth()
    if (month !== previousMonth) {
      previousMonth = month
      const last = months[months.length - 1]
      if (!last || column - last.column >= 3) months.push({ column, label: monthLabel.format(first.time) })
    }
  })

  return { cells, weeks, months, max, totals }
}

/** Arrow keys move like the grid looks: up and down a day, left and right a week. */
export function moveIndex(index: number, key: string, length: number): number | null {
  const step: Record<string, number> = { ArrowUp: -1, ArrowDown: 1, ArrowLeft: -7, ArrowRight: 7 }
  if (key === 'Home') return 0
  if (key === 'End') return length - 1
  if (!(key in step)) return null
  return Math.min(length - 1, Math.max(0, index + step[key]))
}
