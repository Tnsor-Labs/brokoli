import { describe, expect, it } from 'vitest'
import type { DashboardRun, DeadLetter, PipelineSummary, SchedulerEntry } from '@brokoli/api'
import { finishedRate, firstLine, fleetRows, groupDeadLetters, groupRecent, needsAttention, upcoming } from './model'

function pipeline(id: string, over: Partial<PipelineSummary> = {}): PipelineSummary {
  return {
    id,
    name: id,
    description: '',
    schedule: '',
    enabled: true,
    tags: null,
    node_count: 1,
    edge_count: 0,
    depends_on: null,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    last_run_status: '',
    runs_total: 0,
    runs_success: 0,
    runs_failed: 0,
    runs_running: 0,
    run_history: null,
    ...over,
  }
}

const run = (pipeline_id: string, run_id: string, status = 'success'): DashboardRun => ({ pipeline_id, pipeline_name: pipeline_id, run_id, status })

describe('finishedRate', () => {
  it('is null when nothing finished, not 100', () => {
    expect(finishedRate(0, 0)).toBeNull()
  })
  it('never rounds a failure up to 100', () => {
    expect(finishedRate(999, 1)).toBe(99)
  })
  it('is exact at the ends', () => {
    expect(finishedRate(5, 0)).toBe(100)
    expect(finishedRate(0, 3)).toBe(0)
  })
})

describe('firstLine', () => {
  it('keeps only the first line and truncates', () => {
    expect(firstLine('boom\nstack')).toBe('boom')
    expect(firstLine('abcdef', 4)).toBe('abc…')
    expect(firstLine(undefined)).toBe('')
  })
})

describe('needsAttention', () => {
  it('lists only pipelines whose latest run failed, newest failure first', () => {
    const list = needsAttention([
      pipeline('recovered', { last_run_status: 'success', runs_failed: 4 }),
      pipeline('old', { last_run_status: 'failed', last_run_at: '2026-09-01T00:00:00Z' }),
      pipeline('new', { last_run_status: 'FAILED', last_run_at: '2026-09-10T00:00:00Z' }),
      pipeline('never'),
    ])
    expect(list.map((p) => p.id)).toEqual(['new', 'old'])
  })
})

describe('groupRecent', () => {
  it('groups all runs of a pipeline, not only consecutive ones, and counts hidden pipelines', () => {
    const { groups, hidden } = groupRecent([run('a', '1'), run('b', '2'), run('a', '3'), run('c', '4')], 2)
    expect(groups.map((g) => [g.pipelineId, g.runs.map((r) => r.run_id)])).toEqual([
      ['a', ['1', '3']],
      ['b', ['2']],
    ])
    expect(hidden).toBe(1)
  })
})

describe('fleetRows', () => {
  it('ranks failing, then running, then enabled, then paused, and keys next runs by id', () => {
    const summary = [
      pipeline('paused', { enabled: false, schedule: '@daily' }),
      pipeline('ok', { schedule: '@hourly' }),
      pipeline('twin', { name: 'ok', schedule: '@daily' }),
      pipeline('busy', { runs_running: 1 }),
      pipeline('bad', { last_run_status: 'failed' }),
    ]
    const schedule: SchedulerEntry[] = [
      { pipeline_id: 'ok', pipeline_name: 'ok', schedule: '@hourly', next_run: '2026-09-13T13:00:00Z' },
      { pipeline_id: 'twin', pipeline_name: 'ok', schedule: '@daily', next_run: '2026-09-14T00:00:00Z' },
      { pipeline_id: 'paused', pipeline_name: 'paused', schedule: '@daily', next_run: '2026-09-14T00:00:00Z' },
    ]
    const rows = fleetRows(summary, [], schedule)
    expect(rows.map((r) => r.pipeline.id)).toEqual(['bad', 'busy', 'ok', 'twin', 'paused'])
    expect(rows.find((r) => r.pipeline.id === 'ok')?.next).toBe('2026-09-13T13:00:00Z')
    expect(rows.find((r) => r.pipeline.id === 'twin')?.next).toBe('2026-09-14T00:00:00Z')
    expect(rows.find((r) => r.pipeline.id === 'paused')?.next).toBeUndefined()
  })
})

describe('upcoming', () => {
  it('drops entries for pipelines the user cannot see and sorts by time', () => {
    const schedule: SchedulerEntry[] = [
      { pipeline_id: 'late', pipeline_name: 'late', schedule: '', next_run: '2026-09-14T00:00:00Z' },
      { pipeline_id: 'other-org', pipeline_name: 'x', schedule: '', next_run: '2026-09-13T00:00:00Z' },
      { pipeline_id: 'soon', pipeline_name: 'soon', schedule: '', next_run: '2026-09-13T01:00:00Z' },
      { pipeline_id: 'bad', pipeline_name: 'bad', schedule: '', next_run: '' },
    ]
    expect(upcoming(schedule, new Set(['late', 'soon', 'bad']), 5).map((s) => s.pipeline_id)).toEqual(['soon', 'late'])
  })
})

describe('groupDeadLetters', () => {
  const entry = (id: string, error: string, created_at: string, node_name = 'load'): DeadLetter => ({
    id,
    pipeline_id: 'p',
    pipeline_name: 'P',
    run_id: 'r',
    error,
    node_id: 'n',
    node_name,
    payload: '',
    created_at,
    resolved: false,
  })
  it('groups by pipeline, node and first error line, newest group first', () => {
    const groups = groupDeadLetters([
      entry('1', 'timeout\nat line 1', '2026-09-01T00:00:00Z'),
      entry('2', 'timeout\nat line 9', '2026-09-03T00:00:00Z'),
      entry('3', 'refused', '2026-09-02T00:00:00Z'),
      entry('4', 'timeout', '2026-09-04T00:00:00Z', 'extract'),
    ])
    expect(groups.map((g) => [g.nodeName, g.error, g.ids])).toEqual([
      ['extract', 'timeout', ['4']],
      ['load', 'timeout', ['1', '2']],
      ['load', 'refused', ['3']],
    ])
    expect(groups[1].newest).toBe('2026-09-03T00:00:00Z')
  })
})
