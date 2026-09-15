import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

/*
 * Legibility gate for tokens.css. Every text and status color must reach
 * WCAG AA (4.5:1) against every background it can sit on, in both themes.
 * The Svelte UI shipped greys at 2.4:1; this test is what stops that from
 * coming back one "slightly softer grey" at a time.
 */

const css = readFileSync(fileURLToPath(new URL('./tokens.css', import.meta.url)), 'utf8')

export function parseBlock(source: string, selector: string): Record<string, string> {
  const start = source.indexOf(`${selector} {`)
  if (start < 0) throw new Error(`selector not found: ${selector}`)
  const body = source.slice(start, source.indexOf('\n}', start))
  const out: Record<string, string> = {}
  for (const m of body.matchAll(/(--bk-[\w-]+):\s*([^;]+);/g)) out[m[1]] = m[2].trim()
  return out
}

export function luminance(hex: string) {
  const v = hex.replace('#', '')
  if (!/^[0-9a-f]{6}$/i.test(v)) throw new Error(`not a 6-digit hex color: ${hex}`)
  const [r, g, b] = [0, 2, 4]
    .map((i) => parseInt(v.slice(i, i + 2), 16) / 255)
    .map((c) => (c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4))
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}

export function contrast(a: string, b: string) {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x)
  return (hi + 0.05) / (lo + 0.05)
}

const dark = parseBlock(css, ':root')
const light = { ...dark, ...parseBlock(css, ":root[data-theme='light']") }

const backgrounds = [
  '--bk-color-canvas',
  '--bk-color-surface',
  '--bk-color-surface-raised',
  '--bk-color-surface-overlay',
]
const foregrounds = [
  '--bk-color-text',
  '--bk-color-text-secondary',
  '--bk-color-text-muted',
  '--bk-color-text-subtle',
  '--bk-color-accent',
  '--bk-color-success',
  '--bk-color-running',
  '--bk-color-warning',
  '--bk-color-danger',
  '--bk-color-queued',
  '--bk-color-cancelled',
]

describe.each([
  ['dark', dark],
  ['light', light],
])('%s theme contrast', (_, tokens) => {
  it('defines every audited token as a hex color', () => {
    for (const name of [...backgrounds, ...foregrounds])
      expect(tokens[name], name).toMatch(/^#[0-9a-f]{6}$/i)
  })

  it.each(foregrounds)('%s reaches 4.5:1 on every background', (fg) => {
    for (const bg of backgrounds) {
      const ratio = contrast(tokens[fg], tokens[bg])
      expect(ratio, `${fg} ${tokens[fg]} on ${bg} ${tokens[bg]} = ${ratio.toFixed(2)}`).toBeGreaterThanOrEqual(4.5)
    }
  })

  it('keeps the primary action label readable', () => {
    expect(
      contrast(tokens['--bk-color-on-primary'], tokens['--bk-color-primary']),
    ).toBeGreaterThanOrEqual(7)
  })
})

describe('contrast helper', () => {
  it('matches known WCAG reference values', () => {
    expect(contrast('#000000', '#ffffff')).toBeCloseTo(21, 5)
    expect(contrast('#777777', '#ffffff')).toBeCloseTo(4.48, 2)
  })

  it('rejects colors it cannot measure instead of passing them', () => {
    expect(() => luminance('rgba(0,0,0,.5)')).toThrow()
    expect(() => parseBlock(css, ':root[data-theme="missing"]')).toThrow()
  })
})
