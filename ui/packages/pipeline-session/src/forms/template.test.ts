import { describe, expect, it } from 'vitest'
import { formatDateLayout, parseShift, previewTemplate, SAMPLE } from './template'

// Render a template to the flat string a reader would compare, treating a
// deferred or refused reference as staying visible (which the engine does).
function render(input: string): string {
  return previewTemplate(input)
    .segments.map((s) => (s.kind === 'text' ? s.text : s.kind === 'value' ? s.text : s.raw))
    .join('')
}

function firstIssue(input: string): string | undefined {
  return previewTemplate(input).issues[0]?.reason
}

describe('previewTemplate — documented examples', () => {
  it('formats interval.start with a date layout', () => {
    expect(render('${interval.start|date:YYYYMMDD}')).toBe('20240314')
  })
  it('shifts back a day then formats', () => {
    expect(render('${interval.start|shift:-1d|date:YYYY-MM-DD}')).toBe('2024-03-13')
  })
  it('formats run.started_at with time', () => {
    expect(render('${run.started_at|date:YYYYMMDD-HHmm}')).toBe('20240315-1430')
  })
  it('chains shifts left to right', () => {
    // 14 Mar 00:00 minus 1d minus 2h = 12 Mar 22:00
    expect(render('${interval.start|shift:-1d|shift:-2h|date:YYYYMMDD-HHmm}')).toBe('20240312-2200')
  })
  it('renders a real delivery path', () => {
    expect(render('outbound/orders-${interval.start|shift:-1d|date:YYYYMMDD}.csv')).toBe('outbound/orders-20240313.csv')
  })
})

describe('previewTemplate — resolution shapes', () => {
  it('renders a bare timestamp as RFC3339 UTC', () => {
    expect(render('${interval.start}')).toBe('2024-03-14T00:00:00Z')
  })
  it('renders a shifted-but-unformatted timestamp as RFC3339', () => {
    expect(render('${interval.end|shift:-1d}')).toBe('2024-03-14T00:00:00Z')
  })
  it('distinguishes MM (month) from mm (minute)', () => {
    expect(render('${interval.start|date:MM-mm}')).toBe('03-00')
  })
  it('leaves a non-timestamp reference deferred and visible', () => {
    const { segments } = previewTemplate('${param.since}')
    expect(segments).toEqual([{ kind: 'deferred', raw: '${param.since}', ref: 'param.since' }])
  })
  it('flags whether an interval reference is used', () => {
    expect(previewTemplate('${interval.start|date:YYYY}').usesInterval).toBe(true)
    expect(previewTemplate('${run.started_at|date:YYYY}').usesInterval).toBe(false)
  })
})

describe('previewTemplate — refusals stay visible', () => {
  const refused: [string, RegExp][] = [
    ['${param.since|date:YYYY}', /only to a timestamp/],
    ['${run.date|date:YYYY}', /only to a timestamp/],
    ['${interval.start|shift:1y}', /is not a shift/],
    ['${interval.start|shift:-1.5d}', /is not a shift/],
    ['${interval.start|date:yyyy-mm-dd}', /is not a date layout/],
    ['${interval.start|date:YYYY-MM-DD-D}', /is not a date layout/],
    ['${interval.start|date:YYYY|shift:-1d}', /comes after date/],
    ['${interval.start|frob:x}', /Unknown filter/],
    ['${interval.start|shift}', /needs an argument/],
  ]
  it.each(refused)('refuses %s and keeps it visible', (input, reason) => {
    expect(render(input)).toBe(input)
    expect(firstIssue(input)).toMatch(reason)
  })
})

describe('parseShift', () => {
  it('parses signed counts and units', () => {
    expect(parseShift('-1d')).toBe(-86_400_000)
    expect(parseShift('+2h')).toBe(7_200_000)
    expect(parseShift('30m')).toBe(1_800_000)
    expect(parseShift('0s')).toBe(0)
  })
  it('rejects bad shifts', () => {
    expect(parseShift('1y')).toBeNull()
    expect(parseShift('-1.5d')).toBeNull()
    expect(parseShift('d')).toBeNull()
    expect(parseShift('')).toBeNull()
    expect(parseShift('5')).toBeNull()
  })
})

describe('formatDateLayout', () => {
  const t = SAMPLE.startedAt // 2024-03-15T14:30:00Z
  it('matches tokens greedily and copies literals', () => {
    expect(formatDateLayout(t, 'YYYY/MM/DD')).toBe('2024/03/15')
    expect(formatDateLayout(t, 'YY')).toBe('24')
    expect(formatDateLayout(t, 'HH:mm:ss')).toBe('14:30:00')
  })
  it('keeps a space after the colon as a literal', () => {
    expect(formatDateLayout(t, ' YYYY')).toBe(' 2024')
  })
  it('refuses a bare date-token letter and an empty layout', () => {
    expect(formatDateLayout(t, 'YYYY-M')).toBeNull()
    expect(formatDateLayout(t, '')).toBeNull()
  })
})
