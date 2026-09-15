import { describe, expect, it } from 'vitest'
import { buildHeatmap, levelFor, moveIndex } from './heatmap'

// 2026-09-13 is a Sunday. Noon UTC, so no local zone can move it to another UTC day.
const NOW = Date.UTC(2026, 8, 13, 12)

describe('buildHeatmap', () => {
  it('aligns columns to weekdays, Sunday first, ending today', () => {
    const map = buildHeatmap([], 7, NOW)
    expect(map.cells.map((c) => c.date)).toEqual(['2026-09-07', '2026-09-08', '2026-09-09', '2026-09-10', '2026-09-11', '2026-09-12', '2026-09-13'])
    expect(map.weeks).toHaveLength(2)
    expect(map.weeks[0][0]).toBeNull()
    expect(map.weeks[0][1]?.date).toBe('2026-09-07')
    expect(map.weeks[1][0]?.date).toBe('2026-09-13')
    expect(map.weeks[1][0]?.isToday).toBe(true)
    expect(map.weeks[1].slice(1).every((c) => c === null)).toBe(true)
  })

  it('keys days by UTC date and counts only days that are drawn', () => {
    const map = buildHeatmap(
      [
        { date: '2026-09-06', total: 50, success: 50, failed: 0, running: 0 },
        { date: '2026-09-13', total: 6, success: 3, failed: 1, running: 1 },
        { date: '2026-09-14', total: 9, success: 9, failed: 0, running: 0 },
      ],
      7,
      NOW,
    )
    expect(map.totals).toEqual({ total: 6, success: 3, failed: 1, running: 1, other: 1, activeDays: 1 })
    expect(map.max).toBe(6)
    expect(map.cells[6]).toMatchObject({ date: '2026-09-13', other: 1, level: 4 })
  })

  it('labels months where they change', () => {
    const map = buildHeatmap([], 60, NOW)
    expect(map.months.map((m) => m.label)).toHaveLength(3)
    expect(map.months[0].column).toBe(0)
  })
})

describe('levelFor', () => {
  it('is 0 for an empty day and 4 for the busiest', () => {
    expect(levelFor(0, 10)).toBe(0)
    expect(levelFor(10, 10)).toBe(4)
    expect(levelFor(1, 1000)).toBe(1)
  })
})

describe('moveIndex', () => {
  it('moves a day vertically and a week horizontally, clamped', () => {
    expect(moveIndex(10, 'ArrowUp', 30)).toBe(9)
    expect(moveIndex(10, 'ArrowRight', 30)).toBe(17)
    expect(moveIndex(2, 'ArrowLeft', 30)).toBe(0)
    expect(moveIndex(28, 'ArrowRight', 30)).toBe(29)
    expect(moveIndex(5, 'End', 30)).toBe(29)
    expect(moveIndex(5, 'Enter', 30)).toBeNull()
  })
})
