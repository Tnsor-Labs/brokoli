// Client-side preview and validation for the ${...} template variables the
// engine resolves at run time. It mirrors engine/variables.go exactly for the
// three run timestamps and their filters (feature: "Date filters in variables"):
//
//   ${interval.start|shift:-1d|date:YYYYMMDD}  ->  20240313
//
// Only genuine timestamps take a filter: interval.start, interval.end and
// run.started_at. Everything else (param, var, env, secret, run.date, run.id)
// is filled in when the run starts, so the preview shows it deferred rather than
// guessing. A reference the resolver refuses stays visible in the real output,
// so the preview shows it refused too, with the same reason.

export type TemplateSample = { intervalStart: Date; intervalEnd: Date; startedAt: Date }

// A fixed, illustrative interval. The values are chosen so the doc's examples
// read cleanly: start 14 Mar 2024, the run itself the next afternoon.
export const SAMPLE: TemplateSample = {
  intervalStart: new Date('2024-03-14T00:00:00Z'),
  intervalEnd: new Date('2024-03-15T00:00:00Z'),
  startedAt: new Date('2024-03-15T14:30:00Z'),
}

export type PreviewSegment =
  | { kind: 'text'; text: string }
  | { kind: 'value'; raw: string; text: string } // a resolved run timestamp
  | { kind: 'deferred'; raw: string; ref: string } // filled in at run time
  | { kind: 'invalid'; raw: string; reason: string } // refused; stays visible in the output

export type PreviewResult = {
  segments: PreviewSegment[]
  issues: { raw: string; reason: string }[]
  // True when an interval.* reference appears — those are empty on a manual run,
  // which the engine warns about up front, so the preview does too.
  usesInterval: boolean
}

const VAR = /\$\{([^}]+)\}/g

export function previewTemplate(input: string, sample: TemplateSample = SAMPLE): PreviewResult {
  const segments: PreviewSegment[] = []
  const issues: { raw: string; reason: string }[] = []
  let usesInterval = false
  let last = 0
  for (const m of input.matchAll(VAR)) {
    const at = m.index ?? 0
    if (at > last) segments.push({ kind: 'text', text: input.slice(last, at) })
    last = at + m[0].length
    const key = m[1]
    if (key.split('|')[0].trim().startsWith('interval.')) usesInterval = true
    const seg = resolveKey(key, sample)
    segments.push(seg)
    if (seg.kind === 'invalid') issues.push({ raw: seg.raw, reason: seg.reason })
  }
  if (last < input.length) segments.push({ kind: 'text', text: input.slice(last) })
  return { segments, issues, usesInterval }
}

function timeValue(ref: string, s: TemplateSample): Date | null {
  switch (ref) {
    case 'interval.start':
      return s.intervalStart
    case 'interval.end':
      return s.intervalEnd
    case 'run.started_at':
      return s.startedAt
    default:
      return null
  }
}

function resolveKey(key: string, sample: TemplateSample): PreviewSegment {
  const raw = '${' + key + '}'
  if (!key.includes('|')) {
    const ref = key.trim()
    const t = timeValue(ref, sample)
    return t ? { kind: 'value', raw, text: formatRFC3339UTC(t) } : { kind: 'deferred', raw, ref }
  }

  const parts = key.split('|')
  const base = timeValue(parts[0].trim(), sample)
  if (!base) return { kind: 'invalid', raw, reason: 'Filters apply only to a timestamp — interval.start, interval.end or run.started_at.' }

  let t = base
  let rendered: string | null = null
  let done = false
  for (const rawFilter of parts.slice(1)) {
    const f = rawFilter.trim()
    if (done) return { kind: 'invalid', raw, reason: `The filter "${f}" comes after date:, which already rendered the value as text.` }
    const colon = f.indexOf(':')
    if (colon === -1) return { kind: 'invalid', raw, reason: `The filter "${f}" needs an argument, as in shift:-1d or date:YYYYMMDD.` }
    const name = f.slice(0, colon)
    const arg = f.slice(colon + 1)
    if (name === 'shift') {
      const ms = parseShift(arg.trim())
      if (ms === null) return { kind: 'invalid', raw, reason: `"${arg}" is not a shift; write a signed count and one of s m h d w, as in shift:-1d.` }
      t = new Date(t.getTime() + ms)
    } else if (name === 'date') {
      // The date layout is not trimmed: the engine treats a space after the
      // colon as a literal, so the preview must too.
      const s = formatDateLayout(t, arg)
      if (s === null) return { kind: 'invalid', raw, reason: `"${arg}" is not a date layout; the tokens are YYYY YY MM DD HH mm ss, and a literal may not contain the letters Y M D H S.` }
      rendered = s
      done = true
    } else {
      return { kind: 'invalid', raw, reason: `Unknown filter "${name}".` }
    }
  }
  // A statement that shifted but never formatted renders as the bare reference
  // would — RFC3339 — so ${interval.start|shift:0s} and ${interval.start} agree.
  return { kind: 'value', raw, text: rendered ?? formatRFC3339UTC(t) }
}

