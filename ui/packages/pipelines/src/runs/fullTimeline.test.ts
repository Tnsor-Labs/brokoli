import { describe, expect, it } from 'vitest'
import type { NodeRun } from '@brokoli/api'
import { attemptSpan, epoch, groupByAttempt, niceStep, retryCount, rowsWritten, runElapsed, runWindow, ticks, timelineRows, upstreamOf } from './fullTimeline'

const T0 = Date.parse('2026-09-13T10:00:00Z')
const at = (ms: number) => new Date(T0 + ms).toISOString()

function nr(node_id: string, over: Partial<NodeRun> = {}): NodeRun {
  return { id: `${node_id}-${over.attempt ?? 0}`, run_id: 'r1', node_id, status: 'success', row_count: 0, started_at: null, duration_ms: 0, ...over }
}

describe('epoch', () => {
  it('treats Go zero time, null and garbage as absent', () => {
    expect(epoch('0001-01-01T00:00:00Z')).toBeNull()
    expect(epoch(null)).toBeNull()
    expect(epoch('not a date')).toBeNull()
    expect(epoch(at(5))).toBe(T0 + 5)
  })
})

describe('attemptSpan', () => {
  it('grows a running attempt with the clock', () => {
    expect(attemptSpan(nr('a', { status: 'running', started_at: at(0) }), T0 + 700)).toEqual({ start: T0, end: T0 + 700, duration: 700 })
  })
  it('gives no span to a skipped node that never started', () => {
    expect(attemptSpan(nr('a', { status: 'skipped' }), T0)).toBeNull()
  })
})

describe('runWindow', () => {
  it('falls back to the first node start when the run start is null', () => {
    const run = { started_at: null, finished_at: null, status: 'failed', node_runs: [nr('a', { started_at: at(200), duration_ms: 100 }), nr('b', { started_at: at(400), duration_ms: 50 })] }
    expect(runWindow(run, T0 + 10_000)).toEqual({ origin: T0 + 200, end: T0 + 450, total: 250 })
    expect(runElapsed(run, T0 + 10_000)).toBe(250)
  })
  it('ignores skipped nodes without a start instead of producing a negative range', () => {
    const run = { started_at: at(0), finished_at: at(100), status: 'success', node_runs: [nr('a', { status: 'skipped' }), nr('b', { started_at: at(10), duration_ms: 90 })] }
    const win = runWindow(run, T0)!
    expect(win.origin).toBe(T0)
    expect(win.total).toBe(100)
  })
  it('is null when nothing has started', () => {
    expect(runWindow({ started_at: null, finished_at: null, status: 'pending', node_runs: [nr('a', { status: 'pending' })] }, T0)).toBeNull()
  })
  it('keeps a valid 1ms scale for a run where everything took 0ms', () => {
    const run = { started_at: at(0), finished_at: at(0), status: 'success', node_runs: [nr('a', { started_at: at(0) })] }
    expect(runWindow(run, T0 + 5000)).toEqual({ origin: T0, end: T0, total: 1 })
    expect(runElapsed(run, T0 + 5000)).toBe(0)
    expect(ticks(1, 800).offsets).toEqual([0, 1])
  })
  it('extends to now while the run is active', () => {
    const run = { started_at: at(0), finished_at: null, status: 'running', node_runs: [nr('a', { status: 'running', started_at: at(0) })] }
    expect(runWindow(run, T0 + 3000)?.total).toBe(3000)
    expect(runElapsed(run, T0 + 3000)).toBe(3000)
  })
})

describe('ticks', () => {
  it('picks steps in milliseconds, seconds, minutes and hours', () => {
    expect(ticks(800, 800).step).toBe(100)
    expect(ticks(45_000, 800).step).toBe(5000)
    expect(ticks(20 * 60_000, 800).step).toBe(5 * 60_000)
    expect(ticks(5 * 3_600_000, 800).step).toBe(3_600_000)
    expect(niceStep(3 * 86_400_000 + 1)).toBe(4 * 86_400_000)
  })
  it('recomputes a finer step when the track is wider (zoomed in)', () => {
    expect(ticks(45_000, 800 * 8).step).toBeLessThan(ticks(45_000, 800).step)
  })
  it('starts at zero and never passes the total', () => {
    const { offsets } = ticks(1000, 400)
    expect(offsets[0]).toBe(0)
    expect(Math.max(...offsets)).toBeLessThanOrEqual(1000)
  })
})

describe('timelineRows', () => {
  const nodes = [
    { id: 'a', name: 'Load', type: 'source_file' },
    { id: 'b', name: '', type: 'transform' },
    { id: 'c', name: 'Clean', type: 'transform' },
    { id: 'd', name: 'Write', type: 'sink_db' },
  ]
  const runs = [
    nr('a', { started_at: at(200), row_count: 10 }),
    nr('c', { started_at: at(100), attempt: 1, row_count: 8 }),
    nr('c', { started_at: at(50), status: 'failed', attempt: 0 }),
    nr('gone', { started_at: at(300) }),
    nr('d', { started_at: at(400), row_count: 7 }),
  ]
  const rows = timelineRows(nodes, runs)

  it('orders by first start, puts never-started rows last and keeps nodes the definition lost', () => {
    expect(rows.map((r) => r.id)).toEqual(['c', 'a', 'gone', 'd', 'b'])
    expect(rows.find((r) => r.id === 'gone')?.orphan).toBe(true)
    expect(rows.find((r) => r.id === 'b')?.name).toBe('b')
  })
  it('sorts attempts oldest first and counts retries', () => {
    expect(rows[0].attempts.map((a) => a.attempt)).toEqual([0, 1])
    expect(retryCount(rows)).toBe(1)
  })
  it('counts rows written by sinks only', () => {
    expect(rowsWritten(rows)).toBe(7)
    expect(rowsWritten(timelineRows(nodes.slice(0, 3), runs))).toBeNull()
  })
})

describe('upstreamOf and groupByAttempt', () => {
  it('lists distinct upstream nodes', () => {
    expect(upstreamOf('c', [{ from: 'a', to: 'c' }, { from: 'b', to: 'c' }, { from: 'a', to: 'c' }, { from: 'c', to: 'd' }])).toEqual(['a', 'b'])
  })
  it('groups log lines by attempt only when attempts are present', () => {
    expect(groupByAttempt([{ m: 1 }, { m: 2 }] as { attempt?: number }[])).toEqual([{ attempt: null, lines: [{ m: 1 }, { m: 2 }] }])
    // The server omits attempt 0, so a line without one belongs to the first try once retries exist.
    const groups = groupByAttempt([{ attempt: 1 }, { attempt: 0 }, {}, { attempt: 1 }])
    expect(groups.map((g) => [g.attempt, g.lines.length])).toEqual([
      [0, 2],
      [1, 2],
    ])
  })
})
