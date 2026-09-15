/*
 * Display formatting. Every function accepts the loose shapes the API
 * returns (ISO strings, null, zero-value Go times) and returns a visible
 * placeholder for "no value" rather than an empty string, so a missing
 * timestamp never looks like a rendering bug.
 */

export const EMPTY = '-'

/** Go's zero time.Time marshals as 0001-01-01T00:00:00Z; treat it as absent. */
export function toDate(value: string | number | Date | null | undefined): Date | null {
  if (value === null || value === undefined || value === '') return null
  const d = value instanceof Date ? value : new Date(value)
  if (Number.isNaN(d.getTime()) || d.getUTCFullYear() <= 1) return null
  return d
}

const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' })
const UNITS: [Intl.RelativeTimeFormatUnit, number][] = [
  ['year', 31_536_000],
  ['month', 2_592_000],
  ['week', 604_800],
  ['day', 86_400],
  ['hour', 3_600],
  ['minute', 60],
  ['second', 1],
]

export function formatRelative(value: string | number | Date | null | undefined, now = Date.now()): string {
  const d = toDate(value)
  if (!d) return EMPTY
  const seconds = Math.round((d.getTime() - now) / 1000)
  if (Math.abs(seconds) < 10) return 'just now'
  for (const [unit, size] of UNITS) {
    if (Math.abs(seconds) >= size || unit === 'second') return rtf.format(Math.round(seconds / size), unit)
  }
  return EMPTY
}

const dateTime = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' })
const timeOnly = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit' })

export function formatDateTime(value: string | number | Date | null | undefined): string {
  const d = toDate(value)
  return d ? dateTime.format(d) : EMPTY
}

export function formatTime(value: string | number | Date | null | undefined): string {
  const d = toDate(value)
  return d ? timeOnly.format(d) : EMPTY
}

export function formatDuration(ms: number | null | undefined): string {
  if (ms === null || ms === undefined || !Number.isFinite(ms) || ms < 0) return EMPTY
  if (ms < 1000) return `${Math.round(ms)}ms`
  const s = ms / 1000
  if (s < 60) return `${s < 10 ? s.toFixed(1) : Math.round(s)}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ${Math.round(s % 60)}s`
  const h = Math.floor(m / 60)
  return `${h}h ${m % 60}m`
}

export function durationBetween(
  start: string | number | Date | null | undefined,
  end: string | number | Date | null | undefined,
  now = Date.now(),
): number | null {
  const a = toDate(start)
  if (!a) return null
  const b = toDate(end)
  return (b ? b.getTime() : now) - a.getTime()
}

const integer = new Intl.NumberFormat()
const compact = new Intl.NumberFormat(undefined, { notation: 'compact', maximumFractionDigits: 1 })

export function formatNumber(value: number | null | undefined, opts: { compact?: boolean } = {}): string {
  if (value === null || value === undefined || !Number.isFinite(value)) return EMPTY
  return (opts.compact && Math.abs(value) >= 10_000 ? compact : integer).format(value)
}

export function formatBytes(bytes: number | null | undefined): string {
  if (bytes === null || bytes === undefined || !Number.isFinite(bytes)) return EMPTY
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let v = bytes
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v < 10 && i ? v.toFixed(1) : Math.round(v)} ${units[i]}`
}
