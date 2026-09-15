// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
import { cycle, goTarget, isTypingTarget, rank, score, splitMatch, type ShellPage } from './commands'

describe('score', () => {
  it('prefers a whole match, then a prefix, then a word start, then anywhere', () => {
    expect(score('orders', 'Orders')).toBe(100)
    expect(score('ord', 'Orders sync')).toBe(80)
    expect(score('sync', 'Orders sync')).toBe(60)
    expect(score('rder', 'Orders')).toBe(40)
    expect(score('x', 'Orders')).toBe(0)
    expect(score('', 'Orders')).toBe(0)
  })
  it('treats dots, dashes, underscores and slashes as word breaks', () => {
    expect(score('schema', 'warehouse_schema')).toBe(60)
    expect(score('prod', 'db.prod')).toBe(60)
  })
})

describe('rank', () => {
  const items = [
    { label: 'Daily orders', keywords: ['finance'] },
    { label: 'Orders backfill' },
    { label: 'Customers', keywords: ['orders team'] },
    { label: 'Inventory' },
  ]
  it('orders by match quality and matches keywords with less weight', () => {
    expect(rank('orders', items).map((i) => i.label)).toEqual(['Orders backfill', 'Daily orders', 'Customers'])
  })
  it('keeps every item for an empty query', () => {
    expect(rank('  ', items)).toHaveLength(4)
  })
  it('finds items by keyword only', () => {
    expect(rank('finance', items).map((i) => i.label)).toEqual(['Daily orders'])
  })
})

describe('splitMatch', () => {
  it('splits around the first match, keeping the original case', () => {
    expect(splitMatch('Daily Orders', 'orders')).toEqual(['Daily ', 'Orders', ''])
    expect(splitMatch('Daily', 'x')).toBeNull()
  })
})

describe('goTarget', () => {
  const pages: ShellPage[] = [
    { to: '/pipelines', label: 'Pipelines', group: 'Build', key: 'p' },
    { to: '/plugins', label: 'Plugins', group: 'Build' },
  ]
  it('finds the page for a key, case-insensitively, and nothing for an unused key', () => {
    expect(goTarget('P', pages)?.to).toBe('/pipelines')
    expect(goTarget('z', pages)).toBeUndefined()
  })
})

describe('isTypingTarget', () => {
  it('recognises fields and editable content', () => {
    const input = document.createElement('input')
    const editor = document.createElement('div')
    editor.setAttribute('contenteditable', 'true')
    const inner = document.createElement('span')
    editor.appendChild(inner)
    expect(isTypingTarget(input)).toBe(true)
    expect(isTypingTarget(inner)).toBe(true)
    expect(isTypingTarget(document.createElement('button'))).toBe(false)
    expect(isTypingTarget(null)).toBe(false)
  })
})

describe('cycle', () => {
  it('wraps at both ends', () => {
    expect(cycle(2, 1, 3)).toBe(0)
    expect(cycle(0, -1, 3)).toBe(2)
    expect(cycle(0, 1, 0)).toBe(-1)
  })
})