const UNIT_MS: Record<string, number> = { s: 1000, m: 60_000, h: 3_600_000, d: 86_400_000, w: 604_800_000 }

// A signed count and one of s m h d w. No months or years (not fixed
// durations). Mirrors parseShift in engine/variables.go: one optional sign, a
// non-negative integer, one unit letter.
export function parseShift(arg: string): number | null {
  let neg = false
  if (arg.startsWith('-')) {
    neg = true
    arg = arg.slice(1)
  } else if (arg.startsWith('+')) {
    arg = arg.slice(1)
  }
  if (arg.length < 2) return null
  const unit = UNIT_MS[arg[arg.length - 1]]
  if (unit === undefined) return null
  const digits = arg.slice(0, -1)
  if (!/^\d+$/.test(digits)) return null
  const n = Number(digits)
  if (!Number.isFinite(n)) return null
  return neg ? -(n * unit) : n * unit
}

// The letters a token is built from, and so the letters a literal may not
// contain: "YYYY-mm-DD" would otherwise render a real-looking wrong date.
const DATE_TOKEN_LETTERS = 'YyMmDdHhSs'

// Renders t through a layout of tokens — YYYY YY MM DD HH mm ss — copying every
// other byte literally, refusing a bare date-token letter. Tokens match greedily
// from the left (YYYY beats YY). Mirrors formatDateLayout in engine/variables.go.
export function formatDateLayout(t: Date, layout: string): string | null {
  if (layout === '') return null
  let out = ''
  let i = 0
  while (i < layout.length) {
    if (layout.startsWith('YYYY', i)) {
      out += pad(t.getUTCFullYear(), 4)
      i += 4
      continue
    }
    if (i + 2 <= layout.length) {
      const two = layout.slice(i, i + 2)
      let matched = true
      switch (two) {
        case 'YY':
          out += pad(t.getUTCFullYear() % 100, 2)
          break
        case 'MM':
          out += pad(t.getUTCMonth() + 1, 2)
          break
        case 'DD':
          out += pad(t.getUTCDate(), 2)
          break
        case 'HH':
          out += pad(t.getUTCHours(), 2)
          break
        case 'mm':
          out += pad(t.getUTCMinutes(), 2)
          break
        case 'ss':
          out += pad(t.getUTCSeconds(), 2)
          break
        default:
          matched = false
      }
      if (matched) {
        i += 2
        continue
      }
    }
    if (DATE_TOKEN_LETTERS.includes(layout[i])) return null
    out += layout[i]
    i++
  }
  return out
}

function pad(n: number, width: number): string {
  return String(n).padStart(width, '0')
}

function formatRFC3339UTC(t: Date): string {
  return (
    `${pad(t.getUTCFullYear(), 4)}-${pad(t.getUTCMonth() + 1, 2)}-${pad(t.getUTCDate(), 2)}` +
    `T${pad(t.getUTCHours(), 2)}:${pad(t.getUTCMinutes(), 2)}:${pad(t.getUTCSeconds(), 2)}Z`
  )
}
